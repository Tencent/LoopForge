package domain

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

const ProjectCredentialEnvLimit = 64 * 1024

const (
	ProjectCredentialPassword = "username-password"
	ProjectCredentialToken    = "token"
	ProjectCredentialAKSK     = "aksk"
	ProjectCredentialCustom   = "custom"
)

// ProjectCredential contains only information safe to return to project members.
type ProjectCredential struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Variables []string  `json:"variables"`
	Disabled  bool      `json:"disabled"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ProjectCredentialInput struct {
	Name     string                           `json:"name"`
	Kind     string                           `json:"kind"`
	Fields   []ProjectCredentialVariableInput `json:"fields"`
	Disabled bool                             `json:"disabled"`
}

type ProjectCredentialVariableInput struct {
	Name         string  `json:"name"`
	PreviousName string  `json:"previousName,omitempty"`
	Value        *string `json:"value,omitempty"`
}

var projectCredentialVariableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

var projectCredentialReservedNames = []string{
	"HOME", "USERPROFILE", "PATH", "PWD", "OLDPWD", "SHELL", "TMPDIR", "TMP", "TEMP", "IFS", "ENV", "BASH_ENV", "ZDOTDIR",
	"PYTHONPATH", "PYTHONHOME", "NODE_OPTIONS", "RUBYOPT", "PERL5OPT", "JAVA_TOOL_OPTIONS",
	"GITHUB_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GITLAB_TOKEN", "GLAB_TOKEN", "TEA_TOKEN", "CNB_TOKEN",
	"GIT_ASKPASS", "GIT_EXEC_PATH", "GIT_PROXY_COMMAND", "SSH_ASKPASS", "SSH_AUTH_SOCK",
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	"GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_GEMINI_BASE_URL",
	"MOONSHOT_API_KEY", "DSH_HOME", "PI_CODING_AGENT_DIR",
}

var projectCredentialReservedPrefixes = []string{
	"AGENTFLOW_", "LOOPFORGE_", "LD_", "DYLD_", "GIT_CONFIG", "GIT_SSH", "GH_", "OTEL_",
	"AGY_", "ANTHROPIC_", "CLAUDE_", "CODEBUDDY_", "CODEX_", "CURSOR_", "DEEPSEEK_",
	"GEMINI_", "KIMI_", "OPENAI_", "OPENCODE_", "ZCODE_",
}

func ValidateProjectCredentialVariable(name string) error {
	if !projectCredentialVariableName.MatchString(name) {
		return fmt.Errorf("环境变量名无效")
	}
	// Credential names must also be safe on case-insensitive execution hosts.
	key := strings.ToUpper(name)
	if slices.Contains(projectCredentialReservedNames, key) {
		return fmt.Errorf("环境变量名 %s 由沙箱保留", name)
	}
	for _, prefix := range projectCredentialReservedPrefixes {
		if strings.HasPrefix(key, prefix) {
			return fmt.Errorf("环境变量名 %s 由沙箱保留", name)
		}
	}
	return nil
}

func (v ProjectCredentialInput) Validate() error {
	name := strings.TrimSpace(v.Name)
	if count := len([]rune(name)); count < 1 || count > 80 {
		return fmt.Errorf("请输入 1 到 80 个字符的凭据名称")
	}
	count := len(v.Fields)
	switch v.Kind {
	case ProjectCredentialPassword:
		if count != 2 {
			return fmt.Errorf("账号密码需要填写账号和密码")
		}
	case ProjectCredentialToken:
		if count != 1 {
			return fmt.Errorf("API Token / API Key 需要填写一个变量")
		}
	case ProjectCredentialAKSK:
		if count != 2 && count != 3 {
			return fmt.Errorf("AK/SK 需要填写访问密钥和密钥值，可添加 Session Token")
		}
	case ProjectCredentialCustom:
		if count < 1 || count > 16 {
			return fmt.Errorf("自定义凭据需要 1 到 16 个变量")
		}
	default:
		return fmt.Errorf("请选择有效的凭据类型")
	}
	names := make(map[string]bool, count)
	for _, field := range v.Fields {
		if err := ValidateProjectCredentialVariable(field.Name); err != nil {
			return err
		}
		key := strings.ToUpper(field.Name)
		if names[key] {
			return fmt.Errorf("环境变量 %s 重复", field.Name)
		}
		names[key] = true
	}
	return nil
}

// ProjectCredentialAccess applies the credential policy even for shared or
// ownerless projects. Anonymous callers do not get access to project secrets.
func ProjectCredentialAccess(project Project, userID int64, platformAdmin bool) (use, manage bool) {
	if userID <= 0 || project.ID == "" || project.ArchivedAt != nil {
		return false, false
	}
	if platformAdmin || project.OwnerID == userID {
		return true, true
	}
	for _, member := range project.Members {
		if member.UserID == userID {
			role := strings.ToLower(strings.TrimSpace(member.Role))
			return role == "member" || role == "maintainer", role == "maintainer"
		}
	}
	return false, false
}
