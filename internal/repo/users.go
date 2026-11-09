package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"agentflow/internal/domain"
)

// ---------- Users ----------

type userWithConfig struct {
	DeepSeekAPIKey               *string    `gorm:"column:deepseek_api_key"`
	DeepSeekBaseURL              *string    `gorm:"column:deepseek_base_url"`
	ID                           int64      `gorm:"column:id"`
	StaffID                      int        `gorm:"column:staff_id"`
	LoginName                    string     `gorm:"column:login_name"`
	ChineseName                  string     `gorm:"column:chinese_name"`
	DeptID                       int        `gorm:"column:dept_id"`
	DeptName                     string     `gorm:"column:dept_name"`
	PlatformRole                 string     `gorm:"column:platform_role"`
	GitToken                     *string    `gorm:"column:git_token"`
	GitSSHKeyConfigured          bool       `gorm:"column:git_ssh_key_configured"`
	GongfengRefreshToken         *string    `gorm:"column:gongfeng_refresh_token"`
	GongfengTokenExpiresAt       *time.Time `gorm:"column:gongfeng_token_expires_at"`
	CodeBuddyAPIKey              *string    `gorm:"column:codebuddy_api_key"`
	CodeBuddyInternetEnvironment string     `gorm:"column:codebuddy_internet_environment"`
	CodexAPIKey                  *string    `gorm:"column:codex_api_key"`
	CodexBaseURL                 *string    `gorm:"column:codex_base_url"`
	CodexModels                  *string    `gorm:"column:codex_models"`
	ClaudeCodeAPIKey             *string    `gorm:"column:claude_code_api_key"`
	ClaudeCodeBaseURL            *string    `gorm:"column:claude_code_base_url"`
	ClaudeCodeModels             *string    `gorm:"column:claude_code_models"`
	AgentCLICredentials          *string    `gorm:"column:agent_cli_credentials"`
	Created                      *time.Time `gorm:"column:created_at"`
	Updated                      *time.Time `gorm:"column:updated_at"`
}

const userSelectColumns = `u.id, u.staff_id, u.login_name, u.chinese_name, u.dept_id, u.dept_name,
	COALESCE(u.platform_role, '') AS platform_role,
	c.git_token, EXISTS (SELECT 1 FROM user_git_ssh_keys k WHERE k.user_id = u.id) AS git_ssh_key_configured,
	c.gongfeng_refresh_token, c.gongfeng_token_expires_at, c.codebuddy_api_key, COALESCE(c.codebuddy_internet_environment, '') AS codebuddy_internet_environment,
	c.codex_api_key, c.codex_base_url, c.codex_models, c.claude_code_api_key, c.claude_code_base_url, c.claude_code_models, c.deepseek_api_key, c.deepseek_base_url,
	c.agent_cli_credentials,
	u.created_at, u.updated_at`

func (r userWithConfig) toDomain() domain.User {
	u := domain.User{
		ID: r.ID, StaffID: r.StaffID, LoginName: r.LoginName, ChineseName: r.ChineseName,
		DeptID: r.DeptID, DeptName: r.DeptName, PlatformRole: r.PlatformRole,
		CodeBuddyInternetEnvironment: r.CodeBuddyInternetEnvironment,
		GitSSHKeyConfigured:          r.GitSSHKeyConfigured,
	}
	if r.GitToken != nil {
		u.GitToken = *r.GitToken
	}
	if r.GongfengRefreshToken != nil {
		u.GongfengRefreshToken = *r.GongfengRefreshToken
	}
	if r.GongfengTokenExpiresAt != nil {
		u.GongfengTokenExpiresAt = *r.GongfengTokenExpiresAt
	}
	if r.CodeBuddyAPIKey != nil {
		u.CodeBuddyAPIKey = *r.CodeBuddyAPIKey
	}
	if r.CodexAPIKey != nil {
		u.CodexAPIKey = *r.CodexAPIKey
	}
	if r.CodexBaseURL != nil {
		u.CodexBaseURL = *r.CodexBaseURL
	}
	u.CodexModels = decodeCLIModels(r.CodexModels)
	if r.ClaudeCodeAPIKey != nil {
		u.ClaudeCodeAPIKey = *r.ClaudeCodeAPIKey
	}
	if r.ClaudeCodeBaseURL != nil {
		u.ClaudeCodeBaseURL = *r.ClaudeCodeBaseURL
	}
	u.ClaudeCodeModels = decodeCLIModels(r.ClaudeCodeModels)
	if r.AgentCLICredentials != nil {
		_ = json.Unmarshal([]byte(*r.AgentCLICredentials), &u.AgentCLICredentials)
	}
	if r.DeepSeekAPIKey != nil {
		u.DeepSeekAPIKey = *r.DeepSeekAPIKey
	}
	if r.DeepSeekBaseURL != nil {
		u.DeepSeekBaseURL = *r.DeepSeekBaseURL
	}
	if r.Created != nil {
		u.CreatedAt = *r.Created
	}
	if r.Updated != nil {
		u.UpdatedAt = *r.Updated
	}
	return u
}

func (r *UsersRepo) userQuery(where string, args ...any) (domain.User, bool) {
	var row userWithConfig
	q := r.DB.Raw(`SELECT `+userSelectColumns+` FROM users u LEFT JOIN user_configs c ON c.user_id=u.id WHERE `+where, args...)
	if err := q.Scan(&row).Error; err != nil || row.ID == 0 {
		return domain.User{}, false
	}
	return row.toDomain(), true
}

func (r *UsersRepo) Users() []domain.User {
	var rows []userWithConfig
	if err := r.DB.Raw(`SELECT ` + userSelectColumns + ` FROM users u LEFT JOIN user_configs c ON c.user_id=u.id ORDER BY u.login_name ASC`).Scan(&rows).Error; err != nil {
		return []domain.User{}
	}
	out := make([]domain.User, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LoginName < out[j].LoginName })
	return out
}

func (r *UsersRepo) User(id int64) (domain.User, bool) {
	if id <= 0 {
		return domain.User{}, false
	}
	return r.userQuery("u.id=?", id)
}

func (r *UsersRepo) UserByLoginName(loginName string) (domain.User, bool) {
	if r.isMySQL() {
		// MySQL collation is already case-insensitive and keeps index usage.
		return r.userQuery("u.login_name=?", loginName)
	}
	return r.userQuery("LOWER(u.login_name) = LOWER(?)", loginName)
}

func (r *UsersRepo) UserByStaffID(staffID int) (domain.User, bool) {
	return r.userQuery("u.staff_id=?", staffID)
}

func (r *UsersRepo) PutUser(v domain.User) error {
	if v.LoginName == "" && v.StaffID == 0 && v.ID == 0 {
		return errors.New("user loginName, staffId, or ID is required")
	}
	now := time.Now().UTC()
	var old domain.User
	var ok bool
	if v.ID > 0 {
		old, ok = r.User(v.ID)
	} else if v.LoginName != "" {
		old, ok = r.UserByLoginName(v.LoginName)
	}
	if ok {
		if v.ID == 0 {
			v.ID = old.ID
		}
		v.CreatedAt = old.CreatedAt
		// Authentication refreshes identity fields only. Credentials are managed
		// through UpdateUserCredentials and must survive every login refresh.
		v.GitToken = old.GitToken
		v.GongfengRefreshToken = old.GongfengRefreshToken
		v.GongfengTokenExpiresAt = old.GongfengTokenExpiresAt
		v.CodexAPIKey, v.CodexBaseURL, v.CodexModels = old.CodexAPIKey, old.CodexBaseURL, old.CodexModels
		v.ClaudeCodeAPIKey, v.ClaudeCodeBaseURL, v.ClaudeCodeModels = old.ClaudeCodeAPIKey, old.ClaudeCodeBaseURL, old.ClaudeCodeModels
		v.DeepSeekAPIKey, v.DeepSeekBaseURL = old.DeepSeekAPIKey, old.DeepSeekBaseURL
		v.CodeBuddyAPIKey = old.CodeBuddyAPIKey
		v.CodeBuddyInternetEnvironment = old.CodeBuddyInternetEnvironment
		v.AgentCLICredentials = old.AgentCLICredentials
		if strings.TrimSpace(v.PlatformRole) == "" {
			v.PlatformRole = old.PlatformRole
		}
	} else {
		v.CreatedAt = now
	}
	if IsAllowlistedPlatformAdmin(v.LoginName) {
		v.PlatformRole = domain.PlatformRoleAdmin
	}
	v.UpdatedAt = now
	saved, err := r.writeUser(v)
	if err != nil {
		return err
	}
	if !ok && userHasConfig(saved) {
		if err := r.writeUserConfig(saved); err != nil {
			return err
		}
	}
	return nil
}

func (r *UsersRepo) writeUser(v domain.User) (domain.User, error) {
	if v.ID > 0 {
		err := upsert(r.DB, "users", map[string]any{
			"id": v.ID, "staff_id": v.StaffID, "login_name": v.LoginName, "chinese_name": v.ChineseName,
			"dept_id": v.DeptID, "dept_name": v.DeptName, "platform_role": v.PlatformRole,
			"created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
		})
		return v, err
	}
	inserted, err := insertIgnore(r.DB, "users", map[string]any{
		"staff_id": v.StaffID, "login_name": v.LoginName, "chinese_name": v.ChineseName,
		"dept_id": v.DeptID, "dept_name": v.DeptName, "platform_role": v.PlatformRole,
		"created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
	})
	if err != nil {
		return domain.User{}, err
	}
	if !inserted {
		// The unique login already exists: adopt that row and refresh it.
		var existing userRow
		if err := r.DB.Where("login_name = ?", v.LoginName).Take(&existing).Error; err != nil {
			return domain.User{}, err
		}
		if err := r.DB.Model(&userRow{}).Where("id = ?", existing.ID).Updates(map[string]any{
			"staff_id": v.StaffID, "chinese_name": v.ChineseName, "dept_id": v.DeptID,
			"dept_name": v.DeptName, "platform_role": v.PlatformRole, "updated_at": timePtr(v.UpdatedAt),
		}).Error; err != nil {
			return domain.User{}, err
		}
		v.ID = existing.ID
		return v, nil
	}
	var row userRow
	if err := r.DB.Where("login_name = ?", v.LoginName).Take(&row).Error; err != nil {
		return domain.User{}, err
	}
	v.ID = row.ID
	return v, nil
}

func userHasConfig(user domain.User) bool {
	return user.DeepSeekAPIKey != "" || user.DeepSeekBaseURL != "" || user.GitToken != "" ||
		user.GongfengRefreshToken != "" ||
		user.CodeBuddyAPIKey != "" ||
		user.CodeBuddyInternetEnvironment != "" ||
		user.CodexAPIKey != "" ||
		user.CodexBaseURL != "" ||
		len(user.CodexModels) > 0 ||
		user.ClaudeCodeAPIKey != "" ||
		user.ClaudeCodeBaseURL != "" ||
		len(user.ClaudeCodeModels) > 0 ||
		len(user.AgentCLICredentials) > 0
}

type UserCredentialUpdate struct {
	DeepSeekAPIKey               string             `json:"deepSeekApiKey"`
	DeepSeekAPIKeyClear          bool               `json:"deepSeekApiKeyClear"`
	DeepSeekBaseURL              *string            `json:"deepSeekBaseUrl"`
	GitToken                     string             `json:"gitToken"`
	GitTokenClear                bool               `json:"gitTokenClear"`
	CodeBuddyAPIKey              string             `json:"codeBuddyApiKey"`
	CodeBuddyAPIKeyClear         bool               `json:"codeBuddyApiKeyClear"`
	CodeBuddyInternetEnvironment *string            `json:"codeBuddyInternetEnvironment"`
	CodexAPIKey                  string             `json:"codexApiKey"`
	CodexAPIKeyClear             bool               `json:"codexApiKeyClear"`
	CodexBaseURL                 *string            `json:"codexBaseUrl"`
	CodexModels                  *[]domain.CLIModel `json:"codexModels"`
	ClaudeCodeAPIKey             string             `json:"claudeCodeApiKey"`
	ClaudeCodeAPIKeyClear        bool               `json:"claudeCodeApiKeyClear"`
	ClaudeCodeBaseURL            *string            `json:"claudeCodeBaseUrl"`
	ClaudeCodeModels             *[]domain.CLIModel `json:"claudeCodeModels"`
	// AgentCLICredentials updates the agent CLIs by kind; absent fields keep
	// the stored value. CodeBuddy, Codex and Claude Code stay on their own columns.
	AgentCLICredentials map[string]AgentCLICredentialUpdate `json:"agentCliCredentials"`
}

type AgentCLICredentialUpdate struct {
	APIKey      string             `json:"apiKey"`
	APIKeyClear bool               `json:"apiKeyClear"`
	BaseURL     *string            `json:"baseUrl"`
	Protocol    *string            `json:"protocol"`
	Models      *[]domain.CLIModel `json:"models"`
}

func (r *UsersRepo) UpdateUserCredentials(id int64, update UserCredentialUpdate) (domain.User, error) {
	user, ok := r.User(id)
	if !ok {
		return domain.User{}, errors.New("user not found")
	}
	if update.GitTokenClear {
		user.GitToken = ""
		user.GongfengRefreshToken = ""
		user.GongfengTokenExpiresAt = time.Time{}
	} else if value := strings.TrimSpace(update.GitToken); value != "" {
		user.GitToken = value
		// A pasted personal access token replaces an OAuth grant. Leaving the
		// refresh token in place would overwrite the paste on the next use.
		user.GongfengRefreshToken = ""
		user.GongfengTokenExpiresAt = time.Time{}
	}
	if update.CodeBuddyAPIKeyClear {
		user.CodeBuddyAPIKey = ""
	} else if value := strings.TrimSpace(update.CodeBuddyAPIKey); value != "" {
		user.CodeBuddyAPIKey = value
	}
	if update.CodeBuddyInternetEnvironment != nil {
		user.CodeBuddyInternetEnvironment = strings.TrimSpace(*update.CodeBuddyInternetEnvironment)
	}
	if update.CodexAPIKeyClear {
		user.CodexAPIKey = ""
	} else if value := strings.TrimSpace(update.CodexAPIKey); value != "" {
		user.CodexAPIKey = value
	}
	if update.ClaudeCodeAPIKeyClear {
		user.ClaudeCodeAPIKey = ""
	} else if value := strings.TrimSpace(update.ClaudeCodeAPIKey); value != "" {
		user.ClaudeCodeAPIKey = value
	}
	if update.CodexBaseURL != nil {
		user.CodexBaseURL = strings.TrimSpace(*update.CodexBaseURL)
	}
	if update.ClaudeCodeBaseURL != nil {
		user.ClaudeCodeBaseURL = strings.TrimSpace(*update.ClaudeCodeBaseURL)
	}
	if update.CodexModels != nil {
		models, err := normalizeCLIModels(*update.CodexModels)
		if err != nil {
			return domain.User{}, err
		}
		user.CodexModels = models
	}
	if update.ClaudeCodeModels != nil {
		models, err := normalizeCLIModels(*update.ClaudeCodeModels)
		if err != nil {
			return domain.User{}, err
		}
		user.ClaudeCodeModels = models
	}
	if err := applyAgentCLICredentialUpdates(&user, update.AgentCLICredentials); err != nil {
		return domain.User{}, err
	}
	if update.DeepSeekAPIKeyClear {
		user.DeepSeekAPIKey = ""
	} else if value := strings.TrimSpace(update.DeepSeekAPIKey); value != "" {
		user.DeepSeekAPIKey = value
	}
	if update.DeepSeekBaseURL != nil {
		user.DeepSeekBaseURL = strings.TrimSpace(*update.DeepSeekBaseURL)
	}
	user.UpdatedAt = time.Now().UTC()
	if err := r.writeUserConfig(user); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

// SaveGongfengOAuth stores an OAuth access token in the git credential slot
// and keeps the refresh token so a later use can renew it.
func (r *UsersRepo) SaveGongfengOAuth(id int64, accessToken, refreshToken string, expiresAt time.Time) (domain.User, error) {
	user, ok := r.User(id)
	if !ok {
		return domain.User{}, errors.New("user not found")
	}
	user.GitToken = strings.TrimSpace(accessToken)
	user.GongfengRefreshToken = strings.TrimSpace(refreshToken)
	user.GongfengTokenExpiresAt = expiresAt.UTC()
	user.UpdatedAt = time.Now().UTC()
	if err := r.writeUserConfig(user); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (r *UsersRepo) writeUserConfig(v domain.User) error {
	values := map[string]any{
		"deepseek_api_key": nullableString(v.DeepSeekAPIKey), "deepseek_base_url": nullableString(v.DeepSeekBaseURL),
		"user_id":                v.ID,
		"git_token":              nullableString(v.GitToken),
		"gongfeng_refresh_token": nullableString(v.GongfengRefreshToken), "gongfeng_token_expires_at": timePtr(v.GongfengTokenExpiresAt),
		"codebuddy_api_key":              nullableString(v.CodeBuddyAPIKey),
		"codebuddy_internet_environment": v.CodeBuddyInternetEnvironment,
		"codex_api_key":                  nullableString(v.CodexAPIKey), "codex_base_url": nullableString(v.CodexBaseURL), "codex_models": encodeCLIModels(v.CodexModels),
		"claude_code_api_key": nullableString(v.ClaudeCodeAPIKey), "claude_code_base_url": nullableString(v.ClaudeCodeBaseURL), "claude_code_models": encodeCLIModels(v.ClaudeCodeModels),
		"agent_cli_credentials": encodeAgentCLICredentials(v.AgentCLICredentials),
		"created_at":            timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
	}
	return upsert(r.DB, "user_configs", values)
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func applyAgentCLICredentialUpdates(user *domain.User, updates map[string]AgentCLICredentialUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	merged := make(map[string]domain.AgentCLICredential, len(user.AgentCLICredentials)+len(updates))
	for kind, cred := range user.AgentCLICredentials {
		merged[kind] = cred
	}
	for kind, update := range updates {
		kind = strings.TrimSpace(kind)
		if !domain.IsAgentCLIKind(kind) {
			return fmt.Errorf("不支持的 CLI：%s", kind)
		}
		cred := merged[kind]
		if update.APIKeyClear {
			cred.APIKey = ""
		} else if value := strings.TrimSpace(update.APIKey); value != "" {
			cred.APIKey = value
		}
		if update.BaseURL != nil {
			cred.BaseURL = strings.TrimSpace(*update.BaseURL)
		}
		if update.Protocol != nil {
			protocol := strings.TrimSpace(*update.Protocol)
			if protocol != "" && domain.NormalizeAgentCLIProtocol(kind, protocol) != protocol {
				return fmt.Errorf("%s 不支持协议 %s", kind, protocol)
			}
			cred.Protocol = protocol
		}
		if update.Models != nil {
			models, err := normalizeCLIModels(*update.Models)
			if err != nil {
				return err
			}
			cred.Models = models
		}
		if cred.APIKey == "" && cred.BaseURL == "" && cred.Protocol == "" && len(cred.Models) == 0 {
			delete(merged, kind)
			continue
		}
		merged[kind] = cred
	}
	if len(merged) == 0 {
		merged = nil
	}
	user.AgentCLICredentials = merged
	return nil
}

func encodeAgentCLICredentials(creds map[string]domain.AgentCLICredential) any {
	if len(creds) == 0 {
		return nil
	}
	raw, err := json.Marshal(creds)
	if err != nil || string(raw) == "{}" || string(raw) == "null" {
		return nil
	}
	return string(raw)
}

const (
	maxCLIModels        = 100
	maxCLIModelIDLen    = 128
	maxCLIModelNameRune = 64
)

func decodeCLIModels(raw *string) []domain.CLIModel {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	var models []domain.CLIModel
	if json.Unmarshal([]byte(*raw), &models) != nil {
		return nil
	}
	return models
}

func encodeCLIModels(models []domain.CLIModel) any {
	if len(models) == 0 {
		return nil
	}
	raw, err := json.Marshal(models)
	if err != nil {
		return nil
	}
	return string(raw)
}

func normalizeCLIModels(models []domain.CLIModel) ([]domain.CLIModel, error) {
	if len(models) > maxCLIModels {
		return nil, fmt.Errorf("模型目录最多 %d 个", maxCLIModels)
	}
	out := make([]domain.CLIModel, 0, len(models))
	seen := map[string]bool{}
	for _, model := range models {
		id, name := strings.TrimSpace(model.ID), strings.TrimSpace(model.Name)
		if id == "" {
			return nil, errors.New("模型 ID 不能为空")
		}
		if len(id) > maxCLIModelIDLen {
			return nil, fmt.Errorf("模型 ID 长度不能超过 %d 个字符", maxCLIModelIDLen)
		}
		for _, r := range id {
			if r <= ' ' || r == 0x7f || r == '"' || r == '\'' || r == '`' || r == '\\' {
				return nil, fmt.Errorf("模型 ID %q 不能包含空格、引号、反斜杠或控制字符", id)
			}
		}
		if seen[id] {
			return nil, fmt.Errorf("模型 ID %s 重复", id)
		}
		seen[id] = true
		if utf8.RuneCountInString(name) > maxCLIModelNameRune {
			return nil, fmt.Errorf("显示名称长度不能超过 %d 个字符", maxCLIModelNameRune)
		}
		for _, r := range name {
			if r < ' ' || r == 0x7f {
				return nil, errors.New("显示名称不能包含控制字符")
			}
		}
		if name == id {
			name = ""
		}
		out = append(out, domain.CLIModel{ID: id, Name: name})
	}
	return out, nil
}
