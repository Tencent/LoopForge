package mcpserver

import (
	"agentflow/internal/application"
	"encoding/json"
	"strings"
	"testing"
)

func TestToolContractsCoverManifest(t *testing.T) {
	for _, op := range application.PublicOperations() {
		schema := ToolInputSchema(op)
		if _, err := json.Marshal(schema); err != nil {
			t.Fatalf("%s: %v", op.Name, err)
		}
		found := false
		for path, v := range contractObject(contract["paths"]) {
			if normalizeContractPath(path) == normalizeContractPath(op.Path) && contractObject(v)[strings.ToLower(op.Method)] != nil {
				found = true
			}
		}
		if !found {
			t.Errorf("missing HTTP contract %s %s (%s)", op.Method, op.Path, op.Name)
		}
	}
}
func TestDraftContractRejectsMissingImportURLAndAcceptsRepositoryPaste(t *testing.T) {
	ops := map[string]application.Operation{}
	for _, op := range application.PublicOperations() {
		ops[op.Name] = op
	}
	if err := ValidateToolInput(ops["issues_import_tapd"], map[string]any{"body": map[string]any{"projectId": "p"}}); err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("missing URL accepted: %v", err)
	}
	if err := ValidateToolInput(ops["repositories_save"], map[string]any{"body": map[string]any{"projectId": "p", "url": "https://git.example.com/example/login"}}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateToolInput(ops["repositories_save"], map[string]any{"body": map[string]any{"projectId": "p", "name": "login"}}); err == nil {
		t.Fatal("repository without identity accepted")
	}
}

func TestPublicRepositoryToolSupportsGitHub(t *testing.T) {
	for _, op := range application.PublicOperations() {
		if op.Name != "repositories_save" {
			continue
		}
		if !strings.Contains(op.Description, "GitHub") || strings.Contains(op.Description, "Gongfeng") {
			t.Fatalf("misleading public tool description: %s", op.Description)
		}
		if err := ValidateToolInput(op, map[string]any{"body": map[string]any{
			"projectId": "p", "repoUrl": "https://github.com/Tencent/LoopForge", "provider": "github",
		}}); err != nil {
			t.Fatalf("public GitHub tool call rejected: %v", err)
		}
		return
	}
	t.Fatal("repositories_save is missing")
}
