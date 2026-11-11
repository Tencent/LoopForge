package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/knowledge/engine/gitsync"
	"agentflow/internal/knowledge/hosting"
)

// writebackStore 是引擎写回需要的控制面能力。*repo.Store 实现它；测试可以
// 用内存实现替换，不改 Upstream 契约。
type writebackStore interface {
	ClaimKnowledgeWrite(id, owner string, ttl time.Duration) (bool, error)
	RenewKnowledgeWrite(id, owner string, ttl time.Duration) (bool, error)
	FinishKnowledgeWrite(id, owner string) (bool, error)
	PutKnowledgeWriteback(domain.KnowledgeWriteback) error
	KnowledgeWritebackBySubmission(submissionID string) (domain.KnowledgeWriteback, bool)
}

// writebackReconcileControl 是后台合并状态刷新需要的控制面能力。
type writebackReconcileControl interface {
	KnowledgeWritebacksNeedingRefresh(before time.Time, limit int) ([]domain.KnowledgeWriteback, error)
	UpdateKnowledgeWritebackState(id, status, mrURL, mrProvider, mrState, note, errText string, mrIID int, checkedAt time.Time) error
	KnowledgeSource(id string) (domain.KnowledgeSource, bool)
	QueueKnowledgeResync(sourceID string) (bool, error)
}

func (e *Engine) initWriteback() {
	e.writeCacheRoot = defaultWritebackCacheRoot()
	e.writeLeaseTTL = 60 * time.Second
	e.gitWrite = gitsync.CommitAndPushRemote
	e.gitMergeProbe = gitsync.RemoteContainsCommitRemote
	e.hostingClientFor = func(repo hosting.Repo, token string) *hosting.Client {
		return &hosting.Client{BaseURL: repo.APIBase(), Token: token}
	}
}

func defaultWritebackCacheRoot() string {
	if dir := strings.TrimSpace(os.Getenv("AGENTFLOW_KNOWLEDGE_CACHE_DIR")); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".agentflow", "knowledge-cache")
	}
	return filepath.Join(os.TempDir(), "agentflow-knowledge-cache")
}

func (e *Engine) writebackOwner() string {
	if id := strings.TrimSpace(e.newID()); id != "" {
		return "kb-writeback-" + id
	}
	return fmt.Sprintf("kb-writeback-%d", time.Now().UTC().UnixNano())
}

func (e *Engine) writebackCacheDir(src domain.KnowledgeSource) string {
	dir := filepath.Join(e.writeCacheRoot, src.ID)
	if src.CredentialCiphertext != "" {
		// Keep each credential grant's cache separate, matching the sync worker.
		dir += "-" + sha256Hex(src.CredentialCiphertext)
	}
	return dir
}

// WriteGitDocument publishes one reviewed document to a Git source: commit on
// a new branch, push with the source's canWrite credential, then create a merge
// request on supported hosting platforms. The source lease excludes a
// concurrent sync or writeback. Git failures are classified into user-facing
// Chinese and never echo remote output or credentials.
func (e *Engine) WriteGitDocument(ctx context.Context, req knowledge.WritebackRequest) (knowledge.WriteResult, error) {
	sourceID := strings.TrimSpace(req.Source)
	path, ok := knowledge.NormalizeDocPath(req.Path)
	if !ok {
		return knowledge.WriteResult{}, errors.New("写回路径必须是仓库内的 .md 相对路径")
	}
	src, found := e.control.KnowledgeSource(sourceID)
	if !found {
		return knowledge.WriteResult{}, fmt.Errorf("来源 %s 不存在", sourceID)
	}
	if src.Kind != "git" {
		return knowledge.WriteResult{}, errors.New("只有 Git 来源需要写回分支")
	}
	if src.ID == "" || filepath.Base(src.ID) != src.ID || src.ID == "." || src.ID == ".." {
		return knowledge.WriteResult{}, errors.New("来源标识无效，请重新登记来源")
	}
	prefix, ok := knowledge.NormalizePathPrefix(src.PathPrefix)
	if !ok {
		return knowledge.WriteResult{}, errors.New("来源的路径前缀无效")
	}
	if prefix != "" && !strings.HasPrefix(path, prefix+"/") {
		return knowledge.WriteResult{}, fmt.Errorf("文档路径必须位于来源的路径前缀 %s 下", prefix)
	}
	if strings.TrimSpace(req.Content) == "" {
		return knowledge.WriteResult{}, errors.New("写回正文不能为空")
	}
	credential, err := knowledge.DecryptGitCredential(src.ID, src.CredentialCiphertext)
	if err != nil {
		return knowledge.WriteResult{}, errors.New("来源凭据不可用，请检查服务会话密钥或重新配置凭据")
	}
	if credential == nil || !credential.CanWrite {
		return knowledge.WriteResult{}, errors.New("来源凭据未授予写入权限，请先在来源上配置可写凭据")
	}
	store, ok := e.control.(writebackStore)
	if !ok {
		return knowledge.WriteResult{}, errors.New("知识写回存储不可用")
	}
	baseBranch := strings.TrimSpace(src.Branch)
	if baseBranch == "" {
		baseBranch = "main"
	}
	owner := e.writebackOwner()
	claimed, err := store.ClaimKnowledgeWrite(src.ID, owner, e.writeLeaseTTL)
	if err != nil {
		return knowledge.WriteResult{}, errors.New("领取来源写回租约失败，请稍后重试")
	}
	if !claimed {
		return knowledge.WriteResult{}, errors.New("该来源正在同步或写回，请稍后重试")
	}
	defer func() { _, _ = store.FinishKnowledgeWrite(src.ID, owner) }()
	runCtx, stopLease := keepWriteLease(ctx, store, src.ID, owner, e.writeLeaseTTL)
	defer stopLease()

	record, found := store.KnowledgeWritebackBySubmission(req.SubmissionID)
	if !found {
		record = domain.KnowledgeWriteback{
			ID: "kwb-" + e.newID(), SubmissionID: strings.TrimSpace(req.SubmissionID),
			SourceID: src.ID, ProjectID: req.ProjectID,
		}
	}
	if record.Status == domain.KnowledgeWritebackMerged {
		return knowledge.WriteResult{}, errors.New("该条目已合并，无需重复写回")
	}
	record.Path = path
	record.Status = domain.KnowledgeWritebackPending
	record.Attempts++
	record.Error = ""
	record.Note = ""
	if err := store.PutKnowledgeWriteback(record); err != nil {
		return knowledge.WriteResult{}, errors.New("保存写回记录失败，请稍后重试")
	}
	fail := func(cause error) (knowledge.WriteResult, error) {
		message := writebackFailureMessage(cause)
		record.Status = domain.KnowledgeWritebackFailed
		record.Error = message
		_ = store.PutKnowledgeWriteback(record)
		return knowledge.WriteResult{}, errors.New(message)
	}
	branch := strings.TrimSpace(record.Branch)
	if branch == "" {
		branch = writebackBranch(req.SubmissionID, record.ID)
	}
	record.Branch = branch

	gitCtx := gitsync.WithCredential(runCtx, credential)
	result, err := e.gitWrite(gitCtx, e.writebackCacheDir(src), src.GitURL, baseBranch, gitsync.WriteOptions{
		Branch: branch, Path: path, Content: req.Content,
		Message:        writebackCommitMessage(req.Title, path),
		ExpectedRemote: record.Commit,
	})
	if err != nil {
		return fail(err)
	}
	record.Commit = result.Commit
	record.Status = domain.KnowledgeWritebackBranchPushed
	record.Note = ""
	if err := store.PutKnowledgeWriteback(record); err != nil {
		return knowledge.WriteResult{}, errors.New("保存写回记录失败，请稍后重试")
	}

	note := ""
	hostRepo, supported := hosting.Detect(src.GitURL)
	switch {
	case !supported:
		note = fmt.Sprintf("已推送分支 %s。当前托管平台暂不支持自动创建合并请求，请在仓库网页创建并合并后再同步来源。", branch)
	case credential.Kind != "https" || strings.TrimSpace(credential.Token) == "":
		note = fmt.Sprintf("已推送分支 %s。来源凭据不能创建合并请求，请在仓库网页创建并合并后再同步来源。", branch)
	default:
		client := e.hostingClientFor(hostRepo, credential.Token)
		ref, mrErr := client.CreateMergeRequest(runCtx, hostRepo, hosting.MergeRequest{
			Title:        writebackMergeRequestTitle(req.Title, path),
			Body:         writebackMergeRequestBody(src.Name, path, req.SubmissionID),
			SourceBranch: branch,
			TargetBranch: baseBranch,
		})
		if mrErr != nil {
			note = fmt.Sprintf("已推送分支 %s，但合并请求创建失败（%s），请在仓库网页创建并合并后同步来源。", branch, mrErr.Error())
		} else {
			record.MRURL = ref.URL
			record.MRProvider = string(ref.Provider)
			record.MRIID = ref.IID
			record.MRState = ref.State
			record.Status = domain.KnowledgeWritebackMROpen
			note = "已创建合并请求，合并后会自动同步索引。"
		}
	}
	now := e.now().UTC()
	record.Note = note
	record.CheckedAt = &now
	if err := store.PutKnowledgeWriteback(record); err != nil {
		return knowledge.WriteResult{}, errors.New("保存写回记录失败，请稍后重试")
	}
	return knowledge.WriteResult{
		Source: src.ID, Path: path, Commit: record.Commit,
		Branch: branch, MRURL: record.MRURL, MRProvider: record.MRProvider,
		MRStatus: record.Status, Message: note,
	}, nil
}

func keepWriteLease(parent context.Context, store writebackStore, sourceID, owner string, ttl time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	interval := ttl / 3
	if interval <= 0 {
		interval = ttl
	}
	if interval <= 0 {
		interval = time.Second
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ok, err := store.RenewKnowledgeWrite(sourceID, owner, ttl)
				if err != nil || !ok {
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() { cancel(); <-done })
	}
	return ctx, stop
}

func writebackBranch(submissionID, recordID string) string {
	seed := strings.TrimSpace(submissionID)
	if seed == "" {
		seed = strings.TrimSpace(recordID)
	}
	var b strings.Builder
	b.WriteString("kb/writeback-")
	for _, r := range seed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), ".-/")
	if len(out) > 100 {
		out = out[:100]
	}
	if strings.HasSuffix(out, ".") || strings.HasSuffix(out, "/") {
		out = strings.TrimRight(out, "./")
	}
	return out
}

func writebackCommitMessage(title, path string) string {
	title = oneLine(title, 60)
	if title == "" {
		return "docs: add reviewed knowledge " + path
	}
	return "docs: 写回知识 " + title
}

func writebackMergeRequestTitle(title, path string) string {
	title = oneLine(title, 80)
	if title == "" {
		title = path
	}
	return "知识：" + title
}

func writebackMergeRequestBody(sourceName, path, submissionID string) string {
	name := strings.TrimSpace(sourceName)
	if name == "" {
		name = "知识来源"
	}
	submissionID = strings.TrimSpace(submissionID)
	if submissionID == "" {
		submissionID = "-"
	}
	return fmt.Sprintf("来自知识中心审核队列，审核通过后写回。\n\n- 来源：%s\n- 路径：%s\n- 审核条目：%s\n", name, path, submissionID)
}

func oneLine(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return text
}

// writebackFailureMessage maps Git and transport failures to stable Chinese
// messages. Raw stderr, cache paths and credentials never reach the user.
func writebackFailureMessage(err error) string {
	if err == nil {
		return "写回 Git 来源失败，请稍后重试"
	}
	if errors.Is(err, gitsync.ErrNoWriteChanges) {
		return gitsync.ErrNoWriteChanges.Error()
	}
	raw := strings.ToLower(err.Error())
	switch {
	case containsAny(raw, "write lease", "lease lost", "租约"):
		return "来源写回租约已失效，请重试"
	case containsAny(raw, "authentication failed", "permission denied", "could not read username", "terminal prompts disabled"):
		return "仓库鉴权失败，请检查来源凭据的写入权限"
	case containsAny(raw, "push rejected", "protected branch", "pre-receive"):
		return "推送被仓库拒绝，请检查分支保护规则或凭据写入权限"
	case containsAny(raw, "non fast forward", "fetch first", "写回分支已被其他写入更新"):
		return "写回分支已被其他写入更新，请重新发布"
	case containsAny(raw, "remote branch missing"):
		return "来源分支不存在，请检查分支名"
	case containsAny(raw, "repository not found"):
		return "仓库不存在，请检查仓库地址及凭据的访问范围"
	case containsAny(raw, "host key verification failed"):
		return "SSH 服务器公钥验证失败，请核对 known_hosts 记录"
	case containsAny(raw, "failed to connect", "could not resolve", "unable to access", "timed out", "network is unreachable"):
		return "无法连接仓库服务器，请检查网络或地址"
	case containsAny(raw, "public git url required", "public https", "must resolve", "git host"):
		return "Git 地址必须是指向公网服务器的 HTTPS 或 SSH 地址"
	case containsAny(raw, "来源凭据无法解密", "无法准备来源凭据", "invalid source credential", "credential protocol mismatch"):
		return "来源凭据不可用，请检查服务会话密钥或重新配置凭据"
	case containsAny(raw, "写回分支无法应用", "来源分支已更新"):
		return "来源分支已更新，请重新发布"
	case containsAny(raw, "context canceled", "context deadline exceeded"):
		return "写回已中断，请稍后重试"
	default:
		return "写回 Git 来源失败，请稍后重试"
	}
}

// StartWritebackReconcileLoop is folded into StartSyncLoop; this method is
// used by tests and by the loop's periodic tick.
func (w *SyncWorker) maybeReconcileWritebacks(ctx context.Context) {
	if w.engine == nil || w.reconcileEvery <= 0 {
		return
	}
	now := time.Now()
	last := time.Unix(0, w.lastReconcile.Load())
	if !last.IsZero() && now.Sub(last) < w.reconcileEvery {
		return
	}
	if !w.reconciling.CompareAndSwap(false, true) {
		return
	}
	w.lastReconcile.Store(now.UnixNano())
	go func() {
		defer w.reconciling.Store(false)
		w.reconcileWritebacks(ctx)
	}()
}

func (w *SyncWorker) reconcileWritebacks(ctx context.Context) {
	control, ok := w.control.(writebackReconcileControl)
	if !ok || w.engine == nil {
		return
	}
	items, err := control.KnowledgeWritebacksNeedingRefresh(time.Now().UTC().Add(-w.reconcileStale), 20)
	if err != nil {
		log.Printf("knowledge writeback refresh: %v", err)
		return
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		src, found := control.KnowledgeSource(item.SourceID)
		if !found || src.Kind != "git" {
			continue
		}
		w.reconcileWriteback(ctx, control, item, src)
	}
}

func (w *SyncWorker) reconcileWriteback(ctx context.Context, control writebackReconcileControl, item domain.KnowledgeWriteback, src domain.KnowledgeSource) {
	now := time.Now().UTC()
	if item.MRURL != "" && item.MRProvider != "" && item.MRIID > 0 {
		repo, supported := hosting.Detect(src.GitURL)
		if supported {
			if credential, err := knowledge.DecryptGitCredential(src.ID, src.CredentialCiphertext); err == nil && credential != nil {
				client := w.engine.hostingClientFor(repo, credential.Token)
				state, stateErr := client.MergeRequestState(ctx, repo, item.MRIID)
				if stateErr == nil {
					switch state {
					case "merged":
						w.finishWritebackMerged(ctx, control, item, src, now)
						return
					case "closed":
						_ = control.UpdateKnowledgeWritebackState(item.ID, domain.KnowledgeWritebackClosed, "", "", "closed", "合并请求已关闭，未合并到来源分支。", "", 0, now)
						return
					default:
						_ = control.UpdateKnowledgeWritebackState(item.ID, domain.KnowledgeWritebackMROpen, "", "", "opened", "", "", 0, now)
						return
					}
				}
				// An API failure falls through to the Git ancestry probe: the
				// merge may already be visible on the source branch.
			}
		}
	}
	credential, err := knowledge.DecryptGitCredential(src.ID, src.CredentialCiphertext)
	if err != nil || credential == nil {
		_ = control.UpdateKnowledgeWritebackState(item.ID, item.Status, "", "", item.MRState, "", "", 0, now)
		return
	}
	if w.engine.gitMergeProbe == nil {
		_ = control.UpdateKnowledgeWritebackState(item.ID, item.Status, "", "", item.MRState, "", "", 0, now)
		return
	}
	probeCtx := gitsync.WithCredential(ctx, credential)
	baseBranch := strings.TrimSpace(src.Branch)
	if baseBranch == "" {
		baseBranch = "main"
	}
	merged, probeErr := w.engine.gitMergeProbe(probeCtx, w.engine.writebackCacheDir(src), src.GitURL, baseBranch, item.Commit)
	if probeErr != nil {
		log.Printf("knowledge writeback probe %s: %v", item.ID, probeErr)
		_ = control.UpdateKnowledgeWritebackState(item.ID, item.Status, "", "", item.MRState, "", "", 0, now)
		return
	}
	if merged {
		w.finishWritebackMerged(ctx, control, item, src, now)
		return
	}
	_ = control.UpdateKnowledgeWritebackState(item.ID, item.Status, "", "", item.MRState, "", "", 0, now)
}

func (w *SyncWorker) finishWritebackMerged(ctx context.Context, control writebackReconcileControl, item domain.KnowledgeWriteback, src domain.KnowledgeSource, now time.Time) {
	queued, err := control.QueueKnowledgeResync(src.ID)
	if err != nil {
		log.Printf("knowledge writeback queue sync %s: %v", item.ID, err)
	}
	if !queued {
		// The source is being synced or written by another instance. Keep the
		// writeback open so the next tick retries the queue step.
		_ = control.UpdateKnowledgeWritebackState(item.ID, domain.KnowledgeWritebackMROpen, "", "", "merged", "已合并，等待来源同步", "", 0, now)
		return
	}
	note := "已合并，来源已排队增量同步。"
	if item.MRURL == "" {
		note = "来源分支已包含写回提交，已排队增量同步。"
	}
	_ = control.UpdateKnowledgeWritebackState(item.ID, domain.KnowledgeWritebackMerged, "", "", "merged", note, "", 0, now)
}
