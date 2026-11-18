package runtime

import (
	"fmt"
	"sort"
	"strings"

	"agentflow/internal/domain"
)

func cloneHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// MCPAuthCredential is the MCPServerDefinition.AuthType value that binds a
// platform-managed credential as the server's Authorization Bearer header.
// The credential itself is named by MCPServerDefinition.CredentialKind and
// resolved from RunCredentials.Extra per run, so a stored asset never holds
// the secret.
const MCPAuthCredential = "credential"

// BindCredentialMCPHeaders binds the responsible user's saved credential
// (named by CredentialKind) into HTTP MCP servers that opt in with
// AuthType "credential". Stored MCP assets are never mutated: the bundle is
// copied per task, and any Authorization header stored on the asset is
// replaced so the credential is the only authority.
//
// required controls what happens when the responsible user has not saved the
// declared credential: required fails the whole binding with the server and
// kind named (agent invocations, assistant calls); otherwise the server is
// dropped and named in the returned list so the remaining servers keep
// working (opportunistic script bindings, mirroring github-token). Filtering
// keeps entries by identity during the walk — display names are not unique
// (a project and a personal server may share one), so a same-named server
// that needs no credential or has one never drops alongside its twin.
func BindCredentialMCPHeaders(assets domain.AgentAssetBundle, extra map[string]string, required bool) (domain.AgentAssetBundle, []string, error) {
	out := assets
	out.MCPServers = append([]domain.MCPServerDefinition(nil), assets.MCPServers...)
	kept := out.MCPServers[:0]
	var dropped []string
	for i := range out.MCPServers {
		server := out.MCPServers[i]
		if !strings.EqualFold(strings.TrimSpace(server.AuthType), MCPAuthCredential) {
			kept = append(kept, server)
			continue
		}
		kind := strings.TrimSpace(server.CredentialKind)
		if kind == "" {
			return out, nil, fmt.Errorf("MCP %s declares credential authentication without a credentialKind", server.Name)
		}
		token := strings.TrimSpace(extra[kind])
		if token == "" {
			if required {
				return out, nil, fmt.Errorf("MCP %s requires the saved credential %q; configure it in personal settings", server.Name, kind)
			}
			dropped = append(dropped, server.Name)
			continue
		}
		server.Headers = cloneHeaders(server.Headers)
		for key := range server.Headers {
			if strings.EqualFold(strings.TrimSpace(key), "authorization") {
				delete(server.Headers, key)
			}
		}
		server.Headers["Authorization"] = "Bearer " + token
		kept = append(kept, server)
	}
	out.MCPServers = kept
	sort.Strings(dropped)
	return out, dropped, nil
}

// RequiresMCPCredential reports whether any server needs a saved credential.
func RequiresMCPCredential(assets domain.AgentAssetBundle) bool {
	for _, server := range assets.MCPServers {
		if strings.EqualFold(strings.TrimSpace(server.AuthType), MCPAuthCredential) {
			return true
		}
	}
	return false
}
