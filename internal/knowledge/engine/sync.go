package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/domain"
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
	engine      *Engine
	control     SyncControl
	content     SyncContent
	root, owner string
	ttl         time.Duration
	syncGit     func(context.Context, string, string, string, map[string]string) (string, string, []gitsync.FileInfo, []string, []string, error)
}

func NewSyncWorker(e *Engine, control SyncControl, content SyncContent, root, owner string) *SyncWorker {
	return &SyncWorker{engine: e, control: control, content: content, root: root, owner: owner, ttl: 30 * time.Second, syncGit: gitsync.SyncPublicHTTPS}
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
	summary := ""
	if err != nil {
		summary = strings.ReplaceAll(err.Error(), filepath.Join(w.root, "knowledge-cache"), "[knowledge-cache]")
		if chars := []rune(summary); len(chars) > 2048 {
			summary = string(chars[:2048])
		}
	}
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
	prefix := strings.TrimSuffix(src.PathPrefix, "/")
	if prefix != "" && (path.Clean(prefix) != prefix || strings.HasPrefix(prefix, "/") || prefix == ".." || strings.HasPrefix(prefix, "../") || strings.Contains(prefix, "\\")) {
		return "", 0, errors.New("invalid path prefix")
	}
	dir, head, files, _, _, err := w.syncGit(ctx, filepath.Join(w.root, "knowledge-cache", src.ID), src.GitURL, src.Branch, nil)
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
	ok, err := w.control.RenewKnowledgeSync(src.ID, owner, w.ttl)
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
