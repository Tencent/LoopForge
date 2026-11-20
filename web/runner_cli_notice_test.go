package web

import (
	"bytes"
	"os"
	"testing"
)

func TestRunnerUsesPerCLIMaintenanceControls(t *testing.T) {
	page, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(page, []byte("runnerCLIUpdateNotice")) {
		t.Fatal("update guidance belongs to the CLI update action")
	}
	app, err := os.ReadFile("app/runners.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"data-runner-runtime-menu", "data-runner-check-update", "AgentFlowAssistantModels?.cliIcon(provider)", "runtime.version||runtime.binaryPath"} {
		if !bytes.Contains(app, []byte(want)) {
			t.Errorf("missing runner card control or status: %s", want)
		}
	}
}
