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
	ServerURL string
	TaihuPAT  string
	GitToken  string
	// RequireTaihuPAT fails when a Taihu-authenticated MCP has no token. Agent
	// invocations require it; scripts and gates bind opportunistically.
	RequireTaihuPAT bool
}

// PrepareMCPServers is the single place that turns a resolved asset bundle
// into what an invocation may actually call: platform-served servers are
// addressed for the target, then personal credentials are bound. The
// returned warnings name servers that had to be dropped.
func PrepareMCPServers(assets domain.AgentAssetBundle, target MCPTarget) (domain.AgentAssetBundle, []string, error) {
	var warnings []string
	var githubErr error
	assets, githubErr = BindGitHubMCPHeaders(assets, target.GitToken, target.RequireTaihuPAT)
	if githubErr != nil {
		return assets, nil, githubErr
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
	if !target.RequireTaihuPAT {
		return BindPersonalMCPHeadersOptional(assets, target.TaihuPAT), warnings, nil
	}
	bound, err := BindPersonalMCPHeaders(assets, target.TaihuPAT)
	return bound, warnings, err
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
