package tracker

import (
	"encoding/json"
	"os"
	"path/filepath"

	"agentflow/internal/mcpclient"
)

// MCPClient is a Streamable HTTP MCP client used by issue/MR sync.
type MCPClient = mcpclient.Client

func NewMCPClient(url string, headers map[string]string) *MCPClient {
	return mcpclient.New(url, headers)
}

// LoadAuthFromCodeBuddy reads ~/.codebuddy/mcp.json and extracts the
// Authorization bearer token for a named MCP server. Returns "" if not found.
// CodeBuddy stores per-server OAuth tokens here; they auto-refresh on expiry.
func LoadAuthFromCodeBuddy(serverName string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".codebuddy", "mcp.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg struct {
		MCPServers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return ""
	}
	if srv, ok := cfg.MCPServers[serverName]; ok {
		return srv.Headers["Authorization"]
	}
	return ""
}
