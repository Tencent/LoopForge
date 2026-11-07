package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPCallCLIStdioToolsCallsAndLists(t *testing.T) {
	root := t.TempDir()
	server := filepath.Join(root, "mcp-server")
	script := `#!/bin/sh
IFS= read -r init
printf '%s\n' '{"jsonrpc":"2.0","id":99,"result":{}}'
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{}}'
IFS= read -r notification
IFS= read -r request
case "$request" in
  *tools/list*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"read"},{"name":""},{"name":"write"}]}}' ;;
  *) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"done"},{"type":"image","text":"ignored"}],"isError":false}}' ;;
esac
`
	if err := os.WriteFile(server, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "mcp.json")
	cfg := mcpCallConfigFile{Servers: []mcpCallServer{
		{ID: "mcp-local-id", Name: "Local Server", MarketID: "market", Command: server, Transport: "stdio", Aliases: []string{"local"}, Environment: map[string]string{"CVR": "1"}},
		{ID: "http-one", Name: "Remote", URL: "http://127.0.0.1:1", Transport: "http", AuthType: "github-token"},
	}}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(mcpCallConfigEnv, cfgPath)
	loaded, err := loadMCPCallConfig()
	if err != nil || len(loaded.Servers) != 2 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if mcpCallMain([]string{"list"}) != 0 {
		t.Fatal("list failed")
	}
	if mcpCallMain([]string{"tools", "local"}) != 0 {
		t.Fatal("tools failed")
	}
	if mcpCallMain([]string{"call", "Local Server", "read", `{"path":"x"}`}) != 0 {
		t.Fatal("call failed")
	}
	if code := mcpCallMain([]string{"call", "local", "read", `[]`}); code != 1 {
		t.Fatalf("non-object args code=%d", code)
	}
	if code := mcpCallMain([]string{"call", "Remote", "read"}); code != 1 {
		t.Fatalf("missing PAT code=%d", code)
	}
	for _, args := range [][]string{nil, {"bad"}, {"tools"}, {"call"}} {
		if mcpCallMain(args) == 0 {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
}

func TestMCPCallMatchingAliasesAndFailures(t *testing.T) {
	servers := []mcpCallServer{
		{ID: "one", Name: "Same", MarketID: "m1", Aliases: []string{"alias"}},
		{ID: "two", Name: "Same", MarketID: "m2", Aliases: []string{"other"}},
	}
	if _, err := matchMCPCallServer(servers, ""); err == nil {
		t.Fatal("empty query accepted")
	}
	if got, err := matchMCPCallServer(servers, "alias"); err != nil || got.ID != "one" {
		t.Fatalf("match=%+v err=%v", got, err)
	}
	if _, err := matchMCPCallServer(servers, "Same"); err == nil || !strings.Contains(err.Error(), "多个") {
		t.Fatalf("ambiguous err=%v", err)
	}
	if _, err := matchMCPCallServer(servers, "absent"); err == nil {
		t.Fatal("absent query accepted")
	}
	if !mcpCallServerMatches(servers[0], "m1") || mcpCallServerMatches(servers[0], "none") {
		t.Fatal("server matching")
	}
	if authorizationHeader(map[string]string{" Authorization ": " token "}) != "token" || authorizationHeader(nil) != "" {
		t.Fatal("authorization lookup")
	}
	t.Setenv(mcpCallConfigEnv, "")
	if _, err := loadMCPCallConfig(); err == nil {
		t.Fatal("missing config accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte("{"), 0o600)
	t.Setenv(mcpCallConfigEnv, bad)
	if _, err := loadMCPCallConfig(); err == nil {
		t.Fatal("invalid config accepted")
	}
}

func TestMCPStdioRawAndErrorResponses(t *testing.T) {
	root := t.TempDir()
	server := filepath.Join(root, "server")
	cvRWriteMCPServer := func(final string) {
		t.Helper()
		script := "#!/bin/sh\nread a\necho '{\"id\":1,\"result\":{}}'\nread b\nread c\necho '" + final + "'\n"
		if err := os.WriteFile(server, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := mcpCallServer{Name: "fake", Command: server, Transport: "stdio", TimeoutSec: 2}
	cvRWriteMCPServer(`{"id":2,"result":{"content":[{"type":"text","text":"bad"}],"isError":true}}`)
	if _, err := stdioMCPCall(s, "x", map[string]any{}); err == nil {
		t.Fatal("tool error accepted")
	}
	cvRWriteMCPServer(`{"id":2,"result":"raw"}`)
	if got, err := stdioMCPCall(s, "x", map[string]any{}); err != nil || got != `"raw"` {
		t.Fatalf("raw=%q err=%v", got, err)
	}
	cvRWriteMCPServer(`{"id":2,"error":{"code":-1,"message":"failure"}}`)
	if _, err := runStdioMCP(s, "tools/list", map[string]any{}); err == nil {
		t.Fatal("RPC error accepted")
	}
	s.Command = filepath.Join(root, "missing")
	if _, err := runStdioMCP(s, "tools/list", nil); err == nil {
		t.Fatal("missing executable accepted")
	}
}
