package runtime

import (
	"encoding/json"
	"testing"

	"agentflow/internal/domain"
)

func TestMCPCallConfigServersNormalizesTransportAndSkipsIncomplete(t *testing.T) {
	servers := []domain.MCPServerDefinition{
		{ID: "stdio-default", Name: "Local", Command: "/bin/mcp", Args: []string{"--serve"}},
		{ID: "http-upper", Name: "Remote", Transport: " HTTP ", URL: "https://mcp.example.com/mcp", AuthType: "bearer", Headers: map[string]string{"X-Team": "a"}, TimeoutSec: 30},
		{ID: "http-missing-url", Transport: "http", URL: "  "},
		{ID: "stdio-missing-command", Transport: "stdio", Command: " "},
	}
	got := mcpCallConfigServers(servers)
	if len(got) != 2 {
		t.Fatalf("servers = %+v, want 2 entries", got)
	}
	local, remote := got[0], got[1]
	if local.ID != "stdio-default" || local.Transport != "stdio" || local.Command != "/bin/mcp" || len(local.Args) != 1 || local.Args[0] != "--serve" {
		t.Fatalf("stdio server = %+v", local)
	}
	if remote.ID != "http-upper" || remote.Transport != "http" || remote.URL != "https://mcp.example.com/mcp" || remote.AuthType != "bearer" || remote.TimeoutSec != 30 || remote.Headers["X-Team"] != "a" {
		t.Fatalf("http server = %+v", remote)
	}
	if len(remote.Aliases) == 0 || remote.Aliases[0] != "http-upper" {
		t.Fatalf("http aliases = %v, want id first", remote.Aliases)
	}

	servers[0].Args[0] = "mutated"
	servers[1].Headers["X-Team"] = "mutated"
	if local.Args[0] != "--serve" || remote.Headers["X-Team"] != "a" {
		t.Fatalf("config entries share slices/maps with source definitions: args=%v headers=%v", local.Args, remote.Headers)
	}
}

func TestMCPCallConfigServersEmptyMarshalsAsArray(t *testing.T) {
	raw, err := json.Marshal(mcpCallConfigFile{Servers: mcpCallConfigServers(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"servers":[]}` {
		t.Fatalf("config = %s, want empty servers array", raw)
	}
}
