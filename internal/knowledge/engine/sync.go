package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/knowledge/engine/chunk"
	"agentflow/internal/knowledge/engine/gitsync"
)

type SyncControl interface {
	PublishKnowledgeSync(context.Context, string, string, string, int, func(context.Context) error) (bool, error)
	KnowledgeSources(string) []domain.KnowledgeSource
	ClaimKnowledgeSync(string, string, time.Duration) (bool, error)
	RenewKnowledgeSync(string, string, time.Duration) (bool, error)
	FinishKnowledgeSync(string, string, string, int, string) (bool, error)
}
type SyncContent interface {
	ReplaceGitKnowledge(context.Context, string, []domain.KnowledgeDocument, []domain.KnowledgeChunk) error
}
type SyncWorker struct {
	engine           *Engine
	control          SyncControl
	content          SyncContent
	cacheRoot, owner string
	ttl              time.Duration
	syncGit          func(context.Context, string, string, string, map[string]string) (string, string, []gitsync.FileInfo, []string, []string, error)
}

// NewSyncWorker 的 cacheRoot 是独立于沙箱工作区的同步缓存目录。工作副本只
// 是可重建的缓存，不写数据库，也不放进沙箱回收管理的目录。
func NewSyncWorker(e *Engine, control SyncControl, content SyncContent, cacheRoot, owner string) *SyncWorker {
	return &SyncWorker{engine: e, control: control, content: content, cacheRoot: cacheRoot, owner: owner, ttl: 30 * time.Second, syncGit: gitsync.SyncPublicHTTPS}
}

// cacheDir 返回一个来源的工作副本目录；来源 ID 已在 importGit 里校验为
// 单层目录名，不能越出 cacheRoot。
func (w *SyncWorker) cacheDir(sourceID string) string {
	return filepath.Join(w.cacheRoot, sourceID)
}

// StartSyncLoop returns immediately. The execution-role gate belongs here as
// well as at startup, so an API instance cannot clone or index sources.
func (w *SyncWorker) StartSyncLoop(ctx context.Context, executeRuns bool) {
	if !executeRuns {
		return
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			if err := w.SyncOnce(ctx); err != nil {
				log.Printf("knowledge sync: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (w *SyncWorker) SyncOnce(ctx context.Context) error {
	for _, src := range w.control.KnowledgeSources("") {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if src.Kind != "git" {
			continue
		}
		// Each attempt has a distinct owner token, including after a local restart.
		owner := fmt.Sprintf("%s-%s", w.owner, w.engine.newID())
		ok, err := w.control.ClaimKnowledgeSync(src.ID, owner, w.ttl)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err = w.syncClaim(ctx, src, owner); err != nil {
			return err
		}
	}
	return nil
}
func (w *SyncWorker) syncClaim(parent context.Context, src domain.KnowledgeSource, owner string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		ticker := time.NewTicker(w.ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ok, err := w.control.RenewKnowledgeSync(src.ID, owner, w.ttl)
				if err != nil || !ok {
					cancel()
					return
				}
			}
		}
	}()
	head, count, err := w.importGit(ctx, src, owner)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// 用户可见的 last_error 只放能懂的中文；原始错误（含 git 命令和缓存路径）
	// 只写服务端日志。错误归类见 syncFailureMessage。
	log.Printf("knowledge sync %s failed: %v", src.ID, err)
	summary := syncFailureMessage(err)
	ok, finishErr := w.control.FinishKnowledgeSync(src.ID, owner, head, count, summary)
	if finishErr != nil {
		return finishErr
	}
	if !ok {
		return errors.New("knowledge sync lease lost")
	}
	return nil
}
func (w *SyncWorker) importGit(ctx context.Context, src domain.KnowledgeSource, owner string) (string, int, error) {
	u, err := url.Parse(src.GitURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", 0, errors.New("Git source requires public HTTPS without credentials")
	}
	if src.ID == "" || filepath.Base(src.ID) != src.ID || src.ID == "." || src.ID == ".." {
		return "", 0, errors.New("invalid source ID")
	}
	prefix, ok := knowledge.NormalizePathPrefix(src.PathPrefix)
	if !ok {
		return "", 0, errors.New("invalid path prefix")
	}
	dir, head, files, _, _, err := w.syncGit(ctx, w.cacheDir(src.ID), src.GitURL, src.Branch, nil)
	if err != nil {
		return "", 0, err
	}
	docs := []domain.KnowledgeDocument{}
	chunks := []domain.KnowledgeChunk{}
	for _, f := range files {
		if prefix != "" && !strings.HasPrefix(f.Path, prefix+"/") {
			continue
		}
		b, err := gitsync.ReadRepoFile(dir, f.Path)
		if err != nil {
			return "", 0, err
		}
		body := string(b)
		lines := splitBody(body)
		title := chunk.Title(body)
		docs = append(docs, domain.KnowledgeDocument{SourceID: src.ID, Path: f.Path, Title: title, Body: body, LineCount: len(lines), SHA256: sha256Hex(body), Commit: head})
		chunks = append(chunks, buildChunks(src.ID, f.Path, title, head, 0, lines)...)
	}
	ok, err = w.control.RenewKnowledgeSync(src.ID, owner, w.ttl)
	if err != nil {
		return "", 0, err
	}
	if !ok {
		return "", 0, errors.New("knowledge sync lease lost")
	}
	ok, err = w.control.PublishKnowledgeSync(ctx, src.ID, owner, head, len(docs), func(ctx context.Context) error { return w.content.ReplaceGitKnowledge(ctx, src.ID, docs, chunks) })
	if err != nil {
		return "", 0, err
	}
	if !ok {
		return "", 0, errors.New("knowledge sync lease lost before publication")
	}
	return head, len(docs), nil
}

// syncFailureMessage 把 Git 原始错误归类成用户能懂的中文。它不返回原始
// 命令、stderr 或本地缓存路径；这些只留在服务端日志里。
func syncFailureMessage(err error) string {
	if err == nil {
		return ""
	}
	raw := strings.ToLower(err.Error())
	switch {
	case containsAny(raw, "remote branch", "couldn't find remote ref", "could not find remote ref", "not found in upstream", "unknown revision"):
		return "分支不存在，请检查分支名"
	case containsAny(raw,
		"could not read username", "terminal prompts disabled", "authentication failed",
		"permission denied", "repository not found", "could not find repository",
		"repository does not exist", "project you were looking for could not be found"):
		return "仓库不存在，或该地址需要凭据"
	case containsAny(raw,
		"could not resolve host", "unable to access", "failed to connect", "connection refused",
		"connection timed out", "operation timed out", "timed out", "network is unreachable",
		"tls handshake timeout", "server certificate verification failed", "empty reply from server",
		"connection reset by peer"):
		return "无法连接仓库服务器，请检查网络或地址"
	case containsAny(raw, "http.curloptresolve", "git with http"):
		return "当前服务器的 Git 不支持安全同步，请联系管理员"
	case containsAny(raw, "public https url required", "requires public https", "git host must resolve", "cannot resolve public git host"):
		return "Git 地址必须是公开 HTTPS 仓库地址"
	case strings.Contains(raw, "invalid path prefix"):
		return "路径前缀无效，请使用仓库内的相对目录"
	case strings.Contains(raw, "非法远程地址或分支"):
		return "Git 地址或分支无效，请检查后重试"
	case strings.Contains(raw, "invalid source id"):
		return "来源标识无效，请重新登记来源"
	case containsAny(raw, "errfiletoolarge", "file too large", "文件超出"):
		return "仓库里有 Markdown 文件过大，无法索引"
	case containsAny(raw, "路径越出工作副本", "工作副本不可用", "not a git repository", "镜像工作区存在本地改动", "同步缓存"):
		return "同步缓存异常，请重新同步"
	case containsAny(raw, "context canceled", "context deadline exceeded"):
		return "同步已中断，请重新同步"
	default:
		return "同步失败，请稍后重试"
	}
}

func containsAny(s string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(s, term) {
			return true
		}
	}
	return false
}
