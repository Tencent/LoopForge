package engine

import (
	"testing"

	"agentflow/internal/domain"
)

func TestRoleMCPServerVisible(t *testing.T) {
	cases := []struct {
		name   string
		server domain.MCPServerDefinition
		want   bool
	}{
		{"no scope", domain.MCPServerDefinition{}, false},
		{"project same", domain.MCPServerDefinition{Scope: "project", ProjectID: "p1"}, true},
		{"project other", domain.MCPServerDefinition{Scope: "project", ProjectID: "p2"}, false},
		{"personal own", domain.MCPServerDefinition{Scope: "personal", OwnerUserID: 7}, true},
		{"personal other user", domain.MCPServerDefinition{Scope: "personal", OwnerUserID: 8}, false},
		{"personal market", domain.MCPServerDefinition{Scope: "personal", OwnerUserID: 7, MarketID: "m1"}, false},
		{"other scope", domain.MCPServerDefinition{Scope: "shared", ProjectID: "p2", OwnerUserID: 8, MarketID: "m1"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := roleMCPServerVisible(tc.server, "p1", 7); got != tc.want {
				t.Fatalf("roleMCPServerVisible() = %v, want %v", got, tc.want)
			}
		})
	}
}
