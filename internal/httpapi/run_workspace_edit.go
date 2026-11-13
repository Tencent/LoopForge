package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
	"time"
)

func (h *runsHandlers) workspaceEditActor(c *gin.Context) (domain.Run, int64, bool) {
	run, ok := h.repo.RunState(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "运行不存在"})
		return run, 0, false
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return run, 0, false
	}
	// Conversation tools inherit project access from their authenticated entry
	// point; the separate task-panel editor policy does not apply to them.
	if mainBotWorkspaceRun(c.Request.Context()) != run.ID {
		project, ok := h.repo.Project(run.ExecutionContext.ProjectID)
		allowed := h.isPlatformAdmin(c.Request) || ok && (project.OwnerID == user.ID || project.OwnerID == 0 && len(project.Members) == 0)
		for _, member := range project.Members {
			if member.UserID == user.ID && !strings.EqualFold(member.Role, "viewer") {
				allowed = true
			}
		}
		if !allowed {
			c.JSON(403, gin.H{"error": "无权编辑该任务工作区"})
			return run, 0, false
		}
	}
	if engine.WorkspaceRunID(run) != run.ID {
		c.JSON(409, gin.H{"error": "子 SOP 共享父任务工作区，请从父任务操作"})
		return run, 0, false
	}
	for _, node := range run.Nodes {
		if node != nil && node.ChildRunID != "" {
			c.JSON(409, gin.H{"error": "请进入子 SOP 运行后再编辑对应工作区"})
			return run, 0, false
		}
	}
	return run, user.ID, true
}
func (h *runsHandlers) workspaceTakeover(c *gin.Context) {
	run, user, ok := h.workspaceEditActor(c)
	if !ok {
		return
	}
	// Kept for older clients. People edit at any time, so this pauses nothing.
	if err := h.repo.AcquireWorkspaceEditor(run.ID, user); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"editorId": user, "expiresAt": time.Now().UTC().Add(15 * time.Minute)})
}
func (h *runsHandlers) workspaceRelease(c *gin.Context) {
	run, user, ok := h.workspaceEditActor(c)
	if !ok {
		return
	}
	if err := h.repo.ReleaseWorkspaceEditor(run.ID, user, func(locked domain.Run) error { return h.closeWorkspaceTerminals(c.Request.Context(), locked, user) }); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (h *runsHandlers) workspaceWrite(c *gin.Context) {
	run, user, ok := h.workspaceEditActor(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	var req struct {
		Path        string `json:"path"`
		Content     string `json:"content"`
		ExpectedSHA string `json:"expectedSha256"`
	}
	if !decode(c, &req) {
		return
	}
	var sha string
	runnerID := h.engine.RunExecutionRunner(run)
	err := h.withWorkspaceWriter(c, run.ID, user, func(locked domain.Run) error {
		if runnerID != "" {
			if h.runnerControls == nil {
				return fmt.Errorf("执行端未连接")
			}
			ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
			defer cancel()
			response, err := h.runnerControls.requestWorkspace(ctx, runnerID, domain.RunnerWorkspaceRequest{RunID: runnerWorkspaceRunID(locked), RunStatus: locked.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, RunKind: mainBotWorkspaceMode(c, locked), ConcurrentEdit: humanWorkspaceEdit(c, locked), NodeCheckpointsEnabled: locked.ExecutionContext.NodeCheckpointsEnabled, Operation: domain.RunnerWorkspaceOperationWrite, Path: req.Path, Content: req.Content, ExpectedSHA256: req.ExpectedSHA, TerminalOwner: user})
			if err != nil {
				return fmt.Errorf("执行端写入失败：%w", err)
			}
			if response.Error != "" {
				return fmt.Errorf("%s", response.Error)
			}
			var result struct {
				SHA string `json:"sha256"`
			}
			if err = json.Unmarshal(response.Payload, &result); err != nil {
				return err
			}
			sha = result.SHA
			return nil
		}
		root, reason := h.engine.LocalRunWorkspace(locked)
		if reason != "" {
			return fmt.Errorf("%s", reason)
		}
		var err error
		sha, err = runtime.WriteWorkspaceFile(root, req.Path, req.Content, req.ExpectedSHA)
		return err
	})
	if err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	if h.engine != nil {
		h.engine.AppendRunEvent(run.ID, "workspace.changed", "工作区文件已修改，之前的检查仅供历史参考")
	}
	c.JSON(200, gin.H{"sha256": sha})
}

func (h *runsHandlers) workspaceWritable(c *gin.Context, run domain.Run) bool {
	// Read-only probe: never write HTTP errors here (browse handlers still return the payload).
	if engine.WorkspaceRunID(run) != run.ID {
		return false
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		return false
	}
	project, pok := h.repo.Project(run.ExecutionContext.ProjectID)
	allowed := mainBotWorkspaceRun(c.Request.Context()) == run.ID || h.isPlatformAdmin(c.Request) || pok && (project.OwnerID == user.ID || project.OwnerID == 0 && len(project.Members) == 0)
	for _, member := range project.Members {
		if member.UserID == user.ID && !strings.EqualFold(member.Role, "viewer") {
			allowed = true
		}
	}
	if mainBotWorkspaceRun(c.Request.Context()) == run.ID {
		return run.LiveWorkspace() || (run.Status == "waiting" && run.PauseRequested && !run.CancelRequested) || domain.TerminalRunStatus(run.Status)
	}
	return allowed && run.HumanEditable()
}

func (h *runsHandlers) workspaceTerminal(c *gin.Context) {
	run, user, ok := h.workspaceEditActor(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	var req domain.WorkspaceTerminalRequest
	if !decode(c, &req) {
		return
	}
	runnerID := h.engine.RunExecutionRunner(run)
	remoteHost := h.engine.WorkspaceRemoteHost(run)
	var trustedRequest domain.RunnerWorkspaceRequest
	if runnerID != "" || remoteHost {
		var err error
		trustedRequest, err = h.engine.TrustedWorkspaceRequest(run.ID, domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationTerminal, Terminal: &req, TerminalOwner: user})
		if err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
	}
	var result any
	// Checkpoint-enabled runs must fence the entire lifetime of a local PTY,
	// not just its open request. The lease is acquired by withPreparedWorkspace
	// and transferred to a renewer after a successful open.
	var terminalLease string
	terminalLeaseTransferred := false
	if req.Operation == "open" && run.ExecutionContext.NodeCheckpointsEnabled && runnerID == "" {
		var leaseErr error
		terminalLease, leaseErr = h.engine.BeginConversationWorkspaceUse(run.ID)
		if leaseErr != nil {
			c.JSON(409, gin.H{"error": leaseErr.Error(), "code": "workspace_busy"})
			return
		}
		defer func() {
			if !terminalLeaseTransferred {
				_ = h.repo.FinishWorkspaceUse(run.ID, terminalLease, time.Now().UTC(), nil, "")
			}
		}()
		c.Request = c.Request.WithContext(engine.WithConversationWorkspaceToken(c.Request.Context(), terminalLease, run.ID))
		trustedRequest.CheckpointLease = terminalLease
	}
	launch := func() error {
		operate := func(locked domain.Run) error {
			req.ExpiresAt = locked.WorkspaceEditUntil
			trustedRequest.RunID = runnerWorkspaceRunID(locked)
			trustedRequest.RunStatus = locked.Status
			trustedRequest.WorkspaceLayout = locked.ExecutionContext.RunnerWorkspaceLayout
			trustedRequest.RunKind = mainBotWorkspaceMode(c, locked)
			trustedRequest.ConcurrentEdit = humanWorkspaceEdit(c, locked)
			trustedRequest.NodeCheckpointsEnabled = locked.ExecutionContext.NodeCheckpointsEnabled
			if runnerID != "" {
				if h.runnerControls == nil {
					return fmt.Errorf("执行端未连接")
				}
				ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
				defer cancel()

				response, err := h.runnerControls.requestWorkspace(ctx, runnerID, trustedRequest)
				if err != nil {
					return err
				}
				if response.Error != "" {
					// The runner refuses a busy open before creating a PTY, so a
					// caller may retry it like any other workspace_busy answer.
					if req.Operation == "open" && response.Code == domain.WorkspaceErrorBusy {
						return fmt.Errorf("%w（%s）", repo.ErrWorkspaceInUse, response.Error)
					}
					return fmt.Errorf("%s", response.Error)
				}
				result = json.RawMessage(response.Payload)
				return nil
			}
			if remoteHost {
				response, err := h.engine.ForwardPreparedWorkspaceRequest(c.Request.Context(), locked, trustedRequest)
				if err != nil {
					return err
				}
				if response.Error != "" {
					return fmt.Errorf("%s", response.Error)
				}
				result = json.RawMessage(response.Payload)
				return nil
			}
			r, err := h.engine.HandlePreparedWorkspaceTerminal(c.Request.Context(), run, &h.terminals, fmt.Sprintf("%s:%d", run.ID, user), req, user)
			result = r
			return err
		}
		if req.Operation != "open" && run.ExecutionContext.NodeCheckpointsEnabled {
			// An open terminal already owns the workspace fence. Its session is
			// scoped to this user by the terminal pool; taking a second workspace
			// lease here would prevent reading or closing that same terminal.
			var locked domain.Run
			check := func(current domain.Run) error { locked = current; return nil }
			var err error
			if mainBotWorkspaceRun(c.Request.Context()) == run.ID {
				err = h.repo.WithMainBotWorkspace(run.ID, user, check)
			} else {
				err = h.repo.WithWorkspaceEditor(run.ID, user, check)
			}
			if err != nil {
				return err
			}
			return operate(locked)
		}
		return h.withWorkspaceWriter(c, run.ID, user, operate)
	}
	var err error
	if req.Operation == "open" {
		err = h.withPreparedWorkspace(c, run.ID, func(runtime.WorkspacePreparationStatus) error { return launch() })
	} else {
		err = launch()
	}
	if err != nil {
		body := gin.H{"error": err.Error()}
		if errors.Is(err, repo.ErrWorkspaceInUse) {
			// The lease was refused before touching the PTY. Retrying this
			// specific response cannot duplicate input or create a second shell.
			body["code"] = "workspace_busy"
		}
		c.JSON(409, body)
		return
	}
	if terminalLease != "" && runnerID == "" && !remoteHost {
		terminalLeaseTransferred = true
		go h.holdCheckpointTerminalLease(run.ID, terminalLease)
	} else if terminalLease != "" {
		terminalLeaseTransferred = true // The workspace owner renews and releases it.
	}
	if h.engine != nil && req.Operation == "input" && req.Data != "" {
		h.engine.AppendRunEvent(run.ID, "workspace.changed", "工作区终端收到输入，之前的检查仅供历史参考")
	}
	c.JSON(200, result)
}

func (h *runsHandlers) holdCheckpointTerminalLease(runID, token string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer h.repo.FinishWorkspaceUse(runID, token, time.Now().UTC(), nil, "")
	for range ticker.C {
		if !h.terminals.HasRunScope(runID) {
			return
		}
		if err := h.repo.RenewWorkspaceUse(runID, token, time.Now().UTC()); err != nil {
			h.terminals.CloseRunScope(runID)
			return
		}
	}
}
func (h *runsHandlers) closeWorkspaceTerminals(ctx context.Context, run domain.Run, user int64) error {
	h.terminals.CloseScope(fmt.Sprintf("%s:%d", run.ID, user))
	if runnerID := h.engine.RunExecutionRunner(run); runnerID != "" {
		if h.runnerControls == nil {
			return fmt.Errorf("执行端未连接，暂不能结束编辑")
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req := domain.WorkspaceTerminalRequest{Operation: "close-all"}
		response, err := h.runnerControls.requestWorkspace(ctx, runnerID, domain.RunnerWorkspaceRequest{RunID: runnerWorkspaceRunID(run), RunStatus: run.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationTerminal, Terminal: &req, TerminalOwner: user})
		if err != nil {
			return err
		}
		if response.Error != "" {
			return fmt.Errorf("%s", response.Error)
		}
	}
	return nil
}

func (h *runsHandlers) withWorkspaceWriter(c *gin.Context, runID string, user int64, fn func(domain.Run) error) error {
	ctx := c.Request.Context()
	token := engine.ConversationWorkspaceToken(ctx, runID)
	if h.engine != nil && token == "" {
		var err error
		token, err = h.engine.BeginConversationWorkspaceUse(runID)
		if err != nil {
			return err
		}
		defer func() {
			saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer stop()
			_ = h.engine.EndConversationWorkspaceUse(saveCtx, runID, token)
		}()
	}
	if h.engine != nil {
		if err := h.engine.EnsureConversationWorkspace(ctx, runID, token); err != nil {
			return err
		}
	}
	// A remote worker validates the same durable workspace lease before
	// opening a checkpoint-protected terminal. Do not hold the run-row
	// transaction over that RPC: SQLite may have only one DB connection and
	// the remote validation would otherwise wait for its own caller forever.
	if run, ok := h.repo.RunState(runID); ok && run.ExecutionContext.NodeCheckpointsEnabled && token != "" && h.engine.WorkspaceRemoteHost(run) && h.engine.RunExecutionRunner(run) == "" {
		var locked domain.Run
		check := func(current domain.Run) error { locked = current; return nil }
		var err error
		if mainBotWorkspaceRun(ctx) == runID {
			err = h.repo.WithMainBotWorkspace(runID, user, check)
		} else {
			err = h.repo.WithWorkspaceEditor(runID, user, check)
		}
		if err != nil {
			return err
		}
		if err = h.repo.ValidateWorkspaceUse(runID, token, time.Now().UTC()); err != nil {
			return err
		}
		return fn(locked)
	}
	if mainBotWorkspaceRun(ctx) == runID {
		return h.repo.WithMainBotWorkspace(runID, user, fn)
	}
	return h.repo.WithWorkspaceEditor(runID, user, fn)
}

// humanWorkspaceEdit marks IDE requests on runs that may still have agents
// running, which the runner serves beside those tasks (sharing their gate)
// instead of waiting for their fence. Finished runs keep the exclusive fence.
func humanWorkspaceEdit(c *gin.Context, run domain.Run) bool {
	return mainBotWorkspaceRun(c.Request.Context()) != run.ID && run.HumanEditable() && !domain.TerminalRunStatus(run.Status)
}

// mainBotWorkspaceMode tells the runner to apply the live-workspace rule to
// Main Bot (no SOP fence on its conversation workspace).
func mainBotWorkspaceMode(c *gin.Context, run domain.Run) string {
	if mainBotWorkspaceRun(c.Request.Context()) == run.ID {
		return run.Kind
	}
	return ""
}
