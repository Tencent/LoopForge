package httpapi

import (
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// checkpointRunningGeneration renews the lease every tick but rewrites the
// stored diagnostic payload only when the evidence changed.
func TestAssistantCheckpointRunningGenerationHeartbeatsUnchangedEvidence(t *testing.T) {
	st := cvAStore(t)
	h := &assistantHandlers{deps: &deps{repo: st}}
	const owner = 31
	if err := st.PutAssistantSession(owner, domain.AssistantSession{ID: "ckpt-session"}); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: "ckpt-turn", SessionID: "ckpt-session", UserID: owner, Status: "running", StartedAt: time.Now().UTC()}
	if err := st.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	payload := func() string {
		t.Helper()
		var raw []byte
		if err := st.DB.Table("assistant_diagnostics").Select("payload").Where("generation_id = ?", g.ID).Row().Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	leaseAt := func() time.Time {
		t.Helper()
		var at time.Time
		if err := st.DB.Table("assistant_generations").Select("updated_at").Where("generation_id = ?", g.ID).Row().Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}

	d := newAssistantDiagnosticsCollector(g)
	d.platform("cli.preparing", "first evidence", domain.User{})
	if err := h.checkpointRunningGeneration(g, d); err != nil {
		t.Fatal(err)
	}
	first := payload()
	firstLease := leaseAt()

	time.Sleep(5 * time.Millisecond)
	if err := h.checkpointRunningGeneration(g, d); err != nil {
		t.Fatal(err)
	}
	if got := payload(); got != first {
		t.Fatal("unchanged evidence rewrote the diagnostic payload")
	}
	if !leaseAt().After(firstLease) {
		t.Fatal("heartbeat did not renew the generation lease")
	}

	d.platform("cli.started", "second evidence", domain.User{})
	if err := h.checkpointRunningGeneration(g, d); err != nil {
		t.Fatal(err)
	}
	stored, ok := st.AssistantDiagnostics(owner, g.SessionID, g.ID)
	if !ok || payload() == first || len(stored.Timeline) < 2 || stored.Timeline[len(stored.Timeline)-1].Message != "second evidence" {
		t.Fatalf("changed evidence not written: %+v", stored.Timeline)
	}

	// Without a collector the checkpoint still writes (a default report).
	if err := h.checkpointRunningGeneration(g, nil); err != nil {
		t.Fatal(err)
	}
	g.Status = "completed"
	if err := h.checkpointRunningGeneration(g, d); err != nil {
		t.Fatal(err)
	}
	if err := h.checkpointRunningGeneration(g, d); err == nil {
		t.Fatal("checkpoint accepted a finished generation")
	}
}

// Generic agent CLIs read their endpoint from AgentCLICredentials; a key
// embedded in its path, userinfo or query must not reach the report.
func TestAssistantDiagnosticAgentCLIEndpointKeepsOnlySchemeAndHost(t *testing.T) {
	g := domain.AssistantGeneration{ID: "agent-cli-endpoint", SessionID: "s", Status: "running", StartedAt: time.Now().UTC()}
	d := newAssistantDiagnosticsCollector(g)
	user := domain.User{AgentCLICredentials: map[string]domain.AgentCLICredential{
		runtime.AgentCLIKimi:     {BaseURL: "https://u:pw@kimi-gateway.example/v1/path-secret-value?key=q#f", Protocol: "openai"},
		runtime.AgentCLIDeepSeek: {BaseURL: "not a url"},
	}}
	a := d.begin(runtime.AgentCLIKimi, "model", false, user)
	a.configure("/nonexistent/kimi", assistantCLIRequest{Model: runtime.AgentCLIKimi + ":model"})
	b := d.begin(runtime.AgentCLIDeepSeek, "model", false, user)
	b.configure("/nonexistent/dsh", assistantCLIRequest{Model: runtime.AgentCLIDeepSeek + ":model"})
	report := d.snapshot(g)
	if len(report.Attempts) != 2 {
		t.Fatalf("attempts = %+v", report.Attempts)
	}
	if got := report.Attempts[0]; got.Endpoint != "https://kimi-gateway.example" || got.Protocol != "openai" {
		t.Fatalf("agent CLI endpoint kept path or credentials: %+v", got)
	}
	// A value without a host is not a URL and is reported as configured.
	if got := report.Attempts[1].Endpoint; got != "not a url" {
		t.Fatalf("hostless endpoint = %q", got)
	}
}
