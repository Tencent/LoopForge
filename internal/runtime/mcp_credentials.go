package runtime

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

const MCPAuthTaihuPAT = "taihu-pat"

// BindPersonalMCPHeaders returns a task-private copy of the asset bundle and
// injects the responsible user's Taihu PAT only into MCPs that explicitly opt
// in to Taihu authentication. Stored MCP assets are never mutated.
func BindPersonalMCPHeaders(assets domain.AgentAssetBundle, taihuPAT string) (domain.AgentAssetBundle, error) {
	out := assets
	out.MCPServers = append([]domain.MCPServerDefinition(nil), assets.MCPServers...)
	for i := range out.MCPServers {
		m := &out.MCPServers[i]
		if strings.TrimSpace(m.AuthType) != MCPAuthTaihuPAT {
			continue
		}
		token := strings.TrimSpace(taihuPAT)
		if token == "" {
			return domain.AgentAssetBundle{}, fmt.Errorf("MCP %s 需要太湖个人令牌：请先在个人凭证中配置", m.Name)
		}
		m.Headers = cloneHeaders(m.Headers)
		for key := range m.Headers {
			if strings.EqualFold(strings.TrimSpace(key), "authorization") {
				delete(m.Headers, key)
			}
		}
		m.Headers["Authorization"] = "Bearer " + token
	}
	return out, nil
}

func RequiresTaihuPAT(assets domain.AgentAssetBundle) bool {
	for _, m := range assets.MCPServers {
		if strings.TrimSpace(m.AuthType) == MCPAuthTaihuPAT {
			return true
		}
	}
	return false
}

func cloneHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}
