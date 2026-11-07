package httpapi

import (
	"agentflow/internal/application"
	"encoding/json"
	"strings"
	"testing"
)

// assistantDraftScope pins how a draft of every platform write tool is scoped
// when the chat belongs to a project. "global" resources belong to no project
// and must never carry the chat's project (a new project drafted in a
// project chat was shown under that project); "project" resources get the
// chat's project when the model leaves it out; "none" bodies are left alone.
// A new *_save tool fails TestAssistantDraftScopeCoversEveryWriteTool until it
// is classified here and, if global, in assistantGlobalSaveTool and the
// chatbot's GLOBAL_SAVE_TOOLS.
var assistantDraftScope = map[string]string{
	"projects_save":             "global",
	"business_domains_save":     "global",
	"sync_config_save":          "global",
	"repositories_save":         "project",
	"issues_save":               "project",
	"issues_clarification_save": "project",
	"agents_save":               "project",
	"rules_save":                "project",
	"skills_save":               "project",
	"mcp_servers_save":          "project",
	"dockerfiles_save":          "project",
	"environments_save":         "project",
	"sandboxes_save":            "project",
	"workflows_save":            "project",
	"workflows_clone":           "project",
	"schedule_jobs_save":        "project",
	"assistant_tasks_save":      "none",
}

func TestAssistantDraftScopeCoversEveryWriteTool(t *testing.T) {
	seen := map[string]bool{}
	for _, op := range application.PublicOperations() {
		if op.ReadOnly || (!strings.HasSuffix(op.Name, "_save") && op.Name != "workflows_clone") {
			continue
		}
		seen[op.Name] = true
		scope, ok := assistantDraftScope[op.Name]
		if !ok {
			t.Errorf("%s: unclassified write tool; decide whether its drafts belong to the chat's project", op.Name)
			continue
		}
		if got := assistantGlobalSaveTool(op.Name); got != (scope == "global") {
			t.Errorf("%s: assistantGlobalSaveTool = %v, scope %s", op.Name, got, scope)
		}
		for _, body := range []string{`{"id":"x","name":"X"}`, `{"id":"x","name":"X","projectId":"other"}`, `{"id":"x","name":"X","projectId":""}`} {
			got := map[string]any{}
			if err := json.Unmarshal(injectAssistantDraftProjectID(op.Name, json.RawMessage(body), "chat-project"), &got); err != nil {
				t.Fatalf("%s %s: %v", op.Name, body, err)
			}
			pid, has := got["projectId"]
			var in map[string]any
			_ = json.Unmarshal([]byte(body), &in)
			switch scope {
			case "global":
				if has {
					t.Errorf("%s %s: global resource kept projectId %v", op.Name, body, pid)
				}
			case "project":
				want := in["projectId"]
				if want == nil || want == "" {
					want = "chat-project"
				}
				if pid != want {
					t.Errorf("%s %s: projectId = %v, want %v", op.Name, body, pid, want)
				}
			case "none":
				if pid != in["projectId"] {
					t.Errorf("%s %s: body changed: %v", op.Name, body, got)
				}
			}
			if got["id"] != "x" || got["name"] != "X" {
				t.Errorf("%s %s: id/name lost: %v", op.Name, body, got)
			}
		}
	}
	for name := range assistantDraftScope {
		if !seen[name] {
			t.Errorf("%s: classified but no longer a public write tool", name)
		}
	}
	// A chat without a project injects nothing.
	if got := injectAssistantDraftProjectID("agents_save", json.RawMessage(`{"id":"a","name":"A"}`), ""); strings.Contains(string(got), "projectId") {
		t.Fatalf("no-project chat injected a project: %s", got)
	}
}
