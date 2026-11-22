package domain

import (
	"strings"
	"testing"
)

func TestKnowledgeSettingsPublicStripsWeKnoraQuery(t *testing.T) {
	settings := KnowledgeSettings{
		Provider: KnowledgeProviderWeKnora,
		URL:      "https://weknora.example.com/api/v1?api_key=top-secret#frag",
		Token:    "token-value",
		Connections: []KnowledgeConnection{
			{ID: "wk-1", Provider: KnowledgeProviderWeKnora, URL: "https://weknora.example.com/?token=conn-secret", Token: "conn-token"},
			{ID: "lc-1", Provider: KnowledgeProviderLightCloud, URL: "https://mcp.example.com/kb?tenant=1"},
		},
	}

	public := settings.Public()
	if strings.Contains(public.URL, "secret") || strings.Contains(public.URL, "?") {
		t.Fatalf("top-level URL still leaks a query: %q", public.URL)
	}
	if public.URL != "https://weknora.example.com/api/v1" {
		t.Fatalf("top-level URL = %q", public.URL)
	}
	if public.Token != "" || !public.TokenConfigured {
		t.Fatalf("token must be cleared and flagged, got %q/%v", public.Token, public.TokenConfigured)
	}
	if strings.Contains(public.Connections[0].URL, "secret") {
		t.Fatalf("connection URL still leaks a query: %q", public.Connections[0].URL)
	}
	if public.Connections[0].Token != "" || !public.Connections[0].TokenConfigured {
		t.Fatalf("connection token must be cleared and flagged")
	}
	// Other providers may legitimately carry a query; Public must not rewrite it.
	if public.Connections[1].URL != "https://mcp.example.com/kb?tenant=1" {
		t.Fatalf("lightcloud URL was rewritten: %q", public.Connections[1].URL)
	}
}

// A connection row saved before per-connection providers existed inherits the
// top-level provider; redaction must judge it by that inherited provider.
func TestKnowledgeSettingsPublicInheritsTopLevelProvider(t *testing.T) {
	settings := KnowledgeSettings{
		Provider: KnowledgeProviderWeKnora,
		URL:      "https://weknora.example.com",
		Connections: []KnowledgeConnection{
			{ID: "wk-legacy", URL: "https://weknora.example.com/api?api_key=conn-secret"},
			{ID: "lc-explicit", Provider: KnowledgeProviderLightCloud, URL: "https://mcp.example.com/kb?tenant=1"},
		},
	}
	public := settings.Public()
	if strings.Contains(public.Connections[0].URL, "conn-secret") || public.Connections[0].URL != "https://weknora.example.com/api" {
		t.Fatalf("inherited WeKnora connection leaked its query: %q", public.Connections[0].URL)
	}
	if public.Connections[1].URL != "https://mcp.example.com/kb?tenant=1" {
		t.Fatalf("explicit lightcloud URL was rewritten: %q", public.Connections[1].URL)
	}
}
