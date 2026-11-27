package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/workspacessh"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type workspaceSSHRelay struct {
	mu      sync.Mutex
	pending map[string]*workspaceSSHAttachment
	active  map[string]int
	slots   chan struct{}
}

type workspaceSSHAttachment struct {
	runnerID string
	stream   chan *workspacessh.Socket
	done     chan struct{}
}

func newWorkspaceSSHRelay() *workspaceSSHRelay {
	return &workspaceSSHRelay{
		pending: make(map[string]*workspaceSSHAttachment),
		active:  make(map[string]int),
		slots:   make(chan struct{}, 64),
	}
}

func (r *workspaceSSHRelay) hasRun(runID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[runID] > 0
}

func (r *workspaceSSHRelay) trackRun(runID string) func() {
	r.mu.Lock()
	r.active[runID]++
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		r.active[runID]--
		if r.active[runID] == 0 {
			delete(r.active, runID)
		}
		r.mu.Unlock()
	}
}

func (r *workspaceSSHRelay) claim(ticket, runnerID string) *workspaceSSHAttachment {
	r.mu.Lock()
	defer r.mu.Unlock()
	attachment := r.pending[ticket]
	if attachment == nil || attachment.runnerID != runnerID {
		return nil
	}
	delete(r.pending, ticket)
	return attachment
}

func workspaceSSHSecret() string {
	if raw := os.Getenv("AGENTFLOW_WORKSPACE_SSH_ENABLED"); raw == "1" || strings.EqualFold(raw, "true") {
		return strings.TrimSpace(os.Getenv("AGENTFLOW_SESSION_SECRET"))
	}
	return ""
}

func (h *runsHandlers) workspaceSSHAvailable(run domain.Run) bool {
	if workspaceSSHSecret() == "" || h.workspaceSSH == nil || h.engine == nil || !run.HumanEditable() || !h.engine.WorkspaceSupportsSSH(run) {
		return false
	}
	return h.engine.RunExecutionRunner(run) == "" ||
		h.runnerControls != nil && run.ExecutionContext.RunnerWorkspaceLayout == domain.RunnerWorkspaceLayoutShared
}

func (h *runsHandlers) workspaceSSHActor(c *gin.Context) (domain.Run, int64, bool) {
	if workspaceSSHSecret() == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "桌面 IDE 连接未启用"})
		return domain.Run{}, 0, false
	}
	// The desktop companion uses a personal token. A web page with only
	// a browser session cannot open a raw desktop SSH socket.
	if _, bearer := bearerToken(c.Request); !bearer {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "桌面 IDE 连接需要个人 API Token"})
		return domain.Run{}, 0, false
	}
	run, user, ok := h.workspaceEditActor(c)
	if ok && !h.workspaceSSHAvailable(run) {
		c.JSON(http.StatusConflict, gin.H{"error": "当前任务工作区不支持桌面 IDE 连接"})
		ok = false
	}
	return run, user, ok
}

func (h *runsHandlers) workspaceSSHHostKey(c *gin.Context) {
	run, _, ok := h.workspaceSSHActor(c)
	if !ok {
		return
	}
	key, err := workspacessh.HostPublicKey(workspacessh.HostSeed(workspaceSSHSecret(), run.ID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法生成工作区主机密钥"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"hostKey": key})
}

func (h *runsHandlers) connectWorkspaceSSH(c *gin.Context) {
	run, actor, ok := h.workspaceSSHActor(c)
	if !ok {
		return
	}
	if !websocket.IsWebSocketUpgrade(c.Request) || c.GetHeader("Origin") != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "桌面 IDE 连接需要本地客户端的 WebSocket 请求"})
		return
	}
	encodedKey := c.GetHeader(workspacessh.PublicKeyHeader)
	if len(encodedKey) > base64.RawStdEncoding.EncodedLen(4096) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SSH 公钥过大"})
		return
	}
	rawKey, err := base64.RawStdEncoding.DecodeString(encodedKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 SSH 公钥"})
		return
	}
	key, err := workspacessh.ClientPublicKey(rawKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	select {
	case h.workspaceSSH.slots <- struct{}{}:
		defer func() { <-h.workspaceSSH.slots }()
	default:
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "桌面 IDE 连接数已达上限"})
		return
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	runnerID := h.engine.RunExecutionRunner(run)
	keys := workspacessh.Keys{HostSeed: workspacessh.HostSeed(workspaceSSHSecret(), run.ID), ClientKey: key}
	var stream io.ReadWriteCloser
	var finish func()
	err = h.withPreparedWorkspace(c, run.ID, func(runtime.WorkspacePreparationStatus) error {
		// Restore/prepare may have taken long enough for permissions or the
		// workspace owner to change. Re-check before starting a shell.
		current, exists := h.repo.RunState(run.ID)
		if !exists || !h.workspaceWritable(c, current) || !h.workspaceSSHAvailable(current) || h.engine.RunExecutionRunner(current) != runnerID {
			return fmt.Errorf("当前任务工作区不可编辑")
		}
		var err error
		if runnerID == "" {
			stream, err = h.engine.OpenPreparedWorkspaceSSH(ctx, run.ID, keys)
		} else {
			stream, err = h.openRunnerWorkspaceSSH(ctx, run.ID, runnerID, keys)
		}
		if err == nil {
			finish = h.workspaceSSH.trackRun(run.ID)
		}
		return err
	})
	if err != nil {
		message := gin.H{"error": err.Error()}
		if errors.Is(err, repo.ErrWorkspaceInUse) {
			message["code"] = "workspace_busy"
		}
		c.JSON(http.StatusConflict, message)
		return
	}
	defer finish()
	defer stream.Close()
	socket, err := runnerWSUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	h.engine.AppendRunEvent(run.ID, "workspace.changed", "已打开桌面 IDE，之前的工作区检查仅供历史参考")
	go h.watchWorkspaceSSH(ctx, cancel, c.Request.Clone(context.Background()), run.ID, actor, runnerID)
	workspacessh.Relay(ctx, workspacessh.NewSocket(socket), stream)
}

func (h *runsHandlers) watchWorkspaceSSH(ctx context.Context, cancel context.CancelFunc, request *http.Request, runID string, actor int64, runnerID string) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			run, exists := h.repo.RunState(runID)
			id, status, _ := h.workspaceEditAccess(request, run)
			if !exists || status != 0 || id != actor || !h.workspaceSSHAvailable(run) || h.engine.RunExecutionRunner(run) != runnerID ||
				h.repo.TouchWorkspaceUse(runID, now.UTC()) != nil {
				cancel()
				return
			}
		}
	}
}

func (h *runsHandlers) openRunnerWorkspaceSSH(ctx context.Context, runID, runnerID string, keys workspacessh.Keys) (io.ReadWriteCloser, error) {
	ticket := rand.Text()
	attachment := &workspaceSSHAttachment{runnerID: runnerID, stream: make(chan *workspacessh.Socket), done: make(chan struct{})}
	h.workspaceSSH.mu.Lock()
	h.workspaceSSH.pending[ticket] = attachment
	h.workspaceSSH.mu.Unlock()
	defer func() {
		close(attachment.done)
		h.workspaceSSH.mu.Lock()
		delete(h.workspaceSSH.pending, ticket)
		h.workspaceSSH.mu.Unlock()
	}()

	ctx, stop := context.WithTimeout(ctx, workspacessh.HandshakeTimeout)
	defer stop()
	request, err := h.engine.TrustedWorkspaceRequest(runID, domain.RunnerWorkspaceRequest{
		Operation:  domain.RunnerWorkspaceOperationDesktopSSH,
		DesktopSSH: &domain.WorkspaceSSHRequest{Ticket: ticket, HostSeed: keys.HostSeed, ClientKey: keys.ClientKey},
	})
	if err != nil {
		return nil, err
	}
	// The Runner already prepared the environment on the normal run path.
	request.PreparationInvocation = nil
	response, err := h.runnerControls.requestWorkspace(ctx, runnerID, request)
	if err != nil {
		return nil, fmt.Errorf("执行机器未连接：%w", err)
	}
	if response.Code == domain.WorkspaceErrorBusy {
		return nil, repo.ErrWorkspaceInUse
	}
	if response.Error == "unsupported workspace operation" {
		return nil, fmt.Errorf("请将执行机器 Daemon 更新到支持桌面 IDE 的版本")
	}
	if response.Status != http.StatusOK {
		return nil, fmt.Errorf("执行机器无法打开桌面 IDE：%s", response.Error)
	}
	select {
	case stream := <-attachment.stream:
		return stream, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("执行机器建立 SSH 数据通道超时")
	}
}
