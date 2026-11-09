package runner

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/workspaceprep"

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

type controlMessage struct {
	Type              string                          `json:"type"`
	TaskID            string                          `json:"taskId,omitempty"`
	DesiredState      string                          `json:"desiredState,omitempty"`
	ActiveTaskIDs     []string                        `json:"activeTaskIds,omitempty"`
	RequestID         string                          `json:"requestId,omitempty"`
	WorkspaceRequest  *domain.RunnerWorkspaceRequest  `json:"workspaceRequest,omitempty"`
	WorkspaceResponse *domain.RunnerWorkspaceResponse `json:"workspaceResponse,omitempty"`
	WorkspaceProgress *domain.WorkspaceOpening        `json:"workspaceProgress,omitempty"`
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
		if err := conn.WriteJSON(controlMessage{Type: "hello", ActiveTaskIDs: d.activeTaskIDs()}); err == nil {
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
	var writeMu sync.Mutex
	var cancelMu sync.Mutex
	cancellations := map[string]context.CancelFunc{}
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
		if message.Type == "workspace.cancel" {
			cancelMu.Lock()
			if stop := cancellations[message.RequestID]; stop != nil {
				stop()
			}
			cancelMu.Unlock()
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
				writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				err := conn.WriteJSON(controlMessage{Type: "workspace.response", RequestID: message.RequestID, WorkspaceResponse: &response})
				writeMu.Unlock()
				if err != nil {
					return err
				}
				continue
			}
			requests.Add(1)
			requestCtx, stop := context.WithCancel(ctx)
			cancelMu.Lock()
			cancellations[message.RequestID] = stop
			cancelMu.Unlock()
			go func(message controlMessage) {
				defer requests.Done()
				defer func() {
					stop()
					cancelMu.Lock()
					delete(cancellations, message.RequestID)
					cancelMu.Unlock()
				}()
				defer func() { <-slots }()
				if request := message.WorkspaceRequest; request.Operation == domain.RunnerWorkspaceOperationPrepare && request.RunKind == domain.RunKindWorkspace {
					requestCtx = workspaceprep.WithSink(requestCtx, func(progress domain.WorkspaceOpening) {
						writeMu.Lock()
						defer writeMu.Unlock()
						_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
						if err := conn.WriteJSON(controlMessage{Type: "workspace.progress", RequestID: message.RequestID, WorkspaceProgress: &progress}); err != nil {
							stop()
						}
					})
				}
				response := d.readRunnerWorkspace(requestCtx, *message.WorkspaceRequest)
				writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				err := conn.WriteJSON(controlMessage{Type: "workspace.response", RequestID: message.RequestID, WorkspaceResponse: &response})
				writeMu.Unlock()
				if err != nil {
					_ = conn.Close()
				}
			}(message)
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
