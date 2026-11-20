package runtime

import (
	"net/url"
	"strings"

	"agentflow/internal/domain"
)

// MCPTarget describes where an invocation's MCP servers will be called from.
type MCPTarget struct {
	// ServerURL is how that place reaches the platform. Execution machines
	// pass their own server URL; cloud runs leave it empty and keep whatever
	// absolute URL the engine resolved.
	ServerURL          string
	GitHTTPCredentials map[string]domain.GitHTTPExecutionCredential
	// ExtraCredentials carries the responsible user's platform-managed
	// credentials (RunCredentials.Extra) for MCP servers authenticated with
	// AuthType "credential".
	ExtraCredentials map[string]string
	// RequireCredentials fails when a credential-authenticated MCP has no
	// token. Agent invocations require it; scripts and gates bind
	// opportunistically.
	RequireCredentials bool
	// SSH-only workflows can deliver branches without GitHub API access.
	SSHRepositoriesOnly bool
}

// PrepareMCPServers is the single place that turns a resolved asset bundle
// into what an invocation may actually call: platform-served servers are
// addressed for the target, then personal credentials are bound. The
// returned warnings name servers that had to be dropped.
func PrepareMCPServers(assets domain.AgentAssetBundle, target MCPTarget) (domain.AgentAssetBundle, []string, error) {
	var warnings []string
	githubToken := ""
	if credential := target.GitHTTPCredentials["github.com"]; credential.Token != "" {
		githubToken = credential.Token
	}
	if target.SSHRepositoriesOnly && githubToken == "" {
		servers := assets.MCPServers
		assets.MCPServers = nil
		for _, server := range servers {
			if server.AuthType == "github-token" {
				warnings = append(warnings, "Git SSH 仓库可直接推送分支；未配置 GitHub Token，已跳过 "+server.ID)
				continue
			}
			assets.MCPServers = append(assets.MCPServers, server)
		}
	}
	var githubErr error
	assets, githubErr = BindGitHubMCPHeaders(assets, githubToken, target.RequireCredentials)
	if githubErr != nil {
		return assets, nil, githubErr
	}
	assets, credDropped, credErr := BindCredentialMCPHeaders(assets, target.ExtraCredentials, target.RequireCredentials)
	if credErr != nil {
		return assets, nil, credErr
	}
	for _, name := range credDropped {
		warnings = append(warnings, "未配置所需凭证，已跳过 "+name)
	}
	if base := strings.TrimRight(strings.TrimSpace(target.ServerURL), "/"); base != "" {
		assets.MCPServers = rebasePlatformMCP(assets.MCPServers, base)
	} else {
		var dropped []string
		assets.MCPServers, dropped = dropUnreachablePlatformMCP(assets.MCPServers)
		for _, id := range dropped {
			warnings = append(warnings, "未注入 "+id+"：当前执行实例未配置平台地址（AGENTFLOW_PLATFORM_MCP_URL 或 AGENTFLOW_API_URL，需为沙箱可访问的地址）")
		}
	}
	return assets, warnings, nil
}

// IsPlatformMCP reports whether the server is served by the AgentFlow
// deployment itself rather than a third party. Its URL is only meaningful
// relative to the address the execution side uses to reach the platform.
func IsPlatformMCP(id string) bool {
	return id == domain.PlatformMCPServerID || id == domain.KnowledgeMCPServerID
}

func rebasePlatformMCP(servers []domain.MCPServerDefinition, base string) []domain.MCPServerDefinition {
	out := make([]domain.MCPServerDefinition, len(servers))
	for i, server := range servers {
		if IsPlatformMCP(server.ID) {
			server.URL = base + platformMCPPath(server.URL)
		}
		out[i] = server
	}
	return out
}

func dropUnreachablePlatformMCP(servers []domain.MCPServerDefinition) (kept []domain.MCPServerDefinition, dropped []string) {
	for _, server := range servers {
		if IsPlatformMCP(server.ID) && !strings.Contains(server.URL, "://") {
			dropped = append(dropped, server.ID)
			continue
		}
		kept = append(kept, server)
	}
	return kept, dropped
}

func platformMCPPath(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		if u.RawQuery != "" {
			return u.Path + "?" + u.RawQuery
		}
		return u.Path
	}
	return raw
}
