package runtime

import (
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

func TestCodexMCPServerArgsHTTP(t *testing.T) {
	got := codexMCPServerArgs(domain.MCPServerDefinition{ID: "docs", Transport: "http", URL: "https://mcp.example/x", TimeoutSec: 30})
	want := []string{
		"-c", `mcp_servers.docs.url="https://mcp.example/x"`,
		"-c", "mcp_servers.docs.tool_timeout_sec=30.0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestCodexMCPServerArgsStdio(t *testing.T) {
	got := codexMCPServerArgs(domain.MCPServerDefinition{
		ID:               "fs",
		Transport:        "stdio",
		Command:          "node",
		Args:             []string{"server.js", "--root", "a b"},
		WorkingDirectory: "tools/./fs",
		Environment:      map[string]string{"ZED": "1", "ALPHA": `say "hi"`},
	})
	want := []string{
		"-c", `mcp_servers.fs.command="node"`,
		"-c", `mcp_servers.fs.cwd="tools/fs"`,
		"-c", `mcp_servers.fs.args=["server.js","--root","a b"]`,
		"-c", `mcp_servers.fs.env={ ALPHA = "say \"hi\"", ZED = "1" }`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestCodexMCPServerArgsMinimalStdio(t *testing.T) {
	got := codexMCPServerArgs(domain.MCPServerDefinition{ID: "bare", Command: "bin"})
	want := []string{"-c", `mcp_servers.bare.command="bin"`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestCodexConfigArgsAppendsEachMCPServer(t *testing.T) {
	servers := []domain.MCPServerDefinition{
		{ID: "docs", Transport: "http", URL: "https://mcp.example/x"},
		{ID: "fs", Command: "node", Args: []string{"s.js"}, TimeoutSec: 5},
	}
	got := codexConfigArgs("hi", nil, servers)
	want := []string{
		"-c", `developer_instructions="hi"`,
		"-c", `shell_environment_policy.inherit="core"`,
	}
	for _, m := range servers {
		want = append(want, codexMCPServerArgs(m)...)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
