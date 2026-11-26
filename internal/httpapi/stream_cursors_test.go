package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func TestRunStreamKeepsReconnectCursorsUntilNewRecordsArrive(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	run := domain.Run{ID: "cursor-reconnect", Status: "waiting"}
	appendRecords := func(seq int64) {
		t.Helper()
		run.Logs = []domain.LogRecord{{Sequence: seq, Body: "log"}}
		run.Telemetry = []domain.TelemetryEvent{{Sequence: seq, Name: "event"}}
		run.LogSequence, run.TelemetrySequence = seq, seq
		if err := st.PutRun(run); err != nil {
			t.Fatal(err)
		}
		run.Revision++
	}
	appendRecords(1)
	e := engine.New(st, nil)
	handler := &streamHandlers{deps: &deps{repo: st, engine: e}}
	router := gin.New()
	router.GET("/runs/:id", handler.streamRun)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/runs/"+run.ID+"?after=5&logsAfter=5", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	readFrame := func() (logs []domain.LogRecord, telemetry []domain.TelemetryEvent) {
		t.Helper()
		for scanner.Scan() {
			data, ok := strings.CutPrefix(scanner.Text(), "data: ")
			if !ok {
				continue
			}
			var frame struct {
				LogDelta       []domain.LogRecord      `json:"logDelta"`
				TelemetryDelta []domain.TelemetryEvent `json:"telemetryDelta"`
			}
			if err := json.Unmarshal([]byte(data), &frame); err != nil {
				t.Fatal(err)
			}
			return frame.LogDelta, frame.TelemetryDelta
		}
		t.Fatalf("no stream frame: %v", scanner.Err())
		return nil, nil
	}

	if logs, events := readFrame(); len(logs) > 0 || len(events) > 0 {
		t.Fatalf("initial frame sent records below the reconnect cursors: %v, %v", logs, events)
	}
	appendRecords(2)
	if logs, events := readFrame(); len(logs) > 0 || len(events) > 0 {
		t.Fatalf("later frame replayed records below the reconnect cursors: %v, %v", logs, events)
	}
	appendRecords(6)
	logs, events := readFrame()
	if len(logs) != 1 || logs[0].Sequence != 6 || len(events) != 1 || events[0].Sequence != 6 {
		t.Fatalf("later frame missed new records: %v, %v", logs, events)
	}
}
