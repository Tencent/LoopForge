package httpapi

import (
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestSharedAgentReferences(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	agents := []domain.AgentDefinition{
		{ID: "sar-shared", Name: "Shared", Version: 1, Shared: true, Rules: []string{"sar-rule-a", "sar-rule-b"}, Skills: []string{"sar-skill-a"}},
		{ID: "sar-private", Name: "Private", Version: 1, ProjectID: "sar-p", Rules: []string{"sar-rule-private"}, Skills: []string{"sar-skill-private"}},
	}
	for _, a := range agents {
		if err := s.PutAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	h := &catalogHandlers{deps: &deps{repo: s}}
	cases := []struct {
		kind, id string
		want     bool
	}{
		{"rule", "sar-rule-a", true},
		{"rule", "sar-rule-b", true},
		{"skill", "sar-skill-a", true},
		{"rule", "sar-skill-a", false},
		{"skill", "sar-rule-a", false},
		{"rule", "sar-rule-private", false},
		{"skill", "sar-skill-private", false},
		{"rule", "sar-missing", false},
	}
	for _, c := range cases {
		if got := h.sharedAgentReferences(c.kind, c.id); got != c.want {
			t.Errorf("sharedAgentReferences(%q, %q) = %v, want %v", c.kind, c.id, got, c.want)
		}
	}
}
