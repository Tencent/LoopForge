package domain

import (
	"strings"
	"time"
)

// User 表示系统注册或登录的员工实体，用于后续资源归属与权限隔离。
type User struct {
	AssistantDefaults          AssistantDefaults `json:"-"`
	CodexAPIKey                string            `json:"codexApiKey,omitempty"`
	CodexAPIKeyConfigured      bool              `json:"codexApiKeyConfigured,omitempty"`
	CodexBaseURL               string            `json:"codexBaseUrl,omitempty"`
	ClaudeCodeAPIKey           string            `json:"claudeCodeApiKey,omitempty"`
	ClaudeCodeAPIKeyConfigured bool              `json:"claudeCodeApiKeyConfigured,omitempty"`
	ClaudeCodeBaseURL          string            `json:"claudeCodeBaseUrl,omitempty"`
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
	PlatformRole                 string     `json:"platformRole,omitempty"`
	GitToken                     string     `json:"gitToken,omitempty"`
	GitHubCredentialConfigured   bool       `json:"gitHubCredentialConfigured,omitempty"`
	GitTokenOAuth                bool       `json:"gitTokenOAuth,omitempty"`
	GitSSHKeyConfigured          bool       `json:"gitSshKeyConfigured,omitempty"`
	GitHTTPCredentialsConfigured bool       `json:"gitHttpCredentialsConfigured,omitempty"`
	GongfengRefreshToken         string     `json:"-"`
	GongfengTokenExpiresAt       time.Time  `json:"-"`
	CodeBuddyAPIKey              string     `json:"codeBuddyApiKey,omitempty"`
	CodeBuddyAPIKeyConfigured    bool       `json:"codeBuddyApiKeyConfigured,omitempty"`
	CodeBuddyBaseURL             string     `json:"codeBuddyBaseUrl,omitempty"`
	CodeBuddyInternetEnvironment string     `json:"codeBuddyInternetEnvironment,omitempty"`
	CodeBuddyModels              []CLIModel `json:"codeBuddyModels,omitempty"`
	CreatedAt                    time.Time  `json:"createdAt"`
	UpdatedAt                    time.Time  `json:"updatedAt"`
}

// HasExecutorCredential reports whether the user has their own key for a CLI.
func (u User) HasExecutorCredential(kind string) bool {
	var key string
	switch kind {
	case "codebuddy":
		key = u.CodeBuddyAPIKey
	case "codex":
		key = u.CodexAPIKey
	case "claude-code":
		key = u.ClaudeCodeAPIKey
	default:
		if IsAgentCLIKind(kind) {
			key = u.AgentCLICredentials[kind].APIKey
		}
	}
	return strings.TrimSpace(key) != ""
}

const PlatformRoleAdmin = "admin"

// GitSSHKey is the public description of a personal SSH credential.
type GitSSHKey struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Host string `json:"host"`
}

// Platforms of a personal HTTPS Git credential. Git authentication does not
// depend on the platform; it only selects the CLI conveniences, PR discovery
// and delivery polling. "other" makes no API calls.
const (
	GitPlatformGitHub = "github"
	GitPlatformGitLab = "gitlab"
	GitPlatformOther  = "other"
)

// GitTokenHTTPUser is the Basic-auth user when a credential names none; the
// GitHub and Gitea families accept any user with a token password.
const GitTokenHTTPUser = "x-access-token"

// ManagedGitHTTPCredentialID identifies the read-only credential the internal
// adapter keeps (and refreshes) in user_configs.
const ManagedGitHTTPCredentialID = "managed-internal"

// GitHTTPCredential is the public description of a personal HTTPS Git
// credential bound to one host[:port]. It never carries the token.
type GitHTTPCredential struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Host     string `json:"host"`
	Username string `json:"username,omitempty"`
	Managed  bool   `json:"managed,omitempty"`
}

// GitUsername is the Basic-auth user Git sends with the token.
func (c GitHTTPCredential) GitUsername() string {
	if user := strings.TrimSpace(c.Username); user != "" {
		return user
	}
	return GitTokenHTTPUser
}

// APIBase is the REST API root the control plane uses for the credential's
// host, or "" for platforms it does not call.
func (c GitHTTPCredential) APIBase() string {
	switch c.Platform {
	case GitPlatformGitHub:
		if c.Host == "github.com" {
			return "https://api.github.com"
		}
		return "https://" + c.Host + "/api/v3"
	case GitPlatformGitLab:
		return "https://" + c.Host + "/api/v4"
	default:
		return ""
	}
}

// APIAuthHeader is the header that authenticates the token against APIBase:
// a bearer token for GitHub and GitLab OAuth tokens, PRIVATE-TOKEN for other
// GitLab tokens.
func (c GitHTTPCredentialSecret) APIAuthHeader() (string, string) {
	if c.Platform == GitPlatformGitLab && c.Username != "oauth2" {
		return "PRIVATE-TOKEN", c.Token
	}
	return "Authorization", "Bearer " + c.Token
}

// GitHTTPCredentialSecret is a credential with its token; it exists in memory
// only and never serializes the token.
type GitHTTPCredentialSecret struct {
	GitHTTPCredential
	Token string `json:"-"`
	// ExpiresAt is set for managed OAuth tokens; zero means no known expiry.
	ExpiresAt time.Time `json:"-"`
}

// GitHTTPExecutionCredential is only for authenticated, transient execution
// dispatch. Unlike the settings secret, it must carry the token over the wire.
// Queued tasks and public management responses always remove these values.
type GitHTTPExecutionCredential struct {
	GitHTTPCredential
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

// NormalizeGitPlatform reports ok=false for a platform outside the three kinds.
func NormalizeGitPlatform(raw string) (string, bool) {
	switch p := strings.ToLower(strings.TrimSpace(raw)); p {
	case GitPlatformGitHub, GitPlatformGitLab, GitPlatformOther:
		return p, true
	default:
		return "", false
	}
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
	u.CodexAPIKeyConfigured = strings.TrimSpace(u.CodexAPIKey) != ""
	u.CodexAPIKey = ""
	u.ClaudeCodeAPIKeyConfigured = strings.TrimSpace(u.ClaudeCodeAPIKey) != ""
	u.ClaudeCodeAPIKey = ""
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
//
// Efforts is set on a model family folded out of the CLI's own model
// list: it maps each reasoning effort onto the model ID the CLI printed
// for it, and DefaultEffort is the effort an empty choice runs. They are
// saved with the catalog so the family resolves for its owner on any
// instance and after a restart.
type CLIModel struct {
	ID            string            `json:"id"`
	Name          string            `json:"name,omitempty"`
	Efforts       map[string]string `json:"efforts,omitempty"`
	DefaultEffort string            `json:"defaultEffort,omitempty"`
	// ReasoningEfforts is the user's override. An empty list permits only the native default.
	ReasoningEfforts *[]string `json:"reasoningEfforts,omitempty"`
	// DiscoveredReasoningEfforts is supplied by model discovery and used in automatic mode.
	DiscoveredReasoningEfforts *[]string `json:"discoveredReasoningEfforts,omitempty"`
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
	ModelsCustomized bool       `json:"modelsCustomized,omitempty"` // Cursor: preserve explicit edits across discovery.
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
