package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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

func TestSendEventsTruncatesOversizedRecordWithoutBlockingFollowingLogs(t *testing.T) {
	for _, data := range []string{
		strings.Repeat("plain output ", (3<<20)/13),
		strings.Repeat("<\"\n", 250_000),
		strings.Repeat("你好🌍", 300_000),
	} {
		t.Run(fmt.Sprintf("bytes-%d", len(data)), func(t *testing.T) {
			var got []domain.RunnerTaskEvent
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Events []domain.RunnerTaskEvent `json:"events"`
				}
				if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, domain.RunnerTaskEventBodyLimit)).Decode(&body); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if len(body.Events) > domain.RunnerTaskEventBatchLimit {
					t.Error("batch limit exceeded")
				}
				mu.Lock()
				got = append(got, body.Events...)
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)
			events := []domain.RunnerTaskEvent{
				{Seq: 1, Type: "stdout", Data: "before"},
				{Seq: 2, Type: "stderr", Data: "HEAD-" + data + "-TAIL", Ts: time.Unix(1234, 0).UTC()},
				{Seq: 3, Type: "stdout", Data: "after"},
			}
			original := slices.Clone(events)
			raw, err := json.Marshal(events[1])
			if err != nil || len(raw) <= domain.RunnerTaskEventBodyLimit {
				t.Fatal("fixture must exceed body limit")
			}
			if err := NewClient(server.URL).SendEvents(context.Background(), "task", events); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) != 3 {
				t.Fatalf("got %d events, want 3", len(got))
			}
			if got[0] != events[0] || got[2] != events[2] {
				t.Fatal("neighboring events changed")
			}
			for i, event := range got {
				if event.Seq != int64(i+1) {
					t.Fatal("sequence order changed")
				}
			}
			if got[1].Type != events[1].Type || !got[1].Ts.Equal(events[1].Ts) {
				t.Fatal("event metadata changed")
			}
			if !strings.HasPrefix(got[1].Data, "HEAD-") || !strings.HasSuffix(got[1].Data, "-TAIL") || !strings.Contains(got[1].Data, "[runner output truncated]") {
				t.Fatal("missing head, tail or truncation marker")
			}
			if !utf8.ValidString(got[1].Data) {
				t.Fatal("truncation split a UTF-8 character")
			}
			if !slices.Equal(events, original) {
				t.Fatal("caller events mutated")
			}
		})
	}
}

func TestSendEventsTruncatesOversizedObservationAsValidTelemetry(t *testing.T) {
	original := domain.TelemetryEvent{Category: "tool", Name: "tool.result", Message: "HEAD-" + strings.Repeat("<", 500_000) + "-TAIL"}
	payload, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var received []domain.RunnerTaskEvent
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []domain.RunnerTaskEvent `json:"events"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, domain.RunnerTaskEventBodyLimit)).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		mu.Lock()
		received = append(received, body.Events...)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	events := []domain.RunnerTaskEvent{{Seq: 1, Type: "observation", Data: string(payload), Ts: time.Unix(1234, 0).UTC()}, {Seq: 2, Type: "stdout", Data: "following log"}}
	if err := NewClient(server.URL).SendEvents(context.Background(), "task", events); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 || received[0].Seq != 1 || received[0].Type != "observation" || received[1] != events[1] {
		t.Fatal("event order or metadata changed")
	}
	// This is the same typed decode required by runner_dispatch.go before sink().
	var observation domain.TelemetryEvent
	if err := json.Unmarshal([]byte(received[0].Data), &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Category != "process" || observation.Name != "process.output_truncated" || observation.Level != "warning" || !strings.Contains(observation.Message, "[runner output truncated]") || !strings.Contains(observation.Message, "HEAD-") || !strings.Contains(observation.Message, "-TAIL") {
		t.Fatal("missing bounded warning representation")
	}
}
