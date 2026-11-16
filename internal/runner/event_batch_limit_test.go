package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"agentflow/internal/domain"
)

func TestSendEventsSplitsRequestsAtControlPlaneLimits(t *testing.T) {
	var mu sync.Mutex
	var got []int64
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []domain.RunnerTaskEvent `json:"events"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body); err != nil {
			http.Error(w, "request too large or invalid", http.StatusBadRequest)
			return
		}
		if len(body.Events) > 500 {
			http.Error(w, "too many events", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		requests++
		for _, event := range body.Events {
			got = append(got, event.Seq)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	var events []domain.RunnerTaskEvent
	var want []int64
	for i := range 520 {
		number := int64(i + 1)
		data := "small log line"
		if i >= 510 {
			// HTML characters expand to six bytes each in the JSON body.
			data = strings.Repeat("<", 100_000)
		}
		events = append(events, domain.RunnerTaskEvent{Seq: number, Type: "stdout", Data: data})
		want = append(want, number)
	}
	if err := NewClient(server.URL).SendEvents(context.Background(), "task", events); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests < 3 || !slices.Equal(got, want) {
		t.Fatalf("sent %d requests with %d events, want all %d events in order", requests, len(got), len(want))
	}
}
