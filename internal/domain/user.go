package domain

import (
	"strings"
	"time"
)

// User 表示系统注册或登录的员工实体，用于后续资源归属与权限隔离。
type User struct {
	DeepSeekAPIKey             string `json:"deepSeekApiKey,omitempty"`
	DeepSeekAPIKeyConfigured   bool   `json:"deepSeekApiKeyConfigured,omitempty"`
	DeepSeekBaseURL            string `json:"deepSeekBaseUrl,omitempty"`
	CodexAPIKey                string `json:"codexApiKey,omitempty"`
	CodexAPIKeyConfigured      bool   `json:"codexApiKeyConfigured,omitempty"`
	CodexBaseURL               string `json:"codexBaseUrl,omitempty"`
	ClaudeCodeAPIKey           string `json:"claudeCodeApiKey,omitempty"`
	ClaudeCodeAPIKeyConfigured bool   `json:"claudeCodeApiKeyConfigured,omitempty"`
	ClaudeCodeBaseURL          string `json:"claudeCodeBaseUrl,omitempty"`
	// CodexModels and ClaudeCodeModels are the model catalogs AI 对话 offers
	// for that CLI, in display order. Empty follows the CLI's built-in models
	// on the official endpoint, or the models the Base URL publishes.
	CodexModels      []CLIModel `json:"codexModels,omitempty"`
	ClaudeCodeModels []CLIModel `json:"claudeCodeModels,omitempty"`
	// AgentCLICredentials holds the credentials of the agent CLIs
	// (AgentCLIExecutorKinds) by kind.
	AgentCLICredentials map[string]AgentCLICredential `json:"agentCliCredentials,omitempty"`
	ID                  int64                         `json:"id"`
	StaffID             int                           `json:"staffId"`
	LoginName           string                        `json:"loginName"`
	ChineseName         string                        `json:"chineseName"`
	DeptID              int                           `json:"deptId"`
	DeptName            string                        `json:"deptName"`
	// PlatformRole is a platform-wide role, independent of project membership.
	// "admin" may edit generic SOP templates; empty is a regular user.
	PlatformRole                 string    `json:"platformRole,omitempty"`
	GitToken                     string    `json:"gitToken,omitempty"`
	GitTokenConfigured           bool      `json:"gitTokenConfigured,omitempty"`
	GitTokenOAuth                bool      `json:"gitTokenOAuth,omitempty"`
	GitSSHKeyConfigured          bool      `json:"gitSshKeyConfigured,omitempty"`
	GongfengRefreshToken         string    `json:"-"`
	GongfengTokenExpiresAt       time.Time `json:"-"`
	CodeBuddyAPIKey              string    `json:"codeBuddyApiKey,omitempty"`
	CodeBuddyAPIKeyConfigured    bool      `json:"codeBuddyApiKeyConfigured,omitempty"`
	CodeBuddyInternetEnvironment string    `json:"codeBuddyInternetEnvironment,omitempty"`
	CreatedAt                    time.Time `json:"createdAt"`
	UpdatedAt                    time.Time `json:"updatedAt"`
}

const PlatformRoleAdmin = "admin"

// GitSSHKey is the public description of a personal SSH credential.
type GitSSHKey struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Host string `json:"host"`
}

// NormalizeCodeBuddyInternetEnvironment maps a network environment chosen in
// personal settings to the CODEBUDDY_INTERNET_ENVIRONMENT value: "" for the
// international edition, "internal" for the China edition and "ioa" for iOA.
// The CLI silently falls back to the international edition for any value it
// does not recognize, so unknown values report ok=false instead of being saved.
func NormalizeCodeBuddyInternetEnvironment(raw string) (value string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "public", "intl", "international", "国际站":
		return "", true
	case "internal", "china", "cn", "国内站":
		return "internal", true
	case "ioa":
		return "ioa", true
	}
	return "", false
}

// IsPlatformAdmin reports whether the user holds the platform-admin role.
func IsPlatformAdmin(u User) bool {
	return strings.EqualFold(strings.TrimSpace(u.PlatformRole), PlatformRoleAdmin)
}

// PublicUser returns user directory information without exposing credentials.
func PublicUser(u User) User {
	u.DeepSeekAPIKeyConfigured = strings.TrimSpace(u.DeepSeekAPIKey) != ""
	u.DeepSeekAPIKey = ""
	u.CodexAPIKeyConfigured = strings.TrimSpace(u.CodexAPIKey) != ""
	u.CodexAPIKey = ""
	u.ClaudeCodeAPIKeyConfigured = strings.TrimSpace(u.ClaudeCodeAPIKey) != ""
	u.ClaudeCodeAPIKey = ""
	u.GitTokenConfigured = strings.TrimSpace(u.GitToken) != ""
	u.GitTokenOAuth = strings.TrimSpace(u.GongfengRefreshToken) != ""
	u.GitToken = ""
	u.GongfengRefreshToken = ""
	u.GongfengTokenExpiresAt = time.Time{}
	u.CodeBuddyAPIKeyConfigured = strings.TrimSpace(u.CodeBuddyAPIKey) != ""
	u.CodeBuddyAPIKey = ""
	if len(u.AgentCLICredentials) > 0 {
		public := make(map[string]AgentCLICredential, len(u.AgentCLICredentials))
		for kind, cred := range u.AgentCLICredentials {
			cred.APIKeyConfigured = strings.TrimSpace(cred.APIKey) != ""
			cred.APIKey = ""
			public[kind] = cred
		}
		u.AgentCLICredentials = public
	}
	return u
}

// CLIModel is one entry of a CLI model catalog. ID is passed to the CLI
// as-is; an empty Name shows the ID.
type CLIModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// AgentCLICredential is the personal configuration of one agent CLI. An
// empty BaseURL means the CLI's official endpoint; Protocol is the wire
// protocol BaseURL speaks (AgentCLIProtocolOptions).
type AgentCLICredential struct {
	APIKey           string     `json:"apiKey,omitempty"`
	APIKeyConfigured bool       `json:"apiKeyConfigured,omitempty"`
	BaseURL          string     `json:"baseUrl,omitempty"`
	Protocol         string     `json:"protocol,omitempty"`
	Models           []CLIModel `json:"models,omitempty"`
}

// Wire protocols an agent CLI can speak to a user Base URL.
const (
	AgentCLIProtocolChat      = "chat"      // OpenAI Chat Completions
	AgentCLIProtocolResponses = "responses" // OpenAI Responses
	AgentCLIProtocolMessages  = "messages"  // Anthropic Messages
	AgentCLIProtocolGemini    = "gemini"    // Google Gemini API
)

// AgentCLIProtocolOptions lists the protocols each agent CLI speaks against a
// user Base URL; the first one is the default. Cursor only talks to Cursor.
var AgentCLIProtocolOptions = map[string][]string{
	"opencode": {AgentCLIProtocolChat, AgentCLIProtocolResponses, AgentCLIProtocolMessages},
	"kimi":     {AgentCLIProtocolChat, AgentCLIProtocolResponses, AgentCLIProtocolMessages},
	"pi":       {AgentCLIProtocolChat, AgentCLIProtocolResponses, AgentCLIProtocolMessages},
	"dsh":      {AgentCLIProtocolChat, AgentCLIProtocolResponses, AgentCLIProtocolMessages},
	"zcode":    {AgentCLIProtocolMessages, AgentCLIProtocolChat, AgentCLIProtocolResponses},
	"agy":      {AgentCLIProtocolGemini, AgentCLIProtocolChat},
}

// NormalizeAgentCLIProtocol returns a supported protocol choice; unknown
// values fall back to the CLI's default.
func NormalizeAgentCLIProtocol(kind, protocol string) string {
	options := AgentCLIProtocolOptions[strings.TrimSpace(kind)]
	for _, option := range options {
		if option == strings.TrimSpace(protocol) {
			return option
		}
	}
	if len(options) == 0 {
		return ""
	}
	return options[0]
}

func PublicUsers(items []User) []User {
	out := make([]User, len(items))
	for i, u := range items {
		out[i] = PublicUser(u)
	}
	return out
}

// APIToken is a user-owned credential for REST API and MCP access.
// TokenHash is persisted but never serialized; the plaintext token is shown
// only once when it is created.
type APIToken struct {
	ID         string     `json:"id"`
	UserID     int64      `json:"userId"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	TokenHash  string     `json:"-"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}
