package engine

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/forge"
)

func TestForgejoNodeMCPScopesTheNodeAttempt(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "forgejo-node-capability-test")
	e, _ := cvEEngine(t)
	e.ConfigurePlatformMCP("https://platform.example.org", "")
	run := domain.Run{ID: "r", Generation: 2, ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 7}, Nodes: map[string]*domain.NodeRun{
		"mr": {Status: "running", Attempt: 3},
	}}
	server, err := e.forgeNodeMCP(run, "mr")
	if err != nil || server.ID != domain.ForgeMCPServerID || server.URL != "https://platform.example.org"+forge.RunMCPPath {
		t.Fatalf("Forgejo node MCP = %+v, %v", server, err)
	}
	grant, err := forge.VerifyRunGrant(strings.TrimPrefix(server.Headers["Authorization"], "Bearer "), time.Now())
	if err != nil || grant.RunID != run.ID || grant.NodeID != "mr" || grant.UserID != 7 || grant.Generation != 2 || grant.Attempt != 3 {
		t.Fatalf("node capability = %+v, %v", grant, err)
	}
	run.Nodes["mr"].Status = "succeeded"
	if _, err := e.forgeNodeMCP(run, "mr"); err == nil {
		t.Fatal("an ended node received a new capability")
	}
}
