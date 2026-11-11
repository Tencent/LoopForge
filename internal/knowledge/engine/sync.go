package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/knowledge/engine/chunk"
	"agentflow/internal/knowledge/engine/gitsync"
	"agentflow/internal/knowledge/gitremote"
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
	return &SyncWorker{engine: e, control: control, content: content, cacheRoot: cacheRoot, owner: owner, ttl: 30 * time.Second, syncGit: gitsync.SyncRemote}
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
		// worker 启动时 reconcile 一次：模型/维度变更（换环境变量+重启）后
		// 在这里换 collection 并全量重建向量。仅在 executeRuns 实例运行。
		if err := ReconcileVector(ctx); err != nil {
			log.Printf("knowledge vector reconcile: %v", err)
		}
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
		if reader, ok := w.control.(interface {
			KnowledgeSource(string) (domain.KnowledgeSource, bool)
		}); ok {
			current, found := reader.KnowledgeSource(src.ID)
			if !found {
				continue
			}
			src = current
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
	// 错误与日志均只保留分类后的中文提示，远端输出可能包含凭据。
	log.Printf("knowledge sync %s failed: %s", src.ID, syncFailureMessage(err))
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
	if _, err := gitremote.Parse(src.GitURL); err != nil {
		return "", 0, errors.New("public Git URL required")
	}
	credential, err := knowledge.DecryptGitCredential(src.ID, src.CredentialCiphertext)
	if err != nil {
		return "", 0, err
	}
	ctx = gitsync.WithCredential(ctx, credential)
	if src.ID == "" || filepath.Base(src.ID) != src.ID || src.ID == "." || src.ID == ".." {
		return "", 0, errors.New("invalid source ID")
	}
	prefix, ok := knowledge.NormalizePathPrefix(src.PathPrefix)
	if !ok {
		return "", 0, errors.New("invalid path prefix")
	}
	cacheDir := w.cacheDir(src.ID)
	if src.CredentialCiphertext != "" {
		// A replaced grant revokes the old lease before its Git process exits.
		// Keep each encrypted grant's reusable cache separate, including when two
		// workers share a cache root. Ciphertext has a fresh nonce on every save.
		cacheDir += "-" + sha256Hex(src.CredentialCiphertext)
	}
	dir, head, files, _, _, err := w.syncGit(ctx, cacheDir, src.GitURL, src.Branch, nil)
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
	// Git 同步发布后，由持有租约的实例增量更新该来源的向量（best-effort）。
	afterSourceSynced(ctx, src.ID)
	return head, len(docs), nil
}

// syncFailureMessage 把 Git 原始错误归类成用户能懂的中文。它不返回原始
// 命令、stderr 或本地缓存路径。
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
		"permission denied"):
		return "仓库鉴权失败，请检查来源凭据及仓库读取权限"
	case containsAny(raw, "repository not found", "could not find repository", "repository does not exist", "project you were looking for could not be found"):
		return "仓库不存在，请检查仓库地址及凭据的访问范围"
	case strings.Contains(raw, "host key verification failed"):
		return "SSH 服务器公钥验证失败，请核对 known_hosts 记录"
	case strings.Contains(raw, "来源凭据无法解密"):
		return "来源凭据无法解密，请检查服务会话密钥或重新配置凭据"
	case containsAny(raw,
		"could not resolve host", "unable to access", "failed to connect", "connection refused",
		"connection timed out", "operation timed out", "timed out", "network is unreachable",
		"tls handshake timeout", "server certificate verification failed", "empty reply from server",
		"connection reset by peer"):
		return "无法连接仓库服务器，请检查网络或地址"
	case containsAny(raw, "http.curloptresolve", "git with http"):
		return "当前服务器的 Git 不支持安全同步，请联系管理员"
	case containsAny(raw, "public https url required", "public git url required", "requires public https", "git host must resolve", "cannot resolve public git host"):
		return "Git 地址必须是指向公网服务器的 HTTPS 或 SSH 地址"
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
