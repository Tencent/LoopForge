package domain

import (
	"net/url"
	"strings"
	"time"
)

// KnowledgeMCPServerID is the reserved prefix/id used for proxied read-only
// knowledge MCP definitions injected into agent asset bundles.
const KnowledgeMCPServerID = "agentflow-knowledge"

// KnowledgeUsageRuleID is the built-in rule id for read-only knowledge usage
// and citation discipline.
const KnowledgeUsageRuleID = "agentflow-knowledge-usage"

// Built-in extensible connector types. Additional connectors can register new
// type strings via knowledge.Registry without changing core schemas.
const (
	ConnectorTypeWeKnora  = "weknora"
	ConnectorTypeObsidian = "obsidian"
	ConnectorTypeNotion   = "notion"
	ConnectorTypeSiYuan   = "siyuan"
	ConnectorTypeFeishu   = "feishu"
	ConnectorTypeMCP      = "mcp"
)

// IsNativeRESTConnectorType reports whether typ uses the canonical 4-tool
// native read-only REST contract (list_knowledge_bases, search_knowledge,
// list_documents, read_document).
func IsNativeRESTConnectorType(typ string) bool {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case ConnectorTypeWeKnora, ConnectorTypeObsidian, ConnectorTypeNotion, ConnectorTypeSiYuan, ConnectorTypeFeishu:
		return true
	default:
		return false
	}
}

// Credential auth types stored inside the encrypted secret payload. Reserving
// oauth + refresh metadata allows future providers (Feishu/Notion/Confluence)
// to plug in without altering database schemas.
const (
	ConnectorAuthAPIKey  = "api_key"
	ConnectorAuthBearer  = "bearer"
	ConnectorAuthHeaders = "headers"
	ConnectorAuthOAuth   = "oauth"
)

// KnowledgeConnectorConfig holds non-secret per-connector settings.
type KnowledgeConnectorConfig struct {
	TimeoutSec     int               `json:"timeoutSec,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	DefaultSources []string          `json:"defaultSources,omitempty"`
	AllowedTools   []string          `json:"allowedTools,omitempty"`
	Extra          map[string]string `json:"extra,omitempty"`
}

// KnowledgeConnectorSecret holds upstream credentials that are sealed with
// AES-256-GCM before storage and never returned to API callers or sandboxes.
type KnowledgeConnectorSecret struct {
	AuthType     string            `json:"authType,omitempty"`
	Token        string            `json:"token,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	RefreshToken string            `json:"refreshToken,omitempty"`
	ExpiresAt    string            `json:"expiresAt,omitempty"`
	// OAuth 2.0 grant metadata. Everything here is sealed with the rest of the
	// secret: the client secret and refresh token must never reach a client.
	OAuthClientID        string `json:"oauthClientId,omitempty"`
	OAuthClientSecret    string `json:"oauthClientSecret,omitempty"`
	OAuthTokenURL        string `json:"oauthTokenUrl,omitempty"`
	OAuthTokenAuthMethod string `json:"oauthTokenAuthMethod,omitempty"` // client_secret_post|client_secret_basic|none|json_body
	OAuthResource        string `json:"oauthResource,omitempty"`
}

// HasCredential reports whether any upstream credential material is present.
func (s KnowledgeConnectorSecret) HasCredential() bool {
	if strings.TrimSpace(s.Token) != "" || strings.TrimSpace(s.RefreshToken) != "" || strings.TrimSpace(s.OAuthClientSecret) != "" {
		return true
	}
	for _, v := range s.Headers {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// KnowledgeConnector is one user-owned read-only knowledge source integration.
type KnowledgeConnector struct {
	ID               string                   `json:"id"`
	OwnerUserID      int64                    `json:"ownerUserId"`
	Name             string                   `json:"name"`
	Type             string                   `json:"type"`
	Endpoint         string                   `json:"endpoint"`
	Config           KnowledgeConnectorConfig `json:"config,omitempty"`
	SecretEnc        string                   `json:"-"`
	SecretConfigured bool                     `json:"secretConfigured"`
	// OAuth fields are derived read-only projections of the sealed secret and
	// non-secret config; they never carry tokens or client secrets.
	OAuthMode                   string     `json:"oauthMode,omitempty"`
	OAuthStatus                 string     `json:"oauthStatus,omitempty"`
	OAuthExpiresAt              *time.Time `json:"oauthExpiresAt,omitempty"`
	OAuthClientSecretConfigured bool       `json:"oauthClientSecretConfigured,omitempty"`
	Enabled                     bool       `json:"enabled"`
	CreatedAt                   time.Time  `json:"createdAt,omitempty"`
	UpdatedAt                   time.Time  `json:"updatedAt,omitempty"`
}

// Public returns a client-safe copy with sealed credentials stripped and
// sensitive URL parts scrubbed.
func (c KnowledgeConnector) Public() KnowledgeConnector {
	c.SecretEnc = ""
	c.Endpoint = publicConnectorEndpoint(c.Endpoint, c.Type)
	return c
}

func publicConnectorEndpoint(raw, typ string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		if IsNativeRESTConnectorType(typ) {
			if cut := strings.IndexAny(raw, "?#"); cut >= 0 {
				return raw[:cut]
			}
		}
		return raw
	}
	u.User = nil
	if IsNativeRESTConnectorType(typ) {
		u.RawQuery, u.ForceQuery, u.Fragment = "", false, ""
	}
	return u.String()
}

// ConnectorFieldSpec declares one configuration field rendered dynamically by
// the UI when creating or editing a connector of a given type.
type ConnectorFieldSpec struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Kind        string `json:"kind"` // url | text | secret | kv | number
	Placeholder string `json:"placeholder,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Help        string `json:"help,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
}

// ConnectorTypeMeta describes a registered ConnectorFactory so frontend UIs
// adapt automatically when new connector backends are registered in Go.
type ConnectorTypeMeta struct {
	Type               string               `json:"type"`
	Name               string               `json:"name"`
	Description        string               `json:"description"`
	SupportsSubSources bool                 `json:"supportsSubSources"`
	DefaultAuthType    string               `json:"defaultAuthType,omitempty"`
	Fields             []ConnectorFieldSpec `json:"fields"`
}
