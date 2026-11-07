package runtime

import (
	"agentflow/internal/domain"
	"fmt"
	"net/url"
	"strings"
)

// BindGitHubMCPHeaders binds the actor's token only to GitHub's official MCP
// endpoint. An arbitrary custom server cannot opt in to receive Git tokens.
func BindGitHubMCPHeaders(assets domain.AgentAssetBundle, token string, required bool) (domain.AgentAssetBundle, error) {
	out := assets
	out.MCPServers = append([]domain.MCPServerDefinition(nil), assets.MCPServers...)
	for i := range out.MCPServers {
		server := &out.MCPServers[i]
		if server.AuthType != "github-token" {
			continue
		}
		u, err := url.Parse(server.URL)
		if err != nil || u.Scheme != "https" || u.Host != "api.githubcopilot.com" || u.User != nil {
			return out, fmt.Errorf("GitHub token authentication requires the official HTTPS MCP endpoint")
		}
		server.Headers = cloneHeaders(server.Headers)
		for key := range server.Headers {
			if strings.EqualFold(key, "Authorization") {
				delete(server.Headers, key)
			}
		}
		if strings.TrimSpace(token) == "" {
			if required {
				return out, fmt.Errorf("GitHub MCP requires the responsible user's Git token")
			}
			continue
		}
		server.Headers["Authorization"] = "Bearer " + strings.TrimSpace(token)
	}
	return out, nil
}
