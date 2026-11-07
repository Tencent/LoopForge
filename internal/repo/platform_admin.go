package repo

import (
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const platformAdminsEnv = "AGENTFLOW_PLATFORM_ADMINS"

// BuiltinPlatformAdmins are the default login names that always hold the
// platform-admin role. Extra names can be added without a code change via
// AGENTFLOW_PLATFORM_ADMINS (comma/space/semicolon separated) or by granting
// users.platform_role=admin through the API.
var BuiltinPlatformAdmins = []string{}

// EnvPlatformAdmins returns extra allowlisted logins from AGENTFLOW_PLATFORM_ADMINS.
func EnvPlatformAdmins() []string {
	raw := strings.TrimSpace(os.Getenv(platformAdminsEnv))
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		login := strings.ToLower(strings.TrimSpace(part))
		if login == "" || seen[login] {
			continue
		}
		seen[login] = true
		out = append(out, login)
	}
	return out
}

// IsBuiltinPlatformAdmin reports whether loginName is on the hard-coded
// platform-admin allowlist. Comparison is case-insensitive.
func IsBuiltinPlatformAdmin(loginName string) bool {
	return loginInFold(loginName, BuiltinPlatformAdmins)
}

// IsAllowlistedPlatformAdmin reports whether loginName is pinned by code or env.
func IsAllowlistedPlatformAdmin(loginName string) bool {
	return IsBuiltinPlatformAdmin(loginName) || loginInFold(loginName, EnvPlatformAdmins())
}

func loginInFold(loginName string, names []string) bool {
	login := strings.ToLower(strings.TrimSpace(loginName))
	if login == "" {
		return false
	}
	for _, name := range names {
		if login == strings.ToLower(strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// IsPlatformAdmin reports whether loginName may edit generic templates.
// Allowlists win so named owners work even if their user row is missing.
func (r *UsersRepo) IsPlatformAdmin(loginName string) bool {
	if IsAllowlistedPlatformAdmin(loginName) {
		return true
	}
	user, ok := r.UserByLoginName(loginName)
	return ok && domain.IsPlatformAdmin(user)
}

// SetUserPlatformRole writes the platform-wide role without touching credentials.
func (r *UsersRepo) SetUserPlatformRole(id int64, role string) error {
	user, ok := r.User(id)
	if !ok {
		return errors.New("user not found")
	}
	user.PlatformRole = strings.TrimSpace(role)
	user.UpdatedAt = time.Now().UTC()
	_, err := r.writeUser(user)
	return err
}

// PlatformAdminRecord is one configured platform admin for the settings page.
type PlatformAdminRecord struct {
	LoginName   string `json:"loginName"`
	ChineseName string `json:"chineseName,omitempty"`
	UserID      int64  `json:"userId,omitempty"`
	Source      string `json:"source"` // builtin, env, granted
	Revocable   bool   `json:"revocable"`
}

func (r *UsersRepo) ListPlatformAdmins() []PlatformAdminRecord {
	byLogin := map[string]PlatformAdminRecord{}
	add := func(login, source string) {
		login = strings.ToLower(strings.TrimSpace(login))
		if login == "" {
			return
		}
		rec := byLogin[login]
		rec.LoginName = login
		if rec.Source == "" || source == "builtin" || (source == "env" && rec.Source != "builtin") {
			rec.Source = source
		}
		rec.Revocable = rec.Source == "granted"
		if user, ok := r.UserByLoginName(login); ok {
			rec.UserID = user.ID
			rec.ChineseName = user.ChineseName
		}
		byLogin[login] = rec
	}
	for _, login := range BuiltinPlatformAdmins {
		add(login, "builtin")
	}
	for _, login := range EnvPlatformAdmins() {
		add(login, "env")
	}
	for _, user := range r.Users() {
		if domain.IsPlatformAdmin(user) {
			add(user.LoginName, "granted")
		}
	}
	out := make([]PlatformAdminRecord, 0, len(byLogin))
	for _, rec := range byLogin {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LoginName < out[j].LoginName })
	return out
}

func (r *UsersRepo) GrantPlatformAdmin(loginName string) (PlatformAdminRecord, error) {
	loginName = strings.TrimSpace(loginName)
	if loginName == "" {
		return PlatformAdminRecord{}, errors.New("loginName is required")
	}
	if IsAllowlistedPlatformAdmin(loginName) {
		return r.platformAdminRecord(loginName), nil
	}
	now := time.Now().UTC()
	user, ok := r.UserByLoginName(loginName)
	if !ok {
		if err := r.PutUser(domain.User{LoginName: loginName, ChineseName: loginName, PlatformRole: domain.PlatformRoleAdmin, CreatedAt: now, UpdatedAt: now}); err != nil {
			return PlatformAdminRecord{}, err
		}
	} else if !domain.IsPlatformAdmin(user) {
		if err := r.SetUserPlatformRole(user.ID, domain.PlatformRoleAdmin); err != nil {
			return PlatformAdminRecord{}, err
		}
	}
	return r.platformAdminRecord(loginName), nil
}

func (r *UsersRepo) RevokePlatformAdmin(loginName string) error {
	loginName = strings.TrimSpace(loginName)
	if loginName == "" {
		return errors.New("loginName is required")
	}
	if IsAllowlistedPlatformAdmin(loginName) {
		return errors.New("内置或环境变量配置的平台管理员不能通过界面撤销，请改 AGENTFLOW_PLATFORM_ADMINS 或代码白名单")
	}
	user, ok := r.UserByLoginName(loginName)
	if !ok {
		return errors.New("user not found")
	}
	return r.SetUserPlatformRole(user.ID, "")
}

func (r *UsersRepo) platformAdminRecord(loginName string) PlatformAdminRecord {
	for _, rec := range r.ListPlatformAdmins() {
		if strings.EqualFold(rec.LoginName, loginName) {
			return rec
		}
	}
	return PlatformAdminRecord{LoginName: strings.ToLower(strings.TrimSpace(loginName)), Source: "granted", Revocable: true}
}
