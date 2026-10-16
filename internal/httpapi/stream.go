package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	stdsync "sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/managedcli"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

// streamHandlers 持有 SSE 流式端点与 clarify 会话状态（取消登记、
// 每仓库串行锁）。issues 领域经注入的指针发起 clarify。
type streamHandlers struct {
	*deps
	clarifyMu       stdsync.Mutex
	clarifyCancels  map[string]context.CancelFunc
	clarifyRepoMu   stdsync.Mutex
	clarifyRepoLock map[string]*stdsync.Mutex
	jobs            *clarifyHub
	clarifyRun      clarifyExecFunc
}

func (h *streamHandlers) streamRun(c *gin.Context) {
	runID := c.Param("id")
	w := c.Writer
	r := c.Request
	updates, cancel, err := h.engine.Subscribe(runID)
	if err != nil {
		c.JSON(404, map[string]string{"error": err.Error()})
		return
	}
	defer cancel()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	after, _ := strconv.ParseInt(c.Query("after"), 10, 64)
	logsAfter, _ := strconv.ParseInt(c.Query("logsAfter"), 10, 64)
	for {
		select {
		case <-r.Context().Done():
			return
		case run, open := <-updates:
			if !open {
				return
			}
			delta := make([]domain.TelemetryEvent, 0)
			logDelta := make([]domain.LogRecord, 0)
			for _, event := range run.Telemetry {
				if event.Sequence > after {
					delta = append(delta, slimTelemetryEvent(event))
				}
			}
			after = run.TelemetrySequence
			for _, record := range run.Logs {
				if record.Sequence > logsAfter {
					logDelta = append(logDelta, slimLogRecord(record))
				}
			}
			logsAfter = run.LogSequence
			if len(delta) > 80 {
				delta = delta[len(delta)-80:]
			}
			if len(logDelta) > 80 {
				logDelta = logDelta[len(logDelta)-80:]
			}
			run = publicLiveRun(run)
			raw, _ := json.Marshal(map[string]any{"run": run, "telemetryDelta": delta, "logDelta": logDelta})
			_, _ = fmt.Fprintf(w, "id: %d\nevent: run\ndata: %s\n\n", run.Revision, raw)
			c.Writer.Flush()
		case <-ping.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			c.Writer.Flush()
		}
	}
}

type chatClarifyRequest struct {
	ProjectID    string `json:"projectId"`
	RepositoryID string `json:"repositoryId"`
	// RepositoryIDs lists every repository the requirement touches
	// (multi-repository aware); the legacy single repositoryId stays as
	// fallback and mirrors the first entry.
	RepositoryIDs []string `json:"repositoryIds,omitempty"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Message       string   `json:"message"`
	Model         string   `json:"model,omitempty"`
	SessionID     string   `json:"sessionId,omitempty"`
	JobID         string   `json:"jobId,omitempty"`
	History       []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"history"`
}

func (h *streamHandlers) chatClarify(c *gin.Context) {
	var request chatClarifyRequest
	if !decode(c, &request) {
		return
	}
	if request.ProjectID == "" {
		c.JSON(400, map[string]string{"error": "projectId is required"})
		return
	}
	if _, ok := h.repo.Project(request.ProjectID); !ok {
		c.JSON(400, map[string]string{"error": "project not found"})
		return
	}
	seenRepos := map[string]bool{}
	var repoIDs []string
	for _, id := range append(append([]string(nil), request.RepositoryIDs...), request.RepositoryID) {
		id = strings.TrimSpace(id)
		if id == "" || seenRepos[id] {
			continue
		}
		seenRepos[id] = true
		repoIDs = append(repoIDs, id)
	}
	issue := domain.Issue{
		ProjectID:     request.ProjectID,
		RepositoryIDs: repoIDs,
		Title:         request.Title,
		Description:   request.Description,
	}
	if len(repoIDs) > 0 {
		// Clarification deep-dives the primary repository; the full list is
		// surfaced in the prompt so multi-repo scope stays visible.
		issue.RepositoryID = repoIDs[0]
	}
	if issue.Title == "" {
		issue.Title = request.Message
	}
	if issue.Description == "" {
		issue.Description = request.Message
	}
	hydrateClarifyIssueFromText(&issue, clarifyRequestTexts(request)...)
	h.runChatClarify(c, issue, request)
}

// statDir reports whether path exists and is a directory.
func statDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// resolveCodeBuddyBin locates the codebuddy executable. It first honors an
// explicit override (AGENTFLOW_CODEBUDDY_BIN), then the managed inventory if
// configured (read-only), otherwise consults PATH, and finally
// probes well-known install locations. The service PATH may not include the
// user's ~/.local/bin, so relying on PATH alone is not enough.
func resolveCodeBuddyBin() (string, error) {
	if override := strings.TrimSpace(os.Getenv("AGENTFLOW_CODEBUDDY_BIN")); override != "" {
		if fileExecutable(override) {
			return override, nil
		}
	}
	if root := strings.TrimSpace(os.Getenv("AGENTFLOW_MANAGED_CLI_ROOT")); root != "" {
		inventory, err := managedcli.ReadInventory(root)
		if err != nil {
			return "", fmt.Errorf("读取托管 CodeBuddy 清单失败: %w", err)
		}
		if binary, ok := inventory.BinaryFor("codebuddy"); ok {
			return binary, nil
		}
		return "", fmt.Errorf("托管 CodeBuddy 尚未安装，请检查 worker 的 CLI 安装状态")
	}
	if p, err := exec.LookPath("codebuddy"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	if strings.TrimSpace(home) == "" {
		home = "/root"
	}
	candidates := []string{
		filepath.Join(home, ".local", "bin", "codebuddy"),
		"/usr/local/bin/codebuddy",
		"/usr/bin/codebuddy",
	}
	for _, c := range candidates {
		if fileExecutable(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("codebuddy 可执行文件未找到（PATH 及常见安装路径均无）；可设置 AGENTFLOW_CODEBUDDY_BIN 指定绝对路径")
}

// fileExecutable reports whether path is a regular file with an executable bit.
func fileExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

// clarifyRepoCacheRoot is the base directory holding per-repository checkouts
// used during requirement clarification.
func clarifyRepoCacheRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = "/root"
	}
	return filepath.Join(home, ".agentflow", "clarify-cache")
}

// sanitizeDirName turns an arbitrary repository name into a safe single-path
// segment (letters, digits, dot, dash, underscore), falling back to the id.
func sanitizeDirName(name, fallback string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return fallback
	}
	return out
}

// repoLock returns a per-repository mutex so concurrent clarifications of the
// same repository don't clone/fetch into the same directory simultaneously.
func (h *streamHandlers) repoLock(repoID string) *stdsync.Mutex {
	h.clarifyRepoMu.Lock()
	defer h.clarifyRepoMu.Unlock()
	if h.clarifyRepoLock == nil {
		h.clarifyRepoLock = map[string]*stdsync.Mutex{}
	}
	mu, ok := h.clarifyRepoLock[repoID]
	if !ok {
		mu = &stdsync.Mutex{}
		h.clarifyRepoLock[repoID] = mu
	}
	return mu
}

// prepareClarifyRepo ensures a local checkout of repo exists under the clarify
// cache directory (named after the repository) and, when refresh is true,
// pulls it to the tip of the base branch. It degrades gracefully: on failure
// it reuses an existing (stale) checkout if present, otherwise reports the
// reason so the caller can fall back to /tmp.
var gitCLIColor = regexp.MustCompile(`\x1b\[[0-9;]*m|\[[0-9]{1,3}(?:;[0-9]{1,3})*m`)

func stripGitCLINoise(s string) string {
	return strings.Join(strings.Fields(gitCLIColor.ReplaceAllString(s, "")), " ")
}

// Returns (dir, analyzed, reason).
func (h *streamHandlers) prepareClarifyRepo(ctx context.Context, repo domain.Repository, responsibleUserID int64, refresh bool) (string, bool, string) {
	dirName := sanitizeDirName(repo.Name, repo.ID)
	cacheDir := filepath.Join(clarifyRepoCacheRoot(), dirName)

	mu := h.repoLock(repo.ID)
	mu.Lock()
	defer mu.Unlock()

	hadCheckout := statDir(filepath.Join(cacheDir, ".git"))

	// Reuse an existing checkout without refreshing on later turns.
	if hadCheckout && !refresh {
		return cacheDir, true, ""
	}

	token := engine.ResolveClarifyGitToken(h.repo, responsibleUserID)
	prepCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	if _, err := engine.PrepareClarifyWorkspace(prepCtx, cacheDir, repo, token); err != nil {
		if hadCheckout {
			// Degrade to the previous (possibly stale) checkout.
			return cacheDir, true, ""
		}
		return "", false, "仓库代码拉取失败：" + stripGitCLINoise(err.Error())
	}
	return cacheDir, true, ""
}

func (h *streamHandlers) runChatClarify(c *gin.Context, issue domain.Issue, request chatClarifyRequest) {
	if strings.TrimSpace(issue.ID) != "" {
		h.handleIssueClarify(c, issue, request)
		return
	}
	h.handleComposerClarify(c, issue, request)
}

func (h *streamHandlers) executeClarify(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(event string, payload any)) (any, string, map[string]any, error) {
	if emit == nil {
		emit = func(string, any) {}
	}
	emit("thought", map[string]string{"kind": "status", "text": "正在准备分析…"})
	responsible, responsibleErr := h.repo.ResponsibleUser(issue)
	if responsibleErr != nil {
		return nil, "", nil, responsibleErr
	}
	extras := clarifyRequestTexts(request)
	hydrateClarifyIssueFromText(&issue, extras...)
	workItemType := "需求"
	focus := "目标、范围、用户场景、约束和可验证的验收标准"
	if isBugStyleWorkItem(issue) {
		workItemType = "Issue"
		focus = "实际现象、复现条件、期望行为、影响范围、约束和可验证的验收标准"
	}
	ratingProject, ratingProjectFound := h.repo.Project(issue.ProjectID)
	ratingPrompt := clarificationRatingPrompt(clarificationRatingDimensions(issue, ratingProject, ratingProjectFound))
	modelCtx, cancelModel := context.WithCancel(ctx)
	defer cancelModel()
	var toolSession *AgentToolSession
	var toolFiles agentToolFiles
	if err := ensureClarifySessionDirs(responsible.ID); err != nil {
		return nil, "", nil, err
	}
	sess, files, err := h.openClarifyToolSession(modelCtx, responsible, issue, cancelModel)
	if err != nil {
		logAgentToolEvent("session-fallback", "mode=clarify", "err="+truncateRunes(err.Error(), 160))
		return nil, "", nil, err
	}
	toolSession = sess
	toolFiles = files
	defer sess.Close()
	defer files.Cleanup()
	intro := fmt.Sprintf("你是研发工作项分析师，正在澄清一张已有的%s。这是多轮澄清：结合「对话」里已经问过和回答过的内容继续，不要重复已问过的问题。", workItemType)
	mcpStep := "2. 若下文列出了可用 MCP 工具：必须先用这些工具读取 TAPD/工蜂原单；TAPD 还要同步读评论；正文或评论里的 iWiki 链接在 iWiki 工具可用时也要同步读取。不要声称看不到评论或文档。"
	if strings.TrimSpace(issue.ID) == "" {
		intro = "你是研发工作项分析师，正在澄清一条尚未建单的需求草稿。这是多轮澄清：结合「对话」里已经问过和回答过的内容继续，不要重复已问过的问题。"
		if issue.ExternalRef == nil {
			mcpStep = "2. 若下文列出了可用 MCP 工具：描述或对话里出现 TAPD/工蜂/iWiki 链接时必须先调用对应工具读取；TAPD 还要同步读评论。没有链接不要为了找原单去 list。不要声称看不到评论或文档。"
		}
	}
	systemPrompt := clarifySystemPrompt(intro, mcpStep, focus, ratingPrompt)
	var prompt strings.Builder
	prompt.WriteString(systemPrompt)
	prompt.WriteString("\n\n## 需求\n标题：")
	prompt.WriteString(issue.Title)
	prompt.WriteString("\n描述：")
	prompt.WriteString(issue.Description)
	if len(request.History) > 0 {
		prompt.WriteString("\n\n## 对话（按时间）\n")
		for _, h := range request.History {
			role := "用户"
			if strings.EqualFold(h.Role, "assistant") {
				role = "分析师"
			}
			content := strings.TrimSpace(h.Content)
			if len(content) > 4000 {
				content = content[:4000] + "…"
			}
			prompt.WriteString(role)
			prompt.WriteString("：")
			prompt.WriteString(content)
			prompt.WriteString("\n")
		}
	} else if strings.TrimSpace(request.Message) != "" {
		prompt.WriteString("\n\n## 对话\n用户：")
		prompt.WriteString(request.Message)
		prompt.WriteString("\n")
	}
	prompt.WriteString("\n请基于以上对话继续澄清，完成后调用 submit_clarification。")
	cbPath, cbErr := resolveCodeBuddyBin()
	if cbErr != nil {
		return nil, "", nil, cbErr
	}
	workDir := ""
	repoInfo := map[string]any{"analyzed": false, "reason": "该 Issue 未绑定代码仓库"}
	// Resolve the analysis repository list: repositoryIds (multi-repository
	// aware) with the legacy single repositoryId / project default fallback.
	repoIDs := issue.RepositoryIDs
	if len(repoIDs) == 0 && issue.RepositoryID != "" {
		repoIDs = []string{issue.RepositoryID}
	}
	if len(repoIDs) == 0 {
		if project, ok := h.repo.Project(issue.ProjectID); ok && strings.TrimSpace(project.DefaultRepositoryID) != "" {
			repoIDs = []string{project.DefaultRepositoryID}
		}
	}
	if len(repoIDs) > 0 {
		emit("thought", map[string]string{"kind": "status", "text": "正在检出代码仓库…"})
	}
	type repoAnalysis struct {
		id, name, dirName, dir, reason string
		analyzed                       bool
	}
	var analyses []repoAnalysis
	for _, id := range repoIDs {
		repo, ok := h.repo.Repository(id)
		if !ok {
			analyses = append(analyses, repoAnalysis{id: id, reason: "仓库不存在"})
			continue
		}
		switch {
		case repo.LocalPath != "" && statDir(repo.LocalPath):
			// Explicit local path present on this host: use it as-is.
			analyses = append(analyses, repoAnalysis{id: repo.ID, name: repo.Name, dir: repo.LocalPath, analyzed: true})
		case strings.TrimSpace(repo.CloneURL) != "":
			emit("thought", map[string]string{"kind": "status", "text": "正在检出 " + repo.Name + "…"})
			dir, analyzed, reason := h.prepareClarifyRepo(ctx, repo, responsible.ID, len(request.History) == 0)
			analyses = append(analyses, repoAnalysis{id: repo.ID, name: repo.Name, dir: dir, analyzed: analyzed, reason: reason})
		case repo.LocalPath != "":
			analyses = append(analyses, repoAnalysis{id: repo.ID, name: repo.Name, reason: "仓库本地副本不可访问"})
		default:
			analyses = append(analyses, repoAnalysis{id: repo.ID, name: repo.Name, reason: "仓库未配置本地路径或克隆地址"})
		}
	}
	var analyzedRepos []*repoAnalysis
	for i := range analyses {
		if analyses[i].analyzed && analyses[i].dir != "" {
			analyzedRepos = append(analyzedRepos, &analyses[i])
		}
	}
	if len(analyses) > 0 {
		first := analyses[0]
		if first.analyzed {
			repoInfo = map[string]any{"id": first.id, "name": first.name, "analyzed": true}
		} else {
			repoInfo = map[string]any{"id": first.id, "name": first.name, "analyzed": false, "reason": first.reason}
		}
	}
	homeDir := clarifyCLIHome(responsible.ID)
	sessionID := strings.TrimSpace(request.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(issue.Clarification.SessionID)
	}
	resume := sessionID != ""
	if resume && !canResumeClarifySession(homeDir, sessionID, issue.Clarification.SessionUserID, responsible.ID) {
		emit("thought", map[string]string{"kind": "status", "text": "分析环境已变化，正在结合已有回答重新分析…"})
		sessionID = ""
	}
	if sessionID == "" {
		sessionID = newAssistantSessionID()
		resume = false
	}
	if err := ensureClarifySessionDirs(responsible.ID); err != nil {
		return nil, "", nil, err
	}
	wsKey := strings.TrimSpace(issue.ID)
	if wsKey == "" {
		wsKey = "draft-" + sessionID
	}
	if len(analyzedRepos) == 1 {
		workDir = analyzedRepos[0].dir
	} else if len(analyzedRepos) > 1 {
		// Multi-repository clarify: copy every analyzed repository into a
		// durable workspace as a real directory. CodeBuddy Read/Grep/Glob do
		// not follow out-of-tree symlinks, so linking the cache is not enough.
		// Resetting leftover links never deletes the caches.
		ws, err := ensureClarifyWorkspace(responsible.ID, wsKey)
		if err != nil {
			workDir = homeDir
		} else {
			resetClarifyWorkspaceLinks(ws)
			usedNames := map[string]bool{}
			refreshMirrors := len(request.History) == 0
			for i := range analyzedRepos {
				a := analyzedRepos[i]
				a.dirName = domain.NormalizeRepositoryDirName(domain.RepositoryContext{Name: a.name, RepositoryID: a.id})
				for n := 2; usedNames[a.dirName]; n++ {
					a.dirName = fmt.Sprintf("%s-%d", a.dirName, n)
				}
				usedNames[a.dirName] = true
				dest := filepath.Join(ws, a.dirName)
				if err := materializeClarifyRepo(a.dir, dest, refreshMirrors); err != nil {
					a.analyzed = false
					a.reason = "仓库目录准备失败"
					a.dirName = ""
				}
			}
			workDir = ws
		}
	}
	// 拉不到代码时绝不能让 AI 落在 /tmp 里读到无关残留文件并"假装分析过代码"：
	// 1) 换成隔离的空工作目录，物理上杜绝读到垃圾；
	// 2) 在提示词里明确告知无代码可用，禁止声称已读代码，结论需注明未基于代码分析。
	if len(analyzedRepos) == 0 {
		if dir, mkErr := ensureClarifyWorkspace(responsible.ID, wsKey+"-empty"); mkErr == nil {
			workDir = dir
		} else {
			workDir = homeDir
		}
		reason, _ := repoInfo["reason"].(string)
		prompt.WriteString("\n\n## 重要限制\n本次未能读取代码仓库（原因：" + reason + "）。不要使用 Read/Grep/Glob，也不要假设或声称已阅读任何代码；请仅基于需求描述与对话历史进行澄清，并在 summary 开头注明「未基于代码分析」。")
	}
	// Multi-repository analysis: every selected repository is checked out as a
	// real sibling directory in the analysis workspace; the prompt lists each
	// directory plus the repos that could not be prepared.
	if len(analyzedRepos) > 1 {
		var b strings.Builder
		b.WriteString("\n\n## 多仓库分析\n本次需求涉及多个仓库，均已检出供分析（每个仓库一个独立目录，工作目录为其上级）：")
		for _, a := range analyzedRepos {
			if a.dirName != "" {
				fmt.Fprintf(&b, "\n- %s: 目录 ./%s", a.name, a.dirName)
			} else {
				fmt.Fprintf(&b, "\n- %s: 目录 %s", a.name, a.dir)
			}
		}
		b.WriteString("\n澄清问题与 summary 需覆盖以上所有仓库的协同改动，分别说明每个仓库各自需要什么。")
		for _, a := range analyses {
			if a.analyzed && a.dirName != "" {
				continue
			}
			name := a.name
			if name == "" {
				name = a.id
			}
			fmt.Fprintf(&b, "\n- %s 未能检出（原因：%s），请勿声称已分析该仓库代码", name, a.reason)
		}
		prompt.WriteString(b.String())
	} else if len(analyzedRepos) == 1 && len(analyses) > 1 {
		a := analyzedRepos[0]
		prompt.WriteString(fmt.Sprintf("\n\n## 多仓库范围\n本次需求还涉及其他仓库，但仅 %s 成功检出（当前工作目录），其余未能分析，请勿声称已阅读其代码。澄清问题与 summary 需说明多仓库协同的范围。", a.name))
	}
	if mcpHint := h.clarifyMCPInstructionsFor(issue, responsible, len(request.History) > 0, extras...); mcpHint != "" {
		prompt.WriteString(mcpHint)
	}
	userMsg := prompt.String()
	clarifyID := fmt.Sprintf("clarify-%d", time.Now().UnixNano())
	modelID := clarifyAllowedModel(request.Model)
	credentials := runtime.RunCredentials{
		CodeBuddy: runtime.CodeBuddyCredential{APIKey: strings.TrimSpace(responsible.CodeBuddyAPIKey), InternetEnvironment: strings.TrimSpace(responsible.CodeBuddyInternetEnvironment)},
		GitToken:  strings.TrimSpace(responsible.GitToken),
	}
	turnPrompt := userMsg
	if resume {
		turnPrompt = clarifyResumeTurnPrompt(request.Message)
	}
	sessionContext := userMsg
	h.rememberClarifySession(issue.ID, sessionID, responsible.ID)
	if repoInfo == nil {
		repoInfo = map[string]any{}
	}
	repoInfo["sessionId"] = sessionID
	emit("open", map[string]any{"repository": repoInfo, "model": modelID, "sessionId": sessionID})
	emit("thought", map[string]string{"kind": "status", "text": "正在分析需求…"})
	turnOpts := clarifyTurnOpts{Native: true, MCPConfig: toolFiles.MCPConfig, Settings: toolFiles.Settings}
	jsonRetries := 0
	var recoveredParsed any
	var recoveredRaw string
	for follow := 0; ; follow++ {
		_, turnRaw, captured, err := h.runClarifyTurn(modelCtx, workDir, cbPath, modelID, turnPrompt, clarifyID, sessionID, homeDir, resume, credentials, emit, turnOpts)
		if captured != "" {
			sessionID = captured
			repoInfo["sessionId"] = sessionID
			h.rememberClarifySession(issue.ID, sessionID, responsible.ID)
		}
		if err != nil && resume {
			// Never reuse the missing/incompatible session ID when starting over.
			sessionID = newAssistantSessionID()
			fallbackPrompt := sessionContext
			if turnPrompt != userMsg && !strings.Contains(sessionContext, turnPrompt) {
				fallbackPrompt = sessionContext + "\n" + turnPrompt
			}
			_, turnRaw, captured, err = h.runClarifyTurn(modelCtx, workDir, cbPath, modelID, fallbackPrompt, clarifyID, sessionID, homeDir, false, credentials, emit, turnOpts)
			resume = false
			if captured != "" {
				sessionID = captured
				repoInfo["sessionId"] = sessionID
				h.rememberClarifySession(issue.ID, sessionID, responsible.ID)
			}
		}
		if parsed, raw, ok := takeNativeClarification(toolSession, turnRaw); ok {
			return parsed, raw, repoInfo, nil
		}
		if cand, candRaw, ok := recoverNativeClarification(turnRaw); ok && recoveredParsed == nil {
			recoveredParsed, recoveredRaw = cand, candRaw
		}
		if nativeShouldRetryTerminal(modelCtx, err) && jsonRetries < nativeTerminalMaxRetries {
			jsonRetries++
			if toolSession != nil {
				toolSession.SetTerminalOnly()
			}
			turnOpts.SubmitOnly = true
			emit("thought", map[string]string{"kind": "status", "text": "正在提交澄清结论…"})
			turnPrompt = clarifyNativeRetryPrompt()
			resume = true
			continue
		}
		if recoveredParsed != nil {
			emit("thought", map[string]string{"kind": "status", "text": "已生成本轮结论…"})
			return recoveredParsed, recoveredRaw, repoInfo, nil
		}
		if err != nil {
			return nil, "", repoInfo, err
		}
		logAgentProtocolFailure(AgentToolClarify, modelID, "missing_terminal", fmt.Errorf("submit_clarification not called"))
		return nil, "", repoInfo, fmt.Errorf("本轮未生成可回答的澄清问题，请重新分析或补充说明后再试")
	}
}

func (h *streamHandlers) rememberClarifySession(issueID, sessionID string, userID int64) {
	if h == nil || h.repo == nil {
		return
	}
	issueID = strings.TrimSpace(issueID)
	sessionID = strings.TrimSpace(sessionID)
	if issueID == "" || sessionID == "" {
		return
	}
	_, _ = h.repo.PatchIssue(issueID, func(issue *domain.Issue) error {
		issue.Clarification.SessionID = sessionID
		issue.Clarification.SessionUserID = userID
		return nil
	})
}

func (h *streamHandlers) runClarifyTurn(ctx context.Context, workDir, cbPath, model, prompt, runID, sessionID, homeDir string, resume bool, credentials runtime.RunCredentials, emit func(event string, payload any), opts clarifyTurnOpts) (any, string, string, error) {
	command, err := prepareClarifyCommandWith(clarifyCommandOpts{
		Bin: cbPath, Model: model, Prompt: prompt, RunID: runID, SessionID: sessionID, Resume: resume,
		Native: opts.Native, MCPConfig: opts.MCPConfig, Settings: opts.Settings, SubmitOnly: opts.SubmitOnly,
	})
	if err != nil {
		return nil, "", sessionID, err
	}
	if strings.TrimSpace(homeDir) != "" {
		command = command.WithPrivateHomeDir(homeDir)
	}
	captured := sessionID
	sink := func(ev domain.TelemetryEvent) {
		if ev.Name != "process.stdout" {
			return
		}
		line := strings.TrimSpace(ev.Message)
		if line == "" || line[0] != '{' {
			return
		}
		if sid := captureClarifySessionID(line); sid != "" {
			captured = sid
		}
		for _, ui := range clarifyStreamEvents(line) {
			emit("thought", ui)
		}
	}
	output, err := runtime.ExecuteLocalCommand(ctx, workDir, command, credentials, sink)
	if err != nil {
		return nil, "", captured, err
	}
	if err := clarifySessionOutputError(output); err != nil {
		return nil, output, captured, err
	}
	return nil, strings.TrimSpace(output), captured, nil
}

func takeNativeClarification(session *AgentToolSession, turnRaw string) (any, string, bool) {
	if session == nil {
		return nil, turnRaw, false
	}
	term, ok := session.takeFinal()
	if !ok || term.Clarification == nil || term.Clarification.raw == nil {
		return nil, turnRaw, false
	}
	return term.Clarification.raw, turnRaw, true
}

func recoverNativeClarification(raw string) (any, string, bool) {
	parsed, extracted := extractClarifyResultJSON(strings.TrimSpace(raw))
	model := parseClarifyModel(parsed, extracted)
	if err := model.userFacingOK(); err != nil || model.raw == nil {
		return nil, raw, false
	}
	return model.raw, extracted, true
}

type clarifyTurnOpts struct {
	Native              bool
	MCPConfig, Settings string
	SubmitOnly          bool
}

// clarifyStreamEvents decodes a single stream-json line into zero or more
// UI-friendly thought events. It surfaces the agent's natural-language
// "thinking" and its tool invocations (Read / Grep / Glob) so the user can see
// what the analyst is doing. Anything else (raw tool results, final result,
// system bookkeeping) is ignored here.
func clarifyStreamEvents(line string) []map[string]string {
	var evt struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type     string          `json:"type"`
				Thinking string          `json:"thinking"`
				Text     string          `json:"text"`
				Name     string          `json:"name"`
				Input    json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &evt) != nil {
		return nil
	}
	if evt.Type != "assistant" {
		return nil
	}
	var out []map[string]string
	for _, c := range evt.Message.Content {
		switch c.Type {
		case "thinking":
			if t := strings.TrimSpace(c.Thinking); t != "" && !clarifyThoughtShouldHide(t) {
				out = append(out, map[string]string{"kind": "thinking", "text": t})
			}
		case "text":
			if t := strings.TrimSpace(c.Text); t != "" && !clarifyThoughtShouldHide(t) {
				out = append(out, map[string]string{"kind": "thinking", "text": t})
			}
		case "tool_use":
			out = append(out, map[string]string{"kind": "tool", "tool": c.Name, "detail": clarifyToolDetail(c.Name, c.Input)})
		}
	}
	return out
}

func clarifyThoughtShouldHide(s string) bool {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if s[0] == '{' || s[0] == '[' {
		return true
	}
	if strings.Contains(s, `"questions"`) && (strings.Contains(s, `"summary"`) || strings.Contains(s, `"mcpCalls"`)) {
		return true
	}
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return false
	}
	var m map[string]any
	if json.Unmarshal([]byte(s[start:end+1]), &m) != nil {
		return false
	}
	_, hasSummary := m["summary"]
	_, hasQuestions := m["questions"]
	_, hasCalls := m["mcpCalls"]
	_, hasTitle := m["suggestedTitle"]
	return hasCalls || hasTitle || (hasSummary && hasQuestions)
}

func extractClarifyProtocolObject(s string) map[string]any {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(s[start:end+1]), &m) != nil || m == nil {
		return nil
	}
	_, hasSummary := m["summary"]
	_, hasQuestions := m["questions"]
	_, hasCalls := m["mcpCalls"]
	if hasCalls || (hasSummary && hasQuestions) {
		return m
	}
	return nil
}

// clarifyToolDetail renders a short, human-readable description of a tool call
// (e.g. which file is read, which pattern is searched) for display.
func clarifyToolDetail(name string, input json.RawMessage) string {
	var m map[string]any
	if len(input) == 0 || json.Unmarshal(input, &m) != nil {
		return ""
	}
	str := func(k string) string {
		if v, ok := m[k].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	if p := str("file_path"); p != "" {
		return filepath.Base(p)
	}
	if p := str("pattern"); p != "" {
		if path := str("path"); path != "" {
			return p + " @ " + filepath.Base(path)
		}
		return p
	}
	if q := str("query"); q != "" {
		return q
	}
	return ""
}

func (h *streamHandlers) chatClarifyCancel(c *gin.Context) {
	var body struct {
		JobID string `json:"jobId"`
	}
	_ = decodeOptional(c, &body)
	jobID := strings.TrimSpace(body.JobID)
	if jobID == "" {
		jobID = strings.TrimSpace(c.Query("jobId"))
	}
	if jobID != "" {
		cancelled := h.cancelClarifyJob(composerJobKey(jobID))
		c.JSON(200, map[string]bool{"cancelled": cancelled})
		return
	}
	if authRequired() && !h.isPlatformAdmin(c.Request) {
		c.JSON(200, map[string]bool{"cancelled": false})
		return
	}
	h.clarifyMu.Lock()
	for id, cancel := range h.clarifyCancels {
		cancel()
		delete(h.clarifyCancels, id)
	}
	h.clarifyMu.Unlock()
	if h.jobs != nil {
		h.jobs.mu.Lock()
		ids := make([]string, 0)
		for id := range h.jobs.jobs {
			if strings.HasPrefix(id, composerJobPrefix) {
				ids = append(ids, id)
			}
		}
		h.jobs.mu.Unlock()
		for _, id := range ids {
			h.cancelClarifyJob(id)
		}
	}
	c.JSON(200, map[string]bool{"cancelled": true})
}

func extractClarifyResultJSON(raw string) (any, string) {
	raw = strings.TrimSpace(raw)
	try := func(s string) (any, bool) {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
		start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
		if start < 0 || end <= start {
			return nil, false
		}
		s = s[start : end+1]
		var v any
		if json.Unmarshal([]byte(s), &v) != nil {
			return nil, false
		}
		return unwrapStreamJSONEnvelope(v)
	}
	protocol := func(s string) (any, bool) {
		v, ok := try(s)
		if !ok || !isClarifyProtocolObject(v) {
			return nil, false
		}
		return v, true
	}

	var resultVal any
	var resultRaw string
	var fallback any
	var fallbackRaw string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		switch fmt.Sprint(event["type"]) {
		case "assistant", "user":
			if msg, ok := event["message"].(map[string]any); ok {
				if v, src, ok := extractClarifyJSONFromEvent(msg, protocol); ok {
					fallback, fallbackRaw = v, src
				}
			}
		case "result":
			if msg, ok := event["result"].(string); ok {
				if v, ok := protocol(msg); ok {
					resultVal, resultRaw = v, msg
				}
			} else if obj, ok := event["result"].(map[string]any); ok {
				if v, ok := unwrapStreamJSONEnvelope(obj); ok && isClarifyProtocolObject(v) {
					resultVal, resultRaw = v, line
				}
			}
			if resultVal == nil {
				if v, src, ok := extractClarifyJSONFromEvent(event, protocol); ok {
					resultVal, resultRaw = v, src
				}
			}
		}
	}
	if resultVal != nil {
		return resultVal, resultRaw
	}
	if fallback != nil {
		return fallback, fallbackRaw
	}
	if v, ok := protocol(raw); ok {
		return v, raw
	}
	return nil, ""
}

func extractJSONObject(raw string) (any, string) {
	raw = strings.TrimSpace(raw)
	try := func(s string) (any, bool) {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
		start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
		if start < 0 || end <= start {
			return nil, false
		}
		s = s[start : end+1]
		var v any
		if json.Unmarshal([]byte(s), &v) != nil {
			return nil, false
		}
		return unwrapStreamJSONEnvelope(v)
	}

	var resultVal any
	var resultRaw string
	var fallback any
	var fallbackRaw string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if fmt.Sprint(event["type"]) == "result" {
			if msg, ok := event["result"].(string); ok {
				if v, ok := try(msg); ok {
					resultVal, resultRaw = v, msg
				} else if strings.TrimSpace(msg) != "" {
					resultVal = map[string]any{"summary": msg, "questions": []any{}, "ready": false}
					resultRaw = msg
				}
			} else if obj, ok := event["result"].(map[string]any); ok {
				if v, ok := unwrapStreamJSONEnvelope(obj); ok {
					resultVal, resultRaw = v, line
				}
			}
			continue
		}
		if msg, ok := event["message"].(map[string]any); ok {
			if v, src, ok := extractClarifyJSONFromEvent(msg, try); ok {
				fallback, fallbackRaw = v, src
			}
		}
		if v, src, ok := extractClarifyJSONFromEvent(event, try); ok {
			fallback, fallbackRaw = v, src
		}
	}
	if resultVal != nil {
		return resultVal, resultRaw
	}
	if v, ok := try(raw); ok {
		return v, raw
	}
	if fallback != nil {
		return fallback, fallbackRaw
	}
	return map[string]any{"summary": raw, "questions": []any{}, "ready": false}, raw
}

func unwrapStreamJSONEnvelope(v any) (any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return v, true
	}
	switch fmt.Sprint(m["type"]) {
	case "result":
		switch result := m["result"].(type) {
		case string:
			start, end := strings.Index(result, "{"), strings.LastIndex(result, "}")
			if start >= 0 && end > start {
				var inner any
				if json.Unmarshal([]byte(result[start:end+1]), &inner) == nil {
					return unwrapStreamJSONEnvelope(inner)
				}
			}
		case map[string]any:
			return unwrapStreamJSONEnvelope(result)
		}
	case "assistant", "user":
		if msg, ok := m["message"].(map[string]any); ok {
			if v, _, ok := extractClarifyJSONFromEvent(msg, func(s string) (any, bool) {
				start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
				if start < 0 || end <= start {
					return nil, false
				}
				var inner any
				if json.Unmarshal([]byte(s[start:end+1]), &inner) != nil {
					return nil, false
				}
				return inner, true
			}); ok {
				return v, true
			}
		}
	}
	return m, true
}

func extractClarifyJSONFromEvent(event map[string]any, try func(string) (any, bool)) (any, string, bool) {
	if contents, ok := event["content"].([]any); ok {
		for i := len(contents) - 1; i >= 0; i-- {
			block, _ := contents[i].(map[string]any)
			if block == nil {
				continue
			}
			if text, ok := block["text"].(string); ok {
				if v, ok := try(text); ok {
					return v, text, true
				}
			}
			if text, ok := block["content"].(string); ok {
				if v, ok := try(text); ok {
					return v, text, true
				}
			}
		}
	}
	for _, key := range []string{"result", "text", "content"} {
		if msg, ok := event[key].(string); ok {
			if v, ok := try(msg); ok {
				return v, msg, true
			}
		}
	}
	return nil, "", false
}
