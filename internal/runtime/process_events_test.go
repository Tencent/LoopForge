package runtime

import (
	"os/exec"
	"testing"

	"agentflow/internal/domain"
)

// The sink is not goroutine-safe on purpose: runCommand must serialize every
// callback, including process.started, against the output copy goroutines.
func TestRunCommandSerializesStartedBeforeOutput(t *testing.T) {
	for i := 0; i < 20; i++ {
		var names []string
		sink := func(e domain.TelemetryEvent) { names = append(names, e.Name) }
		if _, err := runCommand(exec.Command("sh", "-c", "echo a; echo b 1>&2; echo c"), sink); err != nil {
			t.Fatal(err)
		}
		if len(names) < 5 || names[0] != "process.started" || names[len(names)-1] != "process.finished" {
			t.Fatalf("events out of order: %v", names)
		}
	}
}
