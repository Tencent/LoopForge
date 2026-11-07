package web

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The shared picker chrome lives in app/assistant-models.js. It must be part of
// the classic SPA bundle and load before every script that paints a model
// toggle, otherwise those pickers bind to a missing global and stay inert.
func TestAssistantModelsModuleShipped(t *testing.T) {
	names, err := appBundleNames()
	if err != nil {
		t.Fatal(err)
	}
	index := -1
	for i, name := range names {
		if name == "app/assistant-models.js" {
			index = i
		}
	}
	if index < 0 {
		t.Fatalf("index.html must load /app/assistant-models.js, got %v", names)
	}
	for _, consumer := range []string{"app/flows.js", "app/agents.js", "app/bots.js", "app/sync-schedule.js", "app/composer.js", "app/runs.js"} {
		at := -1
		for i, name := range names {
			if name == consumer {
				at = i
			}
		}
		if at < 0 {
			t.Fatalf("%s is missing from the bundle", consumer)
		}
		if at < index {
			t.Fatalf("%s loads before the model catalog module", consumer)
		}
	}
	source, err := os.ReadFile("app/assistant-models.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"window.AgentFlowAssistantModels", "renderList", "renderOptions", "paintItem", "/api/assistant/models"} {
		if !strings.Contains(string(source), want) {
			t.Fatalf("app/assistant-models.js is missing %q", want)
		}
	}
	for _, name := range []string{"flows.js", "agents.js", "bots.js", "sync-schedule.js", "composer.js", "runs.js"} {
		data, err := os.ReadFile("app/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "AgentFlowAssistantModels=") {
			t.Fatalf("app/%s redefines the shared model catalog", name)
		}
	}
}

func TestAssistantModelPickerSelection(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "assistant_models_test.js", appBundlePath(t), "app/assistant-models.js").CombinedOutput(); err != nil {
		t.Fatalf("assistant model picker: %v\n%s", err, out)
	}
}
