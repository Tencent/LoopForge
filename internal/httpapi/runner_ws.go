package httpapi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	runnerWSPingInterval = 30 * time.Second
	runnerWSWriteTimeout = 10 * time.Second
)

type runnerControlMessage struct {
	Type              string                          `json:"type"`
	TaskID            string                          `json:"taskId,omitempty"`
	DesiredState      string                          `json:"desiredState,omitempty"`
	ActiveTaskIDs     []string                        `json:"activeTaskIds,omitempty"`
	RequestID         string                          `json:"requestId,omitempty"`
	WorkspaceRequest  *domain.RunnerWorkspaceRequest  `json:"workspaceRequest,omitempty"`
	WorkspaceResponse *domain.RunnerWorkspaceResponse `json:"workspaceResponse,omitempty"`
}

type runnerControlHub struct {
	mu      sync.Mutex
	conns   map[string]*runnerControlConn
	pending map[string]chan domain.RunnerWorkspaceResponse
	nextID  uint64
}

type runnerControlConn struct {
	runnerID string
	ws       *websocket.Conn
	send     chan runnerControlMessage
	done     chan struct{}
	once     sync.Once
}

func newRunnerControlHub() *runnerControlHub {
	return &runnerControlHub{conns: map[string]*runnerControlConn{}, pending: map[string]chan domain.RunnerWorkspaceResponse{}}
}

var errRunnerWorkspaceUnavailable = errors.New("runner workspace unavailable")

// requestWorkspace performs a transient RPC over the runner's outbound
// control socket. Neither the request nor file contents touch the durable task
// queue; disconnecting the runner fails every in-flight request.
func (h *runnerControlHub) requestWorkspace(ctx context.Context, runnerID string, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
	h.mu.Lock()
	conn := h.conns[runnerID]
	if conn == nil {
		h.mu.Unlock()
		return domain.RunnerWorkspaceResponse{}, errRunnerWorkspaceUnavailable
	}
	h.nextID++
	requestID := fmt.Sprintf("workspace-%d", h.nextID)
	result := make(chan domain.RunnerWorkspaceResponse, 1)
	h.pending[requestID] = result
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, requestID)
		h.mu.Unlock()
	}()

	select {
	case conn.send <- runnerControlMessage{Type: "workspace.request", RequestID: requestID, WorkspaceRequest: &request}:
	case <-conn.done:
		return domain.RunnerWorkspaceResponse{}, errRunnerWorkspaceUnavailable
	case <-ctx.Done():
		return domain.RunnerWorkspaceResponse{}, ctx.Err()
	}
	select {
	case response := <-result:
		return response, nil
	case <-conn.done:
		return domain.RunnerWorkspaceResponse{}, errRunnerWorkspaceUnavailable
	case <-ctx.Done():
		return domain.RunnerWorkspaceResponse{}, ctx.Err()
	}
}

func (h *runnerControlHub) resolveWorkspace(requestID string, response domain.RunnerWorkspaceResponse) {
	h.mu.Lock()
	result := h.pending[requestID]
	h.mu.Unlock()
	if result == nil {
		return
	}
	select {
	case result <- response:
	default:
	}
}

func (h *runnerControlHub) register(runnerID string, ws *websocket.Conn) *runnerControlConn {
	c := &runnerControlConn{runnerID: runnerID, ws: ws, send: make(chan runnerControlMessage, 64), done: make(chan struct{})}
	h.mu.Lock()
	old := h.conns[runnerID]
	h.conns[runnerID] = c
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
	return c
}

func (h *runnerControlHub) unregister(c *runnerControlConn) {
	h.mu.Lock()
	if h.conns[c.runnerID] == c {
		delete(h.conns, c.runnerID)
	}
	h.mu.Unlock()
	c.close()
}

func (h *runnerControlHub) sendControl(runnerID, taskID, desiredState string) {
	if runnerID == "" || taskID == "" || desiredState == "" {
		return
	}
	h.mu.Lock()
	c := h.conns[runnerID]
	h.mu.Unlock()
	if c == nil {
		return
	}
	msg := runnerControlMessage{Type: "task.control", TaskID: taskID, DesiredState: desiredState}
	select {
	case c.send <- msg:
		return
	case <-c.done:
		return
	default:
	}
	// Buffer is full. Do not tear the socket down: that would force a
	// reconnect and push cancel onto the lease-renew path. Deliver in the
	// background so Pause/Cancel stay non-blocking.
	go func() {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case c.send <- msg:
		case <-c.done:
		case <-timer.C:
		}
	}()
}

func (c *runnerControlConn) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.ws.Close()
	})
}

func (c *runnerControlConn) writeLoop() {
	ticker := time.NewTicker(runnerWSPingInterval)
	defer ticker.Stop()
	for {
		select {
		case message := <-c.send:
			_ = c.ws.SetWriteDeadline(time.Now().Add(runnerWSWriteTimeout))
			if err := c.ws.WriteJSON(message); err != nil {
				c.close()
				return
			}
		case <-ticker.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(runnerWSWriteTimeout))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.close()
				return
			}
		case <-c.done:
			return
		}
	}
}

var runnerWSUpgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	ReadBufferSize:   1024,
	WriteBufferSize:  1024,
}

// runnerControlConnect 升级 /runners/ws 连接并进入控制消息循环。
// 路由: g.GET("/runners/ws", h.runnerControlConnect)
func (h *runnersHandlers) runnerControlConnect(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	ws, err := runnerWSUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	conn := h.runnerControls.register(runner.ID, ws)
	defer h.runnerControls.unregister(conn)
	go conn.writeLoop()
	ws.SetReadLimit(20 << 20)
	for {
		var input runnerControlMessage
		if err := ws.ReadJSON(&input); err != nil {
			return
		}
		if input.Type == "workspace.response" && input.WorkspaceResponse != nil {
			h.runnerControls.resolveWorkspace(input.RequestID, *input.WorkspaceResponse)
			continue
		}
		if input.Type != "hello" {
			continue
		}
		// Reconcile tasks already running when this socket connected. This
		// closes the race between a durable cancellation and reconnect.
		for _, taskID := range input.ActiveTaskIDs {
			task, found := h.repo.RunnerTask(taskID)
			if !found || task.LeaseOwner != runner.ID || task.DesiredState == "" {
				continue
			}
			if task.DesiredState == domain.RunnerTaskDesiredCancel {
				h.runnerControls.sendControl(runner.ID, task.ID, task.DesiredState)
			}
		}
	}
}
