package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestClientRejectsIncompleteResponseBodies(t *testing.T) {
	for _, operation := range []string{"register", "heartbeat", "complete", "preview"} {
		t.Run(operation, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				payload := `{"runnerId":"r1","sessionToken":"s1","expiresAt":"2030-01-01T00:00:00Z"}`
				// The JSON itself is valid, but the HTTP body was cut short.
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)+10))
				_, _ = io.WriteString(w, payload)
			}))
			t.Cleanup(server.Close)
			client := NewClient(server.URL)
			var err error
			switch operation {
			case "register":
				_, err = client.Register(context.Background(), "fixture", RegisterRequest{})
			case "heartbeat":
				_, err = client.Heartbeat(context.Background(), HeartbeatRequest{})
			case "complete":
				err = client.Complete(context.Background(), "task", CompleteRequest{})
			case "preview":
				err = client.UploadPreview(context.Background(), "preview", "fixture", strings.NewReader("archive"))
			}
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("incomplete %s response: %v", operation, err)
			}
		})
	}
}

func TestClientRejectsOversizedAndNonSuccessResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"oversized", http.StatusOK, strings.Repeat(" ", (8<<20)+1)},
		{"unresolved redirect", http.StatusMultipleChoices, "choose another endpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			if err := NewClient(server.URL).Complete(context.Background(), "task", CompleteRequest{}); err == nil {
				t.Fatal("reported task completion success for invalid control-plane response")
			}
		})
	}
}
