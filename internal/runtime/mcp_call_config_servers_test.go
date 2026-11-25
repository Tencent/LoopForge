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

func TestMCPCallConfigServersCarriesIdentityEnvironmentAndWorkingDirectory(t *testing.T) {
	t.Setenv("LOOPFORGE_TEST_MCP_CALL_TOKEN", "tok-123")
	servers := []domain.MCPServerDefinition{
		{
			ID:               "mcp-github-1",
			Name:             "GitHub",
			MarketID:         "market-github",
			Command:          "/bin/github-mcp",
			WorkingDirectory: "tools/../srv",
			Environment: map[string]string{
				"PLAIN":   "value",
				"TOKEN":   "Bearer ${secret:LOOPFORGE_TEST_MCP_CALL_TOKEN}",
				"MISSING": "${secret:LOOPFORGE_TEST_MCP_CALL_UNSET}",
			},
		},
		{
			ID:               "Files.Server",
			Command:          "bin/files-mcp",
			WorkingDirectory: "data",
			FileRefs:         []domain.MCPFileRef{{Path: "bin/files-mcp"}},
		},
	}
	got := mcpCallConfigServers(servers)
	if len(got) != 2 {
		t.Fatalf("servers = %+v, want 2 entries", got)
	}
	gh, files := got[0], got[1]
	if gh.Name != "GitHub" || gh.MarketID != "market-github" {
		t.Fatalf("identity = name %q market %q, want GitHub / market-github", gh.Name, gh.MarketID)
	}
	if gh.CWD != "srv" {
		t.Fatalf("cwd = %q, want cleaned working directory srv", gh.CWD)
	}
	if len(gh.Environment) != 2 || gh.Environment["PLAIN"] != "value" || gh.Environment["TOKEN"] != "Bearer tok-123" {
		t.Fatalf("environment = %v, want PLAIN and resolved TOKEN only", gh.Environment)
	}
	if _, ok := gh.Environment["MISSING"]; ok {
		t.Fatalf("unresolved secret leaked into environment: %v", gh.Environment)
	}
	wantAliases := []string{"mcp-github-1", "market-github", "GitHub"}
	if len(gh.Aliases) < len(wantAliases) {
		t.Fatalf("aliases = %v, want prefix %v", gh.Aliases, wantAliases)
	}
	for i, alias := range wantAliases {
		if gh.Aliases[i] != alias {
			t.Fatalf("aliases = %v, want prefix %v", gh.Aliases, wantAliases)
		}
	}
	if files.CWD != ".agentflow/mcp/filesserver/data" {
		t.Fatalf("file-backed cwd = %q, want .agentflow/mcp/filesserver/data", files.CWD)
	}
	if len(files.Environment) != 0 {
		t.Fatalf("environment without definitions = %v, want none", files.Environment)
	}
}
