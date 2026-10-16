package domain

import "strings"

// MaskedSecretValue replaces stored MCP credentials in API responses. Clients
// send it back unchanged to keep the stored value.
const MaskedSecretValue = "******"

var sensitiveEnvironmentKeyParts = []string{
	"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "ACCESS_KEY", "PRIVATE_KEY",
	"AUTH", "COOKIE", "CREDENTIAL", "SESSION",
}

// PublicMCPServer masks every literal header value and environment values
// whose key names a credential. ${secret:NAME} references are shown as-is.
func PublicMCPServer(server MCPServerDefinition) MCPServerDefinition {
	server.Headers = maskMCPValues(server.Headers, func(string) bool { return true })
	server.Environment = maskMCPValues(server.Environment, sensitiveEnvironmentKey)
	return server
}

func PublicMCPServers(servers []MCPServerDefinition) []MCPServerDefinition {
	out := make([]MCPServerDefinition, len(servers))
	for i, server := range servers {
		out[i] = PublicMCPServer(server)
	}
	return out
}

// RestoreMaskedMCPSecrets puts stored values back where an edit echoed the
// mask. A masked value with no stored counterpart is dropped rather than saved.
func RestoreMaskedMCPSecrets(incoming *MCPServerDefinition, existing MCPServerDefinition) {
	incoming.Headers = restoreMaskedValues(incoming.Headers, existing.Headers)
	incoming.Environment = restoreMaskedValues(incoming.Environment, existing.Environment)
}

func maskMCPValues(values map[string]string, sensitive func(string) bool) map[string]string {
	if len(values) == 0 {
		return values
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		if value != "" && sensitive(key) && !strings.Contains(value, "${secret:") {
			value = MaskedSecretValue
		}
		out[key] = value
	}
	return out
}

func restoreMaskedValues(incoming, stored map[string]string) map[string]string {
	for key, value := range incoming {
		if value != MaskedSecretValue {
			continue
		}
		if original, ok := stored[key]; ok {
			incoming[key] = original
		} else {
			delete(incoming, key)
		}
	}
	return incoming
}

func sensitiveEnvironmentKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, part := range sensitiveEnvironmentKeyParts {
		if strings.Contains(upper, part) {
			return true
		}
	}
	return false
}
