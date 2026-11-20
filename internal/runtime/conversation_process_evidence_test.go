package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestConversationProcessPreservesLifecycleEvidence(t *testing.T) {
	for _, tt := range []struct {
		name      string
		code      int
		startFail bool
	}{
		{name: "success"},
		{name: "nonzero exit", code: 7},
		{name: "start failure", startFail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			root := t.TempDir()
			argv := []string{"sh", "-c", "printf 'out\\n'; printf 'err\\n' >&2; exit 0", "private-argv-marker"}
			if tt.code != 0 {
				argv[2] = "printf 'out\\n'; exit 7"
			}
			if tt.startFail {
				bin := filepath.Join(t.TempDir(), "broken-cli")
				if err := os.WriteFile(bin, []byte("#!/missing-loopforge-interpreter\n"), 0700); err != nil {
					t.Fatal(err)
				}
				argv = []string{bin, "private-argv-marker"}
			}
			pool := &ConversationProcessPool{}
			started, err := pool.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "start", Command: &ConversationCommand{Argv: argv}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var events []domain.TelemetryEvent
			var after int64
			for {
				result, err := pool.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "poll", ID: started.ID, After: after}, nil)
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, result.Events...)
				after = result.Sequence
				if result.Done {
					if (result.Error != "") != (tt.startFail || tt.code != 0) {
						t.Fatalf("unexpected process error: %q", result.Error)
					}
					break
				}
			}
			first := "process.started"
			if tt.startFail {
				first = "process.start_failed"
			}
			if len(events) < 2 || events[0].Name != first || events[len(events)-1].Name != "process.finished" {
				t.Fatalf("missing or unordered process lifecycle: %+v", events)
			}
			for _, event := range events {
				if strings.Contains(event.Message, "private-argv-marker") {
					t.Fatalf("process telemetry exposed command arguments: %+v", event)
				}
			}
			if !tt.startFail {
				if pid, _ := events[0].Attributes["pid"].(int); pid <= 0 {
					t.Fatalf("missing process PID: %+v", events[0])
				}
				last := events[len(events)-1]
				if code, ok := last.Attributes["exitCode"].(int); !ok || code != tt.code {
					t.Fatalf("missing exit code %d: %+v", tt.code, last)
				}
			}
		})
	}
}

func TestConversationOutputOverflowKeepsExitEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	pool := &ConversationProcessPool{}
	start, err := pool.Handle(ctx, root, "", nil, ConversationProcessRequest{
		Operation: "start", Command: &ConversationCommand{Argv: []string{"sh", "-c", "awk 'BEGIN { for (i=0; i<32769; i++) print \"line\" }'"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool.mu.Lock()
	finished := pool.entries[start.ID].finished
	pool.mu.Unlock()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var after int64
	lines, exits := 0, 0
	for {
		result, err := pool.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "poll", ID: start.ID, After: after}, nil)
		if err != nil || result.Error == "" {
			t.Fatalf("overflow must remain an error: error=%q err=%v", result.Error, err)
		}
		for _, event := range result.Events {
			if event.Name == "process.stdout" || event.Name == "process.stderr" {
				lines++
			}
			if event.Name == "process.finished" {
				exits++
			}
		}
		after = result.Sequence
		if result.Done {
			if lines != 32768 || exits != 1 || after > 32771 {
				t.Fatalf("overflow lost output quota or exit evidence: lines=%d exits=%d events=%d", lines, exits, after)
			}
			return
		}
	}
}
