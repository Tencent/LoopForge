package domain

import (
	"strings"
	"time"
)

// User 表示系统注册或登录的员工实体，用于后续资源归属与权限隔离。
type User struct {
	TaihuPAT                   string `json:"taihuPat,omitempty"`
	TaihuPATConfigured         bool   `json:"taihuPatConfigured,omitempty"`
	CodexAPIKey                string `json:"codexApiKey,omitempty"`
	CodexAPIKeyConfigured      bool   `json:"codexApiKeyConfigured,omitempty"`
	CodexBaseURL               string `json:"codexBaseUrl,omitempty"`
	ClaudeCodeAPIKey           string `json:"claudeCodeApiKey,omitempty"`
	ClaudeCodeAPIKeyConfigured bool   `json:"claudeCodeApiKeyConfigured,omitempty"`
	ClaudeCodeBaseURL          string `json:"claudeCodeBaseUrl,omitempty"`
	ID                         int64  `json:"id"`
	StaffID                    int    `json:"staffId"`
	LoginName                  string `json:"loginName"`
	ChineseName                string `json:"chineseName"`
	DeptID                     int    `json:"deptId"`
	DeptName                   string `json:"deptName"`
	// PlatformRole is a platform-wide role, independent of project membership.
	// "admin" may edit generic SOP templates; empty is a regular user.
	PlatformRole                 string    `json:"platformRole,omitempty"`
	GitToken                     string    `json:"gitToken,omitempty"`
	GitTokenConfigured           bool      `json:"gitTokenConfigured,omitempty"`
	CodeBuddyAPIKey              string    `json:"codeBuddyApiKey,omitempty"`
	CodeBuddyAPIKeyConfigured    bool      `json:"codeBuddyApiKeyConfigured,omitempty"`
	CodeBuddyInternetEnvironment string    `json:"codeBuddyInternetEnvironment,omitempty"`
	CreatedAt                    time.Time `json:"createdAt"`
	UpdatedAt                    time.Time `json:"updatedAt"`
}

const PlatformRoleAdmin = "admin"

// IsPlatformAdmin reports whether the user holds the platform-admin role.
func IsPlatformAdmin(u User) bool {
	return strings.EqualFold(strings.TrimSpace(u.PlatformRole), PlatformRoleAdmin)
}

// PublicUser returns user directory information without exposing credentials.
func PublicUser(u User) User {
	u.TaihuPATConfigured = strings.TrimSpace(u.TaihuPAT) != ""
	u.TaihuPAT = ""
	u.CodexAPIKeyConfigured = strings.TrimSpace(u.CodexAPIKey) != ""
	u.CodexAPIKey = ""
	u.ClaudeCodeAPIKeyConfigured = strings.TrimSpace(u.ClaudeCodeAPIKey) != ""
	u.ClaudeCodeAPIKey = ""
	u.GitTokenConfigured = strings.TrimSpace(u.GitToken) != ""
	u.GitToken = ""
	u.CodeBuddyAPIKeyConfigured = strings.TrimSpace(u.CodeBuddyAPIKey) != ""
	u.CodeBuddyAPIKey = ""
	return u
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
