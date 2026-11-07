package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// browserHandlers serves the task panel view of a run's agent browser. The
// browser lives with the run workspace: this process, a sibling worker, or a
// CLI runner reached over its control socket.
type browserHandlers struct {
	*deps
	runnerControls *runnerControlHub

	touchMu sync.Mutex
	touched map[string]time.Time
}

var browserReadOperations = map[string]bool{"": true, "status": true, "frame": true}

// The panel polls frames for as long as it is open, so only operating the
// browser counts as workspace use; a forgotten tab must not pin the sandbox.
const browserTouchInterval = time.Minute

func (h *browserHandlers) browser(c *gin.Context) {
	run, ok := h.repo.RunState(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "运行不存在"})
		return
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
	var req domain.BrowserRequest
	if !decode(c, &req) {
		return
	}
	if !browserReadOperations[req.Operation] && !h.canControl(c.Request, run, user.ID) {
		c.JSON(403, gin.H{"error": "无权操作该任务的浏览器"})
		return
	}
	if !browserReadOperations[req.Operation] {
		h.touchWorkspace(run.ID)
	}
	payload, opErr := h.dispatch(c.Request.Context(), run, req)
	if opErr != "" {
		c.JSON(409, gin.H{"error": opErr, "state": payload})
		return
	}
	c.Data(200, "application/json; charset=utf-8", payload)
}

// dispatch reaches the browser wherever the run's workspace lives and returns
// its state JSON plus the operation error, if any. An unreachable or outdated
// executor is reported as an unavailable state rather than an error.
func (h *browserHandlers) dispatch(ctx context.Context, run domain.Run, req domain.BrowserRequest) (json.RawMessage, string) {
	timeout := 15 * time.Second
	if req.Ensure {
		// The first browser of a host may still need its sidecar image built.
		timeout = 4 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var (
		response domain.RunnerWorkspaceResponse
		err      error
	)
	switch {
	case h.engine.RunExecutionRunner(run) != "":
		if h.runnerControls == nil {
			return browserReason("执行端未连接"), ""
		}
		request, requestErr := h.engine.BrowserWorkspaceRequest(run, &req)
		if requestErr != nil {
			return browserReason(requestErr.Error()), ""
		}
		response, err = h.runnerControls.requestWorkspace(ctx, h.engine.RunExecutionRunner(run), request)
	case h.engine.WorkspaceRemoteHost(run):
		response, err = h.engine.ForwardBrowserRequest(ctx, run, &req)
	default:
		response = h.engine.HandleBrowserRequest(ctx, run, &req)
	}
	if err != nil {
		return browserReason("浏览器所在执行端不可达：" + err.Error()), ""
	}
	if len(response.Payload) == 0 {
		reason := strings.TrimSpace(response.Error)
		if reason == "" || strings.Contains(reason, "unsupported operation") || strings.Contains(reason, "not a directory") {
			reason = "执行端版本过旧，暂不支持 Agent 浏览器"
		}
		return browserReason(reason), ""
	}
	return response.Payload, response.Error
}

func browserReason(reason string) json.RawMessage {
	raw, _ := json.Marshal(domain.BrowserState{Reason: reason})
	return raw
}

func (h *browserHandlers) touchWorkspace(runID string) {
	now := time.Now().UTC()
	h.touchMu.Lock()
	if h.touched == nil {
		h.touched = map[string]time.Time{}
	}
	if now.Sub(h.touched[runID]) < browserTouchInterval {
		h.touchMu.Unlock()
		return
	}
	for id, at := range h.touched {
		if now.Sub(at) >= browserTouchInterval {
			delete(h.touched, id)
		}
	}
	h.touched[runID] = now
	h.touchMu.Unlock()
	_ = h.repo.TouchWorkspaceUse(runID, now)
}

func (h *browserHandlers) canControl(r *http.Request, run domain.Run, userID int64) bool {
	if mainBotWorkspaceRun(r.Context()) == run.ID {
		return true
	}
	if h.isPlatformAdmin(r) {
		return true
	}
	project, ok := h.repo.Project(run.ExecutionContext.ProjectID)
	if !ok {
		return false
	}
	if project.OwnerID == userID || project.OwnerID == 0 && len(project.Members) == 0 {
		return true
	}
	for _, member := range project.Members {
		if member.UserID == userID && !strings.EqualFold(member.Role, "viewer") {
			return true
		}
	}
	return false
}
