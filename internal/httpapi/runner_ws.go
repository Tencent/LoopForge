package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	runnerWSPingInterval = 30 * time.Second
	runnerWSWriteTimeout = 10 * time.Second
)

// runnerWSReadTimeout outlives two ping periods. Without a read deadline a
// half-open socket (dropped by a middlebox without a FIN) stays registered:
// workspace requests are written into it and wait out their own timeout, and
// the runner looks online until the kernel gives up. Every pong and message
// extends it, so a live but idle runner is never cut off.
var runnerWSReadTimeout = 75 * time.Second

// Reconnect tolerance for transient workspace RPCs. A runner reconnects about
// one second after its socket drops (process handover, network blip), so a
// short wait turns that gap into latency instead of a failed tool call.
var (
	runnerReconnectWait  = 5 * time.Second
	runnerReconnectGrace = 15 * time.Second
	runnerStartupGrace   = 30 * time.Second
	// runnerHandoverDrain bounds how long a handed-over process keeps runner
	// sockets open for requests already running on them.
	runnerHandoverDrain = 10 * time.Second
)

type runnerControlMessage struct {
	ToolRequest       *domain.RunnerToolRequest       `json:"toolRequest,omitempty"`
	ToolResponse      *domain.RunnerToolResponse      `json:"toolResponse,omitempty"`
	Type              string                          `json:"type"`
	TaskID            string                          `json:"taskId,omitempty"`
	DesiredState      string                          `json:"desiredState,omitempty"`
	ActiveTaskIDs     []string                        `json:"activeTaskIds,omitempty"`
	RequestID         string                          `json:"requestId,omitempty"`
	WorkspaceRequest  *domain.RunnerWorkspaceRequest  `json:"workspaceRequest,omitempty"`
	WorkspaceResponse *domain.RunnerWorkspaceResponse `json:"workspaceResponse,omitempty"`
	WorkspaceProgress *domain.WorkspaceOpening        `json:"workspaceProgress,omitempty"`
}

type runnerWorkspaceForwarder func(context.Context, string, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error)

type runnerControlHub struct {
	mu                  sync.Mutex
	conns               map[string]*runnerControlConn
	pending             map[string]chan domain.RunnerWorkspaceResponse
	progress            map[string]runnerWorkspaceProgress
	onWorkspaceProgress func(string, domain.WorkspaceOpening)
	nextID              uint64
	// registered is closed and replaced whenever a runner (re)connects.
	registered chan struct{}
	// dropped records when each runner's socket last went away.
	dropped   map[string]time.Time
	startedAt time.Time
	// successor reaches runners through the process that took over the public
	// listener; it is only used once handedOver is set.
	successor  runnerWorkspaceForwarder
	handedOver atomic.Bool

	reconnectWait  time.Duration
	reconnectGrace time.Duration
	startupGrace   time.Duration
	handoverDrain  time.Duration
}

type runnerWorkspaceProgress struct {
	conn    *runnerControlConn
	updates chan domain.WorkspaceOpening
}

type runnerControlConn struct {
	runnerID string
	ws       *websocket.Conn
	send     chan runnerControlMessage
	done     chan struct{}
	once     sync.Once
}

func newRunnerControlHub() *runnerControlHub {
	return &runnerControlHub{
		conns:          map[string]*runnerControlConn{},
		pending:        map[string]chan domain.RunnerWorkspaceResponse{},
		progress:       map[string]runnerWorkspaceProgress{},
		registered:     make(chan struct{}),
		dropped:        map[string]time.Time{},
		startedAt:      time.Now(),
		reconnectWait:  runnerReconnectWait,
		reconnectGrace: runnerReconnectGrace,
		startupGrace:   runnerStartupGrace,
		handoverDrain:  runnerHandoverDrain,
	}
}

var errRunnerWorkspaceUnavailable = errors.New("runner workspace unavailable")

// requestWorkspace performs a transient RPC over the runner's outbound
// control socket. Neither the request nor file contents touch the durable task
// queue. A request that has not left this process yet survives a socket swap:
// it waits briefly for the runner to reconnect, or, after a listener handover,
// is relayed through the successor process that the runner reconnects to. A
// request already handed to the socket is never replayed, so writes and
// commands cannot run twice.
func (h *runnerControlHub) requestWorkspace(ctx context.Context, runnerID string, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
	return h.request(ctx, runnerID, request, true, false)
}

// requestRelayed serves a request a handed-over predecessor relayed here. It
// never relays again (no loops) and always tolerates a reconnect gap, because
// the predecessor has just dropped the runner's socket.
func (h *runnerControlHub) requestRelayed(ctx context.Context, runnerID string, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
	return h.request(ctx, runnerID, request, false, true)
}

func (h *runnerControlHub) request(ctx context.Context, runnerID string, request domain.RunnerWorkspaceRequest, mayRelay, expectReconnect bool) (domain.RunnerWorkspaceResponse, error) {
	if runnerID == "" {
		return domain.RunnerWorkspaceResponse{}, errRunnerWorkspaceUnavailable
	}
	var waitUntil time.Time
	for attempt := 0; attempt < 4; attempt++ {
		conn, relay, err := h.connFor(ctx, runnerID, mayRelay, expectReconnect, &waitUntil)
		if relay != nil {
			return relay(ctx, runnerID, request)
		}
		if err != nil {
			return domain.RunnerWorkspaceResponse{}, err
		}
		response, sent, err := h.send(ctx, conn, request)
		if sent || !errors.Is(err, errRunnerWorkspaceUnavailable) {
			return response, err
		}
		// The socket closed before the request left: look up its replacement.
		expectReconnect = true
	}
	return domain.RunnerWorkspaceResponse{}, errRunnerWorkspaceUnavailable
}

// connFor returns a live socket for runnerID, a relay to the successor, or
// errRunnerWorkspaceUnavailable once no reconnect is expected in time.
func (h *runnerControlHub) connFor(ctx context.Context, runnerID string, mayRelay, expectReconnect bool, waitUntil *time.Time) (*runnerControlConn, runnerWorkspaceForwarder, error) {
	for {
		h.mu.Lock()
		// After a handover new requests go to the successor even while the
		// old socket drains the requests already running on it.
		if mayRelay && h.handedOver.Load() && h.successor != nil {
			h.mu.Unlock()
			return nil, h.successor, nil
		}
		conn := h.conns[runnerID]
		if conn != nil && !conn.closed() {
			h.mu.Unlock()
			return conn, nil, nil
		}
		now := time.Now()
		if waitUntil.IsZero() {
			if !expectReconnect && !h.reconnectExpectedLocked(runnerID, now) {
				h.mu.Unlock()
				return nil, nil, errRunnerWorkspaceUnavailable
			}
			wait := h.reconnectWait
			if !mayRelay || now.Sub(h.startedAt) < h.startupGrace {
				// Right after a handover the predecessor may still be draining
				// the runner's old socket before the runner moves here.
				wait += h.handoverDrain
			}
			*waitUntil = now.Add(wait)
		}
		remaining := waitUntil.Sub(now)
		registered := h.registered
		h.mu.Unlock()
		if remaining <= 0 {
			return nil, nil, errRunnerWorkspaceUnavailable
		}
		timer := time.NewTimer(remaining)
		select {
		case <-registered:
			timer.Stop()
		case <-timer.C:
			return nil, nil, errRunnerWorkspaceUnavailable
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		}
	}
}

func (h *runnerControlHub) reconnectExpectedLocked(runnerID string, now time.Time) bool {
	if now.Sub(h.startedAt) < h.startupGrace {
		return true
	}
	dropped, ok := h.dropped[runnerID]
	return ok && now.Sub(dropped) < h.reconnectGrace
}

// send delivers one request; sent reports whether it reached the socket's
// queue, after which it must not be retried elsewhere.
func (h *runnerControlHub) send(ctx context.Context, conn *runnerControlConn, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, bool, error) {
	h.mu.Lock()
	h.nextID++
	requestID := fmt.Sprintf("workspace-%d", h.nextID)
	result := make(chan domain.RunnerWorkspaceResponse, 1)
	h.pending[requestID] = result
	updates := make(chan domain.WorkspaceOpening, 1)
	if request.Operation == domain.RunnerWorkspaceOperationPrepare && request.RunKind == domain.RunKindWorkspace {
		h.progress[requestID] = runnerWorkspaceProgress{conn: conn, updates: updates}
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, requestID)
		delete(h.progress, requestID)
		h.mu.Unlock()
	}()

	select {
	case <-conn.done:
		return domain.RunnerWorkspaceResponse{}, false, errRunnerWorkspaceUnavailable
	default:
	}
	select {
	case conn.send <- runnerControlMessage{Type: "workspace.request", RequestID: requestID, WorkspaceRequest: &request}:
	case <-conn.done:
		return domain.RunnerWorkspaceResponse{}, false, errRunnerWorkspaceUnavailable
	case <-ctx.Done():
		return domain.RunnerWorkspaceResponse{}, false, ctx.Err()
	}
	report := func(p domain.WorkspaceOpening) {
		if h.onWorkspaceProgress != nil {
			h.onWorkspaceProgress(request.RunID, p)
		}
	}
	for {
		select {
		case progress := <-updates:
			report(progress)
		case response := <-result:
			select {
			case progress := <-updates:
				report(progress)
			default:
			}
			return response, true, nil
		case <-conn.done:
			return domain.RunnerWorkspaceResponse{}, true, errRunnerWorkspaceUnavailable
		case <-ctx.Done():
			// Preparation and checkpoint work must release the remote workspace.
			// Persistent conversation processes have their own stop operation.
			if domain.IsWorkspaceCheckpointTransferOperation(request.Operation) || request.Operation == domain.RunnerWorkspaceOperationCheckpoint ||
				(request.Operation == domain.RunnerWorkspaceOperationPrepare && request.RunKind == domain.RunKindWorkspace) {
				timer := time.NewTimer(time.Second)
				select {
				case conn.send <- runnerControlMessage{Type: "workspace.cancel", RequestID: requestID}:
				case <-conn.done:
				case <-timer.C:
				}
				timer.Stop()
			}
			return domain.RunnerWorkspaceResponse{}, true, ctx.Err()
		}
	}
}

func (h *runnerControlHub) resolveWorkspaceProgress(conn *runnerControlConn, requestID string, progress domain.WorkspaceOpening) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pending, ok := h.progress[requestID]
	if !ok || pending.conn != conn {
		return
	}
	select {
	case pending.updates <- progress:
	default:
		select {
		case <-pending.updates:
		default:
		}
		pending.updates <- progress
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
	close(h.registered)
	h.registered = make(chan struct{})
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
		h.dropped[c.runnerID] = time.Now()
	}
	h.mu.Unlock()
	c.close()
}

// closeAll drops every runner socket so runners reconnect, e.g. to the
// process that took over the listener.
func (h *runnerControlHub) closeAll() {
	h.mu.Lock()
	conns := make([]*runnerControlConn, 0, len(h.conns))
	now := time.Now()
	for id, c := range h.conns {
		conns = append(conns, c)
		h.dropped[id] = now
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
}

// handOver relays later workspace requests through the successor process.
// Requests already on a runner socket may be mid-command or mid-write and
// cannot be replayed, so the sockets stay open until those finish (bounded by
// handoverDrain); then they close and runners reconnect to the successor.
func (h *runnerControlHub) handOver() {
	h.handedOver.Store(true)
	go h.closeWhenIdle(h.handoverDrain)
}

func (h *runnerControlHub) closeWhenIdle(limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		busy := len(h.pending)
		h.mu.Unlock()
		if busy == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.closeAll()
}

const runnerWorkspaceRelayPath = "/internal/runner-workspace"

// runnerWorkspaceRelayLimit bounds relayed bodies; the runner socket itself
// accepts 20 MiB messages, the envelope adds a little JSON around them.
const runnerWorkspaceRelayLimit = 24 << 20

type runnerWorkspaceRelayRequest struct {
	RunnerID string                        `json:"runnerId"`
	Request  domain.RunnerWorkspaceRequest `json:"request"`
}

// handleRunnerWorkspaceRelay serves workspace RPCs that a handed-over
// predecessor relays to the runner sockets this process now holds. The
// predecessor already authorized and bound the request; this endpoint only
// accepts the shared service token and never relays again.
func handleRunnerWorkspaceRelay(e *engine.Engine, hub *runnerControlHub) gin.HandlerFunc {
	return func(c *gin.Context) {
		if e == nil || !e.ServiceTokenOK(c.GetHeader(engine.ControlTokenHeader)) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		var input runnerWorkspaceRelayRequest
		if err := json.NewDecoder(io.LimitReader(c.Request.Body, runnerWorkspaceRelayLimit)).Decode(&input); err != nil || strings.TrimSpace(input.RunnerID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
		response, err := hub.requestRelayed(c.Request.Context(), input.RunnerID, input.Request)
		if err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				status = http.StatusGatewayTimeout
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, response)
	}
}

// relayRunnerWorkspaceToSuccessor reaches runners through the process that
// took over the listener, over the API address this process was configured
// with (never a user-supplied address).
func relayRunnerWorkspaceToSuccessor(e *engine.Engine) runnerWorkspaceForwarder {
	return func(ctx context.Context, runnerID string, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		base, token := e.InternalAPI()
		if base == "" || token == "" {
			return domain.RunnerWorkspaceResponse{}, fmt.Errorf("%w: handed-over process needs AGENTFLOW_CONTROL_TOKEN and an internal API URL to reach runners", errRunnerWorkspaceUnavailable)
		}
		body, err := json.Marshal(runnerWorkspaceRelayRequest{RunnerID: runnerID, Request: request})
		if err != nil {
			return domain.RunnerWorkspaceResponse{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+runnerWorkspaceRelayPath, bytes.NewReader(body))
		if err != nil {
			return domain.RunnerWorkspaceResponse{}, err
		}
		req.Header.Set(engine.ControlTokenHeader, token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return domain.RunnerWorkspaceResponse{}, ctx.Err()
			}
			return domain.RunnerWorkspaceResponse{}, fmt.Errorf("%w: relay to successor: %v", errRunnerWorkspaceUnavailable, err)
		}
		defer res.Body.Close()
		reader := io.LimitReader(res.Body, runnerWorkspaceRelayLimit)
		if res.StatusCode != http.StatusOK {
			var failure struct {
				Error string `json:"error"`
			}
			_ = json.NewDecoder(reader).Decode(&failure)
			if res.StatusCode == http.StatusGatewayTimeout && ctx.Err() != nil {
				return domain.RunnerWorkspaceResponse{}, ctx.Err()
			}
			return domain.RunnerWorkspaceResponse{}, fmt.Errorf("%w: successor returned HTTP %d %s", errRunnerWorkspaceUnavailable, res.StatusCode, strings.TrimSpace(failure.Error))
		}
		var response domain.RunnerWorkspaceResponse
		if err := json.NewDecoder(reader).Decode(&response); err != nil {
			return domain.RunnerWorkspaceResponse{}, fmt.Errorf("relay to successor: %w", err)
		}
		return response, nil
	}
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
		if c.ws != nil {
			_ = c.ws.Close()
		}
	})
}

func (c *runnerControlConn) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
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
	_ = ws.SetReadDeadline(time.Now().Add(runnerWSReadTimeout))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(runnerWSReadTimeout))
	})
	for {
		var input runnerControlMessage
		if err := ws.ReadJSON(&input); err != nil {
			return
		}
		_ = ws.SetReadDeadline(time.Now().Add(runnerWSReadTimeout))
		if input.Type == "workspace.progress" && input.WorkspaceProgress != nil {
			h.runnerControls.resolveWorkspaceProgress(conn, input.RequestID, *input.WorkspaceProgress)
			continue
		}
		if input.Type == "agent_tool.request" && input.ToolRequest != nil {
			go func(input runnerControlMessage) {
				result := domain.RunnerToolResponse{}
				session, ok := agentToolSessions.get(input.ToolRequest.Token)
				if !ok || session.ExecutionRunnerID != runner.ID {
					result.Error = "invalid conversation gateway"
				} else {
					var request agentToolRPCRequest
					if err := json.Unmarshal(input.ToolRequest.Body, &request); err != nil {
						result.Error = "invalid tool request"
					} else {
						response := (&agentToolGateway{deps: h.deps}).handleRPC(withAgentToolToken(c.Request.Context(), input.ToolRequest.Token), session, request)
						if response != nil {
							result.Body, _ = json.Marshal(response)
						}
					}
				}
				select {
				case conn.send <- runnerControlMessage{Type: "agent_tool.response", RequestID: input.RequestID, ToolResponse: &result}:
				case <-conn.done:
				}
			}(input)
			continue
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
