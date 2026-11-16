package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"agentflow/internal/domain"
)

func TestCompletionRetriesTemporaryServerFailure(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req CompleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Status != domain.RunnerTaskSuccess || req.Result != "finished" {
			http.Error(w, "invalid report", http.StatusBadRequest)
			return
		}
		if attempts.Add(1) == 1 {
			http.Error(w, "database restarting", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	err := NewClient(server.URL).Complete(context.Background(), "task", CompleteRequest{Status: domain.RunnerTaskSuccess, Result: "finished"})
	if err != nil || attempts.Load() != 2 {
		t.Fatalf("completion attempts=%d, error=%v, want retry and success", attempts.Load(), err)
	}
}

func TestCompletionDoesNotRetryLostLease(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"task lease lost"}`))
	}))
	t.Cleanup(server.Close)
	err := NewClient(server.URL).Complete(context.Background(), "task", CompleteRequest{Status: domain.RunnerTaskSuccess})
	if !errors.Is(err, ErrTaskLeaseLost) || attempts.Load() != 1 {
		t.Fatalf("completion attempts=%d, error=%v, want a single loss", attempts.Load(), err)
	}
}
