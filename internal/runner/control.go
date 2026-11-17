package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"agentflow/internal/domain"

	"github.com/gorilla/websocket"
)

const (
	controlReconnectMin = time.Second
	controlReconnectMax = 30 * time.Second
)

// controlReadTimeout outlives two server ping periods (30s). Without a read
// deadline a half-open socket keeps ReadJSON blocked forever: the daemon
// looks connected and never reconnects. The server only sends pings, so the
// ping handler (not a pong handler) extends the deadline.
var controlReadTimeout = 75 * time.Second

var controlWriteTimeout = 10 * time.Second

type controlMessage struct {
	ToolRequest       *domain.RunnerToolRequest       `json:"toolRequest,omitempty"`
	ToolResponse      *domain.RunnerToolResponse      `json:"toolResponse,omitempty"`
	Type              string                          `json:"type"`
	TaskID            string                          `json:"taskId,omitempty"`
	DesiredState      string                          `json:"desiredState,omitempty"`
	ActiveTaskIDs     []string                        `json:"activeTaskIds,omitempty"`
	RequestID         string                          `json:"requestId,omitempty"`
	WorkspaceRequest  *domain.RunnerWorkspaceRequest  `json:"workspaceRequest,omitempty"`
	WorkspaceResponse *domain.RunnerWorkspaceResponse `json:"workspaceResponse,omitempty"`
}

// controlLoop maintains one outbound WebSocket for immediate server-to-runner
// controls. Reconnect is automatic; durable desired_state and lease renewal
// cover every disconnected interval.
func (d *Daemon) controlLoop(ctx context.Context) {
	backoff := controlReconnectMin
	for ctx.Err() == nil {
		conn, err := d.client.OpenControl(ctx)
		if err != nil {
			log.Printf("control websocket connect: %v", err)
			if errors.Is(err, ErrUnauthorized) {
				if registerErr := d.reregister(ctx); registerErr != nil {
					log.Printf("control re-register: %v", registerErr)
				}
			}
			if !waitContext(ctx, backoff) {
				return
			}
			backoff *= 2
			if backoff > controlReconnectMax {
				backoff = controlReconnectMax
			}
			continue
		}

		d.wsOnline.Store(true)
		backoff = controlReconnectMin
		_ = conn.SetWriteDeadline(time.Now().Add(controlWriteTimeout))
		if err = conn.WriteJSON(controlMessage{Type: "hello", ActiveTaskIDs: d.activeTaskIDs()}); err == nil {
			err = d.readControls(ctx, conn)
		}
		d.wsOnline.Store(false)
		_ = conn.Close()
		if ctx.Err() == nil && err != nil {
			log.Printf("control websocket disconnected: %v", err)
		}
		if !waitContext(ctx, backoff) {
			return
		}
	}
}

func (d *Daemon) readControls(ctx context.Context, conn *websocket.Conn) error {
	ctx, cancel := context.WithCancel(ctx)
	var requests sync.WaitGroup
	var checkpointMu sync.Mutex
	checkpointCancels := map[string]context.CancelFunc{}
	var writeMu sync.Mutex
	writeMessage := func(message controlMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(controlWriteTimeout))
		return conn.WriteJSON(message)
	}
	d.conversationMu.Lock()
	if d.conversationReplies == nil {
		d.conversationReplies = map[string]chan domain.RunnerToolResponse{}
	}
	d.conversationForward = func(callCtx context.Context, token string, body json.RawMessage) (json.RawMessage, error) {
		return d.forwardConversationTool(ctx, callCtx, writeMessage, token, body)
	}
	d.conversationMu.Unlock()
	slots := make(chan struct{}, 8)
	defer func() { cancel(); _ = conn.Close(); requests.Wait(); d.checkpointTransfers.Close() }()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)
	_ = conn.SetReadDeadline(time.Now().Add(controlReadTimeout))
	conn.SetPingHandler(func(data string) error {
		_ = conn.SetReadDeadline(time.Now().Add(controlReadTimeout))
		err := conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(10*time.Second))
		if err == websocket.ErrCloseSent {
			return nil
		}
		if e, ok := err.(net.Error); ok && e.Timeout() {
			return nil
		}
		return err
	})
	for {
		var message controlMessage
		if err := conn.ReadJSON(&message); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(controlReadTimeout))
		if message.Type == "agent_tool.response" && message.ToolResponse != nil {
			d.deliverConversationReply(message.RequestID, *message.ToolResponse)
			continue
		}
		if message.Type == "workspace.cancel" {
			checkpointMu.Lock()
			stop := checkpointCancels[message.RequestID]
			checkpointMu.Unlock()
			if stop != nil {
				stop()
			}
			continue
		}
		if message.Type == "task.control" {
			d.signalTaskControl(message.TaskID, message.DesiredState)
			continue
		}
		if message.Type == "workspace.request" && message.WorkspaceRequest != nil {
			select {
			case slots <- struct{}{}:
			default:
				response := domain.RunnerWorkspaceResponse{Status: 429, Error: "too many workspace requests"}
				err := writeMessage(controlMessage{Type: "workspace.response", RequestID: message.RequestID, WorkspaceResponse: &response})
				if err != nil {
					return err
				}
				continue
			}
			requestCtx := ctx
			stopRequest := func() {}
			if domain.IsWorkspaceCheckpointTransferOperation(message.WorkspaceRequest.Operation) || message.WorkspaceRequest.Operation == domain.RunnerWorkspaceOperationCheckpoint {
				requestCtx, stopRequest = context.WithCancel(ctx)
				checkpointMu.Lock()
				checkpointCancels[message.RequestID] = stopRequest
				checkpointMu.Unlock()
			}
			requests.Add(1)
			go func(message controlMessage, requestCtx context.Context, stopRequest context.CancelFunc) {
				defer requests.Done()
				defer func() { <-slots }()
				defer func() {
					stopRequest()
					checkpointMu.Lock()
					delete(checkpointCancels, message.RequestID)
					checkpointMu.Unlock()
				}()
				response := d.readRunnerWorkspace(requestCtx, *message.WorkspaceRequest)
				err := writeMessage(controlMessage{Type: "workspace.response", RequestID: message.RequestID, WorkspaceResponse: &response})
				if err != nil {
					_ = conn.Close()
				}
			}(message, requestCtx, stopRequest)
		}
	}
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (d *Daemon) registerTaskControl(taskID string) chan string {
	d.controlMu.Lock()
	defer d.controlMu.Unlock()
	ch := make(chan string, 1)
	d.controls[taskID] = ch
	if desiredState := d.pendingControls[taskID]; desiredState != "" {
		ch <- desiredState
		delete(d.pendingControls, taskID)
	}
	return ch
}

func (d *Daemon) unregisterTaskControl(taskID string) {
	d.controlMu.Lock()
	delete(d.controls, taskID)
	delete(d.pendingControls, taskID)
	d.controlMu.Unlock()
}

func (d *Daemon) taskControl(taskID string) <-chan string {
	d.controlMu.RLock()
	defer d.controlMu.RUnlock()
	return d.controls[taskID]
}

func (d *Daemon) signalTaskControl(taskID, desiredState string) {
	d.controlMu.Lock()
	ch := d.controls[taskID]
	if ch == nil {
		d.pendingControls[taskID] = desiredState
		d.controlMu.Unlock()
		return
	}
	d.controlMu.Unlock()
	select {
	case ch <- desiredState:
	default:
	}
}

func (d *Daemon) activeTaskIDs() []string {
	d.controlMu.RLock()
	defer d.controlMu.RUnlock()
	ids := make([]string, 0, len(d.controls))
	for id := range d.controls {
		ids = append(ids, id)
	}
	return ids
}

// forwardConversationTool sends one agent_tool.request over the control
// connection and waits for the matching agent_tool.response. It returns early
// when either the caller (callCtx) or the control connection (connCtx) ends.
// readControls must have initialised d.conversationReplies.
func (d *Daemon) forwardConversationTool(connCtx, callCtx context.Context, write func(controlMessage) error, token string, body json.RawMessage) (json.RawMessage, error) {
	id := fmt.Sprintf("tool-%d", time.Now().UnixNano())
	reply := make(chan domain.RunnerToolResponse, 1)
	d.conversationMu.Lock()
	d.conversationReplies[id] = reply
	d.conversationMu.Unlock()
	defer func() { d.conversationMu.Lock(); delete(d.conversationReplies, id); d.conversationMu.Unlock() }()
	err := write(controlMessage{Type: "agent_tool.request", RequestID: id, ToolRequest: &domain.RunnerToolRequest{Token: token, Body: body}})
	if err != nil {
		return nil, err
	}
	select {
	case response := <-reply:
		if response.Error != "" {
			return nil, errors.New(response.Error)
		}
		return response.Body, nil
	case <-callCtx.Done():
		return nil, callCtx.Err()
	case <-connCtx.Done():
		return nil, connCtx.Err()
	}
}

// deliverConversationReply hands an agent_tool.response to the waiting
// forwardConversationTool call. Replies for unknown or already-answered
// request IDs are dropped without blocking the control read loop.
func (d *Daemon) deliverConversationReply(requestID string, response domain.RunnerToolResponse) {
	d.conversationMu.Lock()
	reply := d.conversationReplies[requestID]
	d.conversationMu.Unlock()
	if reply != nil {
		select {
		case reply <- response:
		default:
		}
	}
}
