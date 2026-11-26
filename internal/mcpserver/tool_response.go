package mcpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AcceptsEventStream reports whether the client advertised Streamable HTTP SSE.
func AcceptsEventStream(r *http.Request) bool {
	for _, header := range r.Header.Values("Accept") {
		for _, entry := range strings.Split(header, ",") {
			mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(entry))
			if err != nil || mediaType != "text/event-stream" {
				continue
			}
			if params["q"] == "" {
				return true
			}
			if quality, err := strconv.ParseFloat(params["q"], 64); err == nil && quality > 0 && quality <= 1 {
				return true
			}
		}
	}
	return false
}

// WriteToolResponse keeps slow tool calls, including approval waits, alive on
// transports with an idle timeout. Heartbeats finish before the tool result
// is written and before the HTTP handler returns.
func WriteToolResponse(w http.ResponseWriter, r *http.Request, id any, call func() (json.RawMessage, error)) {
	stop := make(chan struct{})
	done := make(chan struct{})
	finish := sync.OnceFunc(func() {
		close(stop)
		<-done
	})
	defer finish()

	streamed := false
	go func() {
		defer close(done)
		// Fast calls keep the JSON response. A slow response sends its
		// headers with the first comment and then sends periodic comments.
		heartbeat := time.NewTimer(time.Second)
		defer heartbeat.Stop()
		controller := http.NewResponseController(w)
		for {
			select {
			case <-stop:
				return
			case <-r.Context().Done():
				return
			case <-heartbeat.C:
				if !streamed {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Cache-Control", "no-cache")
					w.Header().Set("X-Accel-Buffering", "no")
					streamed = true
				}
				if _, err := io.WriteString(w, ": awaiting tool result\n\n"); err != nil {
					return
				}
				if err := controller.Flush(); err != nil {
					return
				}
				heartbeat.Reset(15 * time.Second)
			}
		}
	}()

	body, err := call()
	finish()
	if err != nil {
		if !streamed {
			http.Error(w, "gateway unavailable", http.StatusBadGateway)
			return
		}
		body, _ = json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"error":   map[string]any{"code": -32603, "message": "gateway unavailable"},
		})
	}
	if streamed {
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if len(body) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	_, _ = w.Write(body)
}
