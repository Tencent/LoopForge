package runtime

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func credentialServer(name, kind string, headers map[string]string) domain.MCPServerDefinition {
	return domain.MCPServerDefinition{
		ID: "srv-" + name, Name: name, Transport: "http", URL: "https://" + name + ".example",
		AuthType: MCPAuthCredential, CredentialKind: kind, Headers: headers,
	}
}

func TestBindCredentialMCPHeadersInjectsBearer(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{
		credentialServer("gateway", "corp-gateway", nil),
	}}
	out, dropped, err := BindCredentialMCPHeaders(assets, map[string]string{"corp-gateway": "tok-1"}, true)
	if err != nil {
		t.Fatalf("BindCredentialMCPHeaders: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("unexpected drops: %v", dropped)
	}
	if got := out.MCPServers[0].Headers["Authorization"]; got != "Bearer tok-1" {
		t.Fatalf("Authorization: got %q, want %q", got, "Bearer tok-1")
	}
	// The stored asset must not be mutated.
	if _, ok := assets.MCPServers[0].Headers["Authorization"]; ok {
		t.Fatal("stored MCP asset was mutated")
	}
}

func TestBindCredentialMCPHeadersReplacesStoredAuthorization(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{
		credentialServer("gateway", "corp-gateway", map[string]string{"authorization": "stale", "X-Keep": "v"}),
	}}
	out, _, err := BindCredentialMCPHeaders(assets, map[string]string{"corp-gateway": "tok-2"}, true)
	if err != nil {
		t.Fatalf("BindCredentialMCPHeaders: %v", err)
	}
	srv := out.MCPServers[0]
	if got := srv.Headers["Authorization"]; got != "Bearer tok-2" {
		t.Fatalf("Authorization: got %q, want %q", got, "Bearer tok-2")
	}
	for key := range srv.Headers {
		if strings.EqualFold(key, "authorization") && key != "Authorization" {
			t.Fatalf("stale Authorization header %q survived", key)
		}
	}
	if srv.Headers["X-Keep"] != "v" {
		t.Fatal("unrelated headers must survive")
	}
}

func TestBindCredentialMCPHeadersMissingCredentialFailsWhenRequired(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{
		credentialServer("gateway", "corp-gateway", nil),
	}}
	if _, _, err := BindCredentialMCPHeaders(assets, nil, true); err == nil {
		t.Fatal("missing credential must fail when required")
	} else if !strings.Contains(err.Error(), "corp-gateway") {
		t.Fatalf("error must name the server and credential: %v", err)
	}
}

// TestBindCredentialMCPHeadersDropsMissingWhenOptional mirrors the script
// binding semantics: one server's missing credential must not take down the
// other servers that have theirs — the same contract github-token follows.
func TestBindCredentialMCPHeadersDropsMissingWhenOptional(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{
		credentialServer("needs-cred", "absent", nil),
		credentialServer("has-cred", "corp-gateway", nil),
	}}
	out, dropped, err := BindCredentialMCPHeaders(assets, map[string]string{"corp-gateway": "tok-3"}, false)
	if err != nil {
		t.Fatalf("optional binding must not fail: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "needs-cred" {
		t.Fatalf("dropped = %v, want [needs-cred]", dropped)
	}
	if len(out.MCPServers) != 1 || out.MCPServers[0].Name != "has-cred" {
		t.Fatalf("surviving servers = %+v", out.MCPServers)
	}
	if got := out.MCPServers[0].Headers["Authorization"]; got != "Bearer tok-3" {
		t.Fatalf("has-cred Authorization: got %q", got)
	}
}

func TestBindCredentialMCPHeadersMissingKindFails(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{
		credentialServer("gateway", "  ", nil),
	}}
	if _, _, err := BindCredentialMCPHeaders(assets, map[string]string{"x": "y"}, true); err == nil {
		t.Fatal("empty credentialKind must fail")
	}
}

func TestBindCredentialMCPHeadersIgnoresOtherServers(t *testing.T) {
	plain := domain.MCPServerDefinition{ID: "p", Name: "plain", Transport: "http", URL: "https://plain.example"}
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{plain}}
	out, _, err := BindCredentialMCPHeaders(assets, nil, true)
	if err != nil {
		t.Fatalf("BindCredentialMCPHeaders: %v", err)
	}
	if out.MCPServers[0].Headers != nil {
		t.Fatal("non-credential servers must stay untouched")
	}
}

func TestPrepareMCPServersBindsCredentialAuth(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{
		credentialServer("gateway", "corp-gateway", nil),
	}}
	out, warnings, err := PrepareMCPServers(assets, MCPTarget{ExtraCredentials: map[string]string{"corp-gateway": "tok-4"}})
	if err != nil {
		t.Fatalf("PrepareMCPServers: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if got := out.MCPServers[0].Headers["Authorization"]; got != "Bearer tok-4" {
		t.Fatalf("Authorization: got %q, want %q", got, "Bearer tok-4")
	}
}

// TestPrepareMCPServersDropsMissingCredentialWhenOptional covers the script
// path (RequireCredentials unset): a missing credential drops that one server
// with a warning while the remaining credential servers stay usable.
func TestPrepareMCPServersDropsMissingCredentialWhenOptional(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{
		credentialServer("needs-cred", "absent", nil),
		credentialServer("has-cred", "corp-gateway", nil),
	}}
	out, warnings, err := PrepareMCPServers(assets, MCPTarget{ExtraCredentials: map[string]string{"corp-gateway": "tok-5"}})
	if err != nil {
		t.Fatalf("PrepareMCPServers: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "needs-cred") {
		t.Fatalf("warnings = %v, want one naming needs-cred", warnings)
	}
	if len(out.MCPServers) != 1 || out.MCPServers[0].Name != "has-cred" {
		t.Fatalf("surviving servers = %+v", out.MCPServers)
	}
	if got := out.MCPServers[0].Headers["Authorization"]; got != "Bearer tok-5" {
		t.Fatalf("has-cred Authorization: got %q", got)
	}
}

// TestBindCredentialMCPHeadersSameNameServersFilterIndependently covers the
// case where a project and a personal server share a display name: dropping
// one must never drop its same-named twin, whether the twin carries its own
// credential or needs no authentication at all.
func TestBindCredentialMCPHeadersSameNameServersFilterIndependently(t *testing.T) {
	missing := credentialServer("gateway", "absent", nil)
	missing.ID = "project-gateway"
	extra := map[string]string{"corp-gateway": "tok-6"}

	t.Run("same-named twin has its own credential", func(t *testing.T) {
		withCred := credentialServer("gateway", "corp-gateway", nil)
		withCred.ID = "personal-gateway"
		out, dropped, err := BindCredentialMCPHeaders(domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{missing, withCred}}, extra, false)
		if err != nil {
			t.Fatalf("BindCredentialMCPHeaders: %v", err)
		}
		if len(dropped) != 1 || dropped[0] != "gateway" {
			t.Fatalf("dropped = %v, want the one gateway entry", dropped)
		}
		if len(out.MCPServers) != 1 || out.MCPServers[0].ID != "personal-gateway" {
			t.Fatalf("survivors = %+v, want personal-gateway only", out.MCPServers)
		}
		if got := out.MCPServers[0].Headers["Authorization"]; got != "Bearer tok-6" {
			t.Fatalf("twin Authorization = %q, want Bearer tok-6", got)
		}
	})

	t.Run("same-named twin needs no authentication", func(t *testing.T) {
		plain := domain.MCPServerDefinition{ID: "personal-gateway", Name: "gateway", Transport: "http", URL: "https://plain.example"}
		out, dropped, err := BindCredentialMCPHeaders(domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{missing, plain}}, extra, false)
		if err != nil {
			t.Fatalf("BindCredentialMCPHeaders: %v", err)
		}
		if len(dropped) != 1 {
			t.Fatalf("dropped = %v, want one entry", dropped)
		}
		if len(out.MCPServers) != 1 || out.MCPServers[0].ID != "personal-gateway" {
			t.Fatalf("survivors = %+v, want personal-gateway only", out.MCPServers)
		}
	})
}
