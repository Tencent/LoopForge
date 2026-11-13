package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// The stream keeps the frame long-poll next to the browser and pushes frames
// to the panel over one WebSocket. Polling from the panel pays the access
// gateway's per-request latency on every frame; here only the first one does.
var browserStreamUpgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	ReadBufferSize:   4 << 10,
	WriteBufferSize:  64 << 10,
}

const (
	browserStreamWriteWait  = 10 * time.Second
	browserStreamPongWait   = 60 * time.Second
	browserStreamPing       = 20 * time.Second
	browserStreamRunRefresh = 10 * time.Second
	browserStreamRetry      = 4 * time.Second
	// Frame long-polls kept waiting at the browser. Three cover a round trip
	// of a few frame intervals at the rates a sandbox browser renders.
	browserStreamFramePulls = 3
	browserStreamPullGap    = 15 * time.Millisecond
)

type browserStreamMessage struct {
	Type  string               `json:"type"`
	State *domain.BrowserState `json:"state,omitempty"`
	Error string               `json:"error,omitempty"`
}

type browserStream struct {
	h       *browserHandlers
	ws      *websocket.Conn
	writeMu sync.Mutex

	runID string

	mu    sync.Mutex
	run   domain.Run
	runAt time.Time
	seq   int64
	meta  []byte
}

// stream serves GET /api/runs/:id/browser/stream. Binary messages are JPEG
// frames; text messages carry the remaining state or an operation error. The
// panel sends BrowserRequest JSON for input and navigation.
func (h *browserHandlers) stream(c *gin.Context) {
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
	control := h.canControl(c.Request, run, user.ID)
	ws, err := browserStreamUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &browserStream{h: h, ws: ws, runID: run.ID, run: run, runAt: time.Now()}
	commands := make(chan domain.BrowserRequest, 64)
	go s.read(cancel, commands)
	go s.execute(ctx, cancel, commands, control)
	go s.ping(ctx, cancel)
	s.frames(ctx, cancel)
}

func (s *browserStream) read(cancel context.CancelFunc, commands chan<- domain.BrowserRequest) {
	defer cancel()
	defer close(commands)
	s.ws.SetReadLimit(256 << 10)
	_ = s.ws.SetReadDeadline(time.Now().Add(browserStreamPongWait))
	s.ws.SetPongHandler(func(string) error {
		return s.ws.SetReadDeadline(time.Now().Add(browserStreamPongWait))
	})
	for {
		kind, raw, err := s.ws.ReadMessage()
		if err != nil {
			return
		}
		_ = s.ws.SetReadDeadline(time.Now().Add(browserStreamPongWait))
		var req domain.BrowserRequest
		if kind != websocket.TextMessage || json.Unmarshal(raw, &req) != nil || browserReadOperations[req.Operation] {
			continue
		}
		commands <- req
	}
}

// execute runs panel commands in order, merging queued input batches so a
// slow round trip does not replay stale pointer moves one by one.
func (s *browserStream) execute(ctx context.Context, cancel context.CancelFunc, commands <-chan domain.BrowserRequest, control bool) {
	var pending *domain.BrowserRequest
	for {
		var req domain.BrowserRequest
		if pending != nil {
			req, pending = *pending, nil
		} else {
			next, ok := <-commands
			if !ok {
				return
			}
			req = next
		}
		if req.Operation == "input" {
		merge:
			for {
				select {
				case next, ok := <-commands:
					if !ok {
						break merge
					}
					if next.Operation != "input" {
						pending = &next
						break merge
					}
					req.Events = append(req.Events, next.Events...)
				default:
					break merge
				}
			}
		}
		if !control {
			if s.send(browserStreamMessage{Type: "error", Error: "无权操作该任务的浏览器"}) != nil {
				cancel()
			}
			continue
		}
		s.h.touchWorkspace(s.runID)
		payload, opErr := s.h.dispatch(ctx, s.currentRun(false), req)
		if ctx.Err() != nil {
			return
		}
		available, err := s.publish(payload)
		if err != nil {
			cancel()
			return
		}
		if req.Ensure && !available && opErr == "" {
			// An unchanged state is not re-sent, so a failed start must answer.
			var state domain.BrowserState
			_ = json.Unmarshal(payload, &state)
			opErr = firstNonEmpty(state.Reason, "Agent 浏览器未能启动")
		}
		if opErr != "" && s.send(browserStreamMessage{Type: "error", Error: opErr}) != nil {
			cancel()
			return
		}
	}
}

func (s *browserStream) frames(ctx context.Context, cancel context.CancelFunc) {
	pull := func(ctx context.Context, after int64) json.RawMessage {
		payload, _ := s.h.dispatch(ctx, s.currentRun(false), domain.BrowserRequest{Operation: "frame", After: after, WaitMs: 3000})
		return payload
	}
	err := pipelineFrames(ctx, browserStreamFramePulls, pull, s.publish, func() int64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.seq
	}, func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(browserStreamRetry):
		}
		s.currentRun(true)
		return true
	})
	if err != nil {
		cancel()
	}
}

// pipelineFrames keeps pulls frame long-polls waiting at the browser, each
// for a later frame than the latest shown (latest, latest+1, …). A frame
// then leaves the moment it is captured instead of one round trip after the
// previous one arrived: a single poll delivered about half the frames a
// scrolling page produced.
//
// Thresholds always count from the latest frame shown, never from earlier
// polls: a poll that times out on an idle page is sent again for the same
// frame. Counting up per poll ran the thresholds ahead of the browser while
// nothing moved, and the runtime only returns an image newer than the
// threshold, so the next frames were never shown.
//
// publish reports whether the browser is available; while it is not, the
// polls drain and retry waits before the next round.
func pipelineFrames(
	ctx context.Context,
	pulls int,
	pull func(context.Context, int64) json.RawMessage,
	publish func(json.RawMessage) (bool, error),
	latest func() int64,
	retry func() bool,
) error {
	type answer struct {
		after   int64
		payload json.RawMessage
	}
	results := make(chan answer, pulls)
	var issued time.Time
	waiting := map[int64]bool{}
	down := false
	for {
		for !down && len(waiting) < pulls && ctx.Err() == nil {
			// A spaced start keeps a runtime that answers at once (no frame
			// wait) from turning this into a hot loop.
			if wait := browserStreamPullGap - time.Since(issued); wait > 0 {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(wait):
				}
			}
			issued = time.Now()
			after := latest()
			for waiting[after] {
				after++
			}
			waiting[after] = true
			go func() {
				payload := pull(ctx, after)
				select {
				case results <- answer{after, payload}:
				case <-ctx.Done():
				}
			}()
		}
		if len(waiting) == 0 {
			if ctx.Err() != nil || !retry() {
				return nil
			}
			down = false
			continue
		}
		var got answer
		select {
		case <-ctx.Done():
			return nil
		case got = <-results:
		}
		delete(waiting, got.after)
		available, err := publish(got.payload)
		if err != nil {
			return err
		}
		if !available {
			down = true
		}
	}
}

func (s *browserStream) ping(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(browserStreamPing)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.writeMu.Lock()
			err := s.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(browserStreamWriteWait))
			s.writeMu.Unlock()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

// currentRun re-reads the run now and then: its browser may move to another
// executor while the panel stays open.
func (s *browserStream) currentRun(force bool) domain.Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	if force || time.Since(s.runAt) > browserStreamRunRefresh {
		if run, ok := s.h.repo.RunState(s.runID); ok {
			s.run = run
		}
		s.runAt = time.Now()
	}
	return s.run
}

// publish sends the state when it changed and a frame newer than the last one.
func (s *browserStream) publish(payload json.RawMessage) (bool, error) {
	var state domain.BrowserState
	if json.Unmarshal(payload, &state) != nil {
		return false, nil
	}
	frame, seq := state.Frame, state.Seq
	state.Frame, state.Seq = "", 0
	meta, _ := json.Marshal(browserStreamMessage{Type: "state", State: &state})
	s.mu.Lock()
	if seq < s.seq {
		// The browser view was reopened and its sequence restarted.
		s.seq = 0
	}
	fresh := frame != "" && seq > s.seq
	if fresh {
		s.seq = seq
	}
	changed := !bytes.Equal(meta, s.meta)
	if changed {
		s.meta = meta
	}
	s.mu.Unlock()
	if changed {
		if err := s.write(websocket.TextMessage, meta); err != nil {
			return state.Available, err
		}
	}
	if fresh {
		if jpeg, err := base64.StdEncoding.DecodeString(frame); err == nil {
			if err := s.write(websocket.BinaryMessage, jpeg); err != nil {
				return state.Available, err
			}
		}
	}
	return state.Available, nil
}

func (s *browserStream) send(message browserStreamMessage) error {
	raw, _ := json.Marshal(message)
	return s.write(websocket.TextMessage, raw)
}

func (s *browserStream) write(kind int, data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.ws.SetWriteDeadline(time.Now().Add(browserStreamWriteWait))
	return s.ws.WriteMessage(kind, data)
}
