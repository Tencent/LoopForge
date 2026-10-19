package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	mysqldriver "github.com/go-sql-driver/mysql"
)

const (
	assistantAcceptanceLogin     = "example-maintainer"
	assistantAcceptanceProjectID = "assistant-test"
	assistantAcceptanceSandboxID = "assistant-local"
)

// AssistantAcceptanceIdentity is the local copy of a production user's
// CodeBuddy and Taihu credentials used only in disposable acceptance databases.
type AssistantAcceptanceIdentity struct {
	LoginName                    string
	StaffID                      int
	ChineseName                  string
	DeptID                       int
	DeptName                     string
	CodeBuddyAPIKey              string
	CodeBuddyInternetEnvironment string
	TaihuPAT                     string
	GitToken                     string
}

func mysqlReadDSN(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if !strings.Contains(dsn, "charset=") {
		if strings.Contains(dsn, "?") {
			dsn += "&charset=utf8mb4&collation=utf8mb4_unicode_ci"
		} else {
			dsn += "?charset=utf8mb4&collation=utf8mb4_unicode_ci"
		}
	}
	if !strings.Contains(dsn, "parseTime=") {
		dsn += "&parseTime=true&loc=UTC"
	}
	return dsn
}

func loopbackMySQLDSN(dsn string) bool {
	return strings.Contains(dsn, "tcp(127.0.0.1:") || strings.Contains(dsn, "tcp(localhost:")
}

// LookupCodeBuddyCredentialReadOnly copies the named user's CodeBuddy key from
// a remote MySQL with a single SELECT. It never writes.
func LookupCodeBuddyCredentialReadOnly(ctx context.Context, dsn, login string) (AssistantAcceptanceIdentity, error) {
	var ident AssistantAcceptanceIdentity
	login = strings.TrimSpace(login)
	dsn = strings.TrimSpace(dsn)
	if login == "" || dsn == "" {
		return ident, errors.New("missing production DSN or login")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return ident, fmt.Errorf("production MySQL DSN: %w", err)
	}
	cfg.Timeout = 10 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second
	cfg.AllowNativePasswords = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return ident, fmt.Errorf("production MySQL open: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return ident, fmt.Errorf("production MySQL ping: %w", err)
	}
	ident.LoginName = login
	err = db.QueryRowContext(ctx, `
SELECT COALESCE(u.staff_id, 0), COALESCE(u.chinese_name, ''), COALESCE(u.dept_id, 0), COALESCE(u.dept_name, ''),
       COALESCE(c.codebuddy_api_key, ''),
       COALESCE(c.codebuddy_internet_environment, ''),
       COALESCE(c.taihu_pat, ''),
       COALESCE(c.git_token, '')
  FROM users u
  LEFT JOIN user_configs c ON c.user_id = u.id
 WHERE u.login_name = ?
 LIMIT 1`, login).Scan(
		&ident.StaffID, &ident.ChineseName, &ident.DeptID, &ident.DeptName,
		&ident.CodeBuddyAPIKey, &ident.CodeBuddyInternetEnvironment, &ident.TaihuPAT, &ident.GitToken,
	)
	if err == sql.ErrNoRows {
		return AssistantAcceptanceIdentity{}, fmt.Errorf("user %s not found", login)
	}
	if err != nil {
		return AssistantAcceptanceIdentity{}, fmt.Errorf("lookup %s: %w", login, err)
	}
	ident.TaihuPAT = strings.TrimSpace(ident.TaihuPAT)
	ident.GitToken = strings.TrimSpace(ident.GitToken)
	if strings.TrimSpace(ident.CodeBuddyAPIKey) == "" {
		return AssistantAcceptanceIdentity{}, fmt.Errorf("user %s has no CodeBuddy API Key", login)
	}
	if strings.TrimSpace(ident.TaihuPAT) == "" {
		return AssistantAcceptanceIdentity{}, fmt.Errorf("user %s has no Taihu PAT", login)
	}
	if ident.CodeBuddyInternetEnvironment == "" {
		ident.CodeBuddyInternetEnvironment = "ioa"
	}
	return ident, nil
}

// SeedAssistantAcceptance writes the local test user, project, and sandbox.
func SeedAssistantAcceptance(st *repo.Store, ident AssistantAcceptanceIdentity) (domain.User, error) {
	if strings.TrimSpace(ident.LoginName) == "" || strings.TrimSpace(ident.CodeBuddyAPIKey) == "" {
		return domain.User{}, errors.New("acceptance identity is incomplete")
	}
	if ident.ChineseName == "" {
		ident.ChineseName = ident.LoginName
	}
	if err := st.PutUser(domain.User{
		LoginName:                    ident.LoginName,
		StaffID:                      ident.StaffID,
		ChineseName:                  ident.ChineseName,
		DeptID:                       ident.DeptID,
		DeptName:                     ident.DeptName,
		CodeBuddyAPIKey:              ident.CodeBuddyAPIKey,
		CodeBuddyInternetEnvironment: ident.CodeBuddyInternetEnvironment,
	}); err != nil {
		return domain.User{}, err
	}
	user, ok := st.UserByLoginName(ident.LoginName)
	if !ok {
		return domain.User{}, errors.New("acceptance user missing after insert")
	}
	env := ident.CodeBuddyInternetEnvironment
	if _, err := st.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{
		CodeBuddyAPIKey:              ident.CodeBuddyAPIKey,
		CodeBuddyInternetEnvironment: &env,
		TaihuPAT:                     ident.TaihuPAT,
		GitToken:                     ident.GitToken,
	}); err != nil {
		return domain.User{}, err
	}
	user, ok = st.UserByLoginName(ident.LoginName)
	if !ok || strings.TrimSpace(user.CodeBuddyAPIKey) == "" {
		return domain.User{}, errors.New("acceptance credential was not stored")
	}
	if strings.TrimSpace(ident.TaihuPAT) != "" && strings.TrimSpace(user.TaihuPAT) == "" {
		return domain.User{}, errors.New("acceptance Taihu PAT was not stored")
	}
	if err := st.PutProject(domain.Project{
		ID:          assistantAcceptanceProjectID,
		Key:         "AST",
		Name:        "AI 助手验收",
		Description: "Disposable project for assistant acceptance tests",
		Color:       "#526cdb",
		OwnerID:     user.ID,
		Members:     []domain.ProjectMember{{UserID: user.ID, Role: "maintainer"}},
	}); err != nil {
		return domain.User{}, err
	}
	if err := st.PutSandbox(domain.SandboxProfile{
		ID:        assistantAcceptanceSandboxID,
		Name:      "助手验收沙箱",
		ProjectID: assistantAcceptanceProjectID,
		Driver:    "local",
	}); err != nil {
		return domain.User{}, err
	}
	return user, nil
}
