package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestMCPServerCredentialsAreMaskedAndSurviveEdits(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	server := domain.MCPServerDefinition{
		ID: "gateway", Name: "Gateway", Transport: "http", URL: "https://gateway.example/mcp", TimeoutSec: 60,
		Headers: map[string]string{"X-Gateway-Api-Token": "gateway-secret", "X-Env": "prod", "Authorization": "Bearer ${secret:GATEWAY}"},
	}
	rec := requestWithHeaders(t, h, http.MethodPut, "/api/mcp-servers", server, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "gateway-secret") {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/mcp-servers", "/api/bootstrap"} {
		if body := requestWithHeaders(t, h, http.MethodGet, path, nil, nil).Body.String(); strings.Contains(body, "gateway-secret") {
			t.Fatalf("GET %s leaked header: %s", path, body)
		}
	}
	var listed []domain.MCPServerDefinition
	if err := json.Unmarshal(requestWithHeaders(t, h, http.MethodGet, "/api/mcp-servers", nil, nil).Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	var edited domain.MCPServerDefinition
	for _, item := range listed {
		if item.ID == "gateway" {
			edited = item
		}
	}
	if edited.Headers["X-Gateway-Api-Token"] != domain.MaskedSecretValue || edited.Headers["Authorization"] != "Bearer ${secret:GATEWAY}" {
		t.Fatalf("listed headers = %v", edited.Headers)
	}
	edited.Description = "改了描述"
	edited.Headers["X-Unknown"] = domain.MaskedSecretValue
	if rec := requestWithHeaders(t, h, http.MethodPut, "/api/mcp-servers", edited, nil); rec.Code != http.StatusOK {
		t.Fatalf("re-save = %d %s", rec.Code, rec.Body.String())
	}
	stored, _ := s.MCPServer("gateway")
	want := map[string]string{"X-Gateway-Api-Token": "gateway-secret", "X-Env": "prod", "Authorization": "Bearer ${secret:GATEWAY}"}
	if len(stored.Headers) != len(want) {
		t.Fatalf("stored headers = %v", stored.Headers)
	}
	for k, v := range want {
		if stored.Headers[k] != v {
			t.Fatalf("stored headers = %v", stored.Headers)
		}
	}
	edited.Headers["X-Gateway-Api-Token"] = "rotated"
	requestWithHeaders(t, h, http.MethodPut, "/api/mcp-servers", edited, nil)
	if stored, _ := s.MCPServer("gateway"); stored.Headers["X-Gateway-Api-Token"] != "rotated" {
		t.Fatalf("token rotation not saved: %v", stored.Headers)
	}
}
