package domain

import "testing"

// TestValidateMCPServerErrorOrder locks the message and precedence of every
// ValidateMCPServer branch so the transport/scope/auth helpers stay a pure
// extraction.
func TestValidateMCPServerErrorOrder(t *testing.T) {
	stdio := func(mut func(*MCPServerDefinition)) MCPServerDefinition {
		v := MCPServerDefinition{ID: "x", Name: "X", Command: "npx"}
		mut(&v)
		return v
	}
	cases := []struct {
		name string
		in   MCPServerDefinition
		want string
	}{
		{"valid stdio", stdio(func(*MCPServerDefinition) {}), ""},
		{"missing name wins over transport", MCPServerDefinition{ID: "x", Transport: "bogus"}, "MCP 需要 id 和 name"},
		{"blank id", stdio(func(v *MCPServerDefinition) { v.ID = "  " }), "MCP 需要 id 和 name"},
		{"http bad url", MCPServerDefinition{ID: "x", Name: "X", Transport: " http ", URL: "ftp://a"}, "HTTP MCP 需要有效的 url"},
		{"stdio missing command", stdio(func(v *MCPServerDefinition) { v.Command = " " }), "stdio MCP 需要 command"},
		{"install script nul", stdio(func(v *MCPServerDefinition) { v.InstallScript = "a\x00b" }), "MCP 安装脚本不能包含 NUL"},
		{"bad cwd", stdio(func(v *MCPServerDefinition) { v.WorkingDirectory = "../up" }), `workingDirectory: invalid file path "../up"`},
		{"bad path entry", stdio(func(v *MCPServerDefinition) { v.PathEntries = []string{"bin", "/abs"} }), `pathEntries: invalid file path "/abs"`},
		{"bad file ref path", stdio(func(v *MCPServerDefinition) { v.FileRefs = []MCPFileRef{{Path: "../x"}} }), `invalid file path "../x"`},
		{"file ref too large", stdio(func(v *MCPServerDefinition) { v.FileRefs = []MCPFileRef{{Path: "a.js", Size: MCPFileMaxBytes + 1}} }), "MCP file a.js exceeds the 1MB limit"},
		{"file ref negative", stdio(func(v *MCPServerDefinition) { v.FileRefs = []MCPFileRef{{Path: "a.js", Size: -1}} }), "MCP file a.js exceeds the 1MB limit"},
		{"unknown transport", stdio(func(v *MCPServerDefinition) { v.Transport = "sse" }), "transport 只能是 stdio 或 http"},
		{"transport before timeout", stdio(func(v *MCPServerDefinition) { v.Command = ""; v.TimeoutSec = 601 }), "stdio MCP 需要 command"},
		{"timeout too large", stdio(func(v *MCPServerDefinition) { v.TimeoutSec = 601 }), "timeoutSec 需在 0–600 之间"},
		{"timeout negative", stdio(func(v *MCPServerDefinition) { v.TimeoutSec = -1 }), "timeoutSec 需在 0–600 之间"},
		{"timeout before scope", stdio(func(v *MCPServerDefinition) { v.TimeoutSec = 601; v.Scope = "bogus" }), "timeoutSec 需在 0–600 之间"},
		{"project missing id", stdio(func(v *MCPServerDefinition) { v.Scope = "project" }), "项目级 MCP 需要 projectId"},
		{"project with owner", stdio(func(v *MCPServerDefinition) { v.Scope = "project"; v.ProjectID = "p"; v.OwnerUserID = 1 }), "项目级 MCP 不能设置 ownerUserId"},
		{"valid project", stdio(func(v *MCPServerDefinition) { v.Scope = " project "; v.ProjectID = "p" }), ""},
		{"personal missing owner", stdio(func(v *MCPServerDefinition) { v.Scope = "personal" }), "个人级 MCP 需要 ownerUserId"},
		{"personal with project", stdio(func(v *MCPServerDefinition) { v.Scope = "personal"; v.OwnerUserID = 1; v.ProjectID = "p" }), "个人级 MCP 不能设置 projectId"},
		{"personal market", stdio(func(v *MCPServerDefinition) { v.Scope = "personal"; v.OwnerUserID = 1; v.MarketID = "m" }), "市场 MCP 只能安装到项目，个人级 MCP 必须为自定义"},
		{"valid personal", stdio(func(v *MCPServerDefinition) { v.Scope = "personal"; v.OwnerUserID = 1 }), ""},
		{"unknown scope", stdio(func(v *MCPServerDefinition) { v.Scope = "global" }), "scope 只能为空、project 或 personal"},
		{"scope before auth", stdio(func(v *MCPServerDefinition) { v.Scope = "global"; v.AuthType = "basic" }), "scope 只能为空、project 或 personal"},
		{"unknown auth", stdio(func(v *MCPServerDefinition) { v.AuthType = "basic" }), "authType must be empty or github-token"},
		{"github token on stdio", stdio(func(v *MCPServerDefinition) { v.AuthType = "github-token" }), "GitHub 令牌认证仅支持 HTTP MCP"},
		{"github token header", MCPServerDefinition{ID: "x", Name: "X", Transport: "http", URL: "https://a", AuthType: "github-token", Headers: map[string]string{" Authorization ": "t"}}, "GitHub 令牌认证不允许在资产 Headers 中保存 Authorization"},
		{"padded github token on http", MCPServerDefinition{ID: "x", Name: "X", Transport: "http", URL: "https://a", AuthType: " github-token ", Headers: map[string]string{"Authorization": "t"}}, ""},
		{"valid github token", MCPServerDefinition{ID: "x", Name: "X", Transport: "http", URL: "https://a", AuthType: "github-token", Headers: map[string]string{"X-Other": "v"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMCPServer(tc.in)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Fatalf("ValidateMCPServer() error = %q, want %q", got, tc.want)
			}
		})
	}
}
