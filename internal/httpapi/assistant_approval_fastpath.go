package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"agentflow/internal/domain"
)

const (
	assistantApprovalRuleModel            = "rule"
	assistantApprovalPolicyModel          = "policy"
	assistantApprovalMaxArgRunes          = 600
	assistantApprovalArgHeadRunes         = 300
	assistantApprovalArgTailRunes         = 150
	assistantApprovalMaxArgSliceItems     = 20
	assistantApprovalArgSliceHeadItems    = 15
	assistantApprovalMaxArgDepth          = 6
	assistantApprovalMaxConsecutiveDenies = 3
)

// evaluateApprovalFastPath settles strictly safe, low-risk, reversible actions
// with zero LLM tokens before invoking the review model. It never hard-denies:
// every non-trivially-safe or potentially risky action is delegated to AI
// review so user intent and conversation authorization are always considered.
func (h *assistantHandlers) evaluateApprovalFastPath(tool string, args map[string]any) (assistantApprovalReview, bool) {
	tool = strings.TrimSpace(tool)
	switch tool {
	case "issues_comments_add":
		return assistantApprovalReview{
			Outcome:           "allow",
			RiskLevel:         "low",
			UserAuthorization: "high",
			Model:             assistantApprovalRuleModel,
			Rationale:         "项目内常规评论写入，范围明确且可逆",
		}, true
	case "workbench_workspace_command":
		cmd := approvalNestedArgString(args, "command")
		if approvalCommandSafeReadOrTest(cmd) {
			return assistantApprovalReview{
				Outcome:           "allow",
				RiskLevel:         "low",
				UserAuthorization: "high",
				Model:             assistantApprovalRuleModel,
				Rationale:         "工作区只读检查或本地测试命令，无外部副作用",
			}, true
		}
	case "workbench_workspace_write":
		target := approvalNestedArgString(args, "path")
		if approvalWorkspaceWriteSafePath(target) {
			return assistantApprovalReview{
				Outcome:           "allow",
				RiskLevel:         "low",
				UserAuthorization: "high",
				Model:             assistantApprovalRuleModel,
				Rationale:         "工作区内常规源码或文档修改，影响局限于工作区",
			}, true
		}
	}
	return assistantApprovalReview{}, false
}

func approvalNestedArgString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if s, ok := args[key].(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	if nested, ok := args["arguments"].(map[string]any); ok {
		if s, ok := nested[key].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func approvalCommandSafeReadOrTest(command string) bool {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false
	}
	for _, meta := range []string{"\n", "\r", "\x00", ";", "&&", "||", "|", "`", "$(", ">", "<", "&", "\\"} {
		if strings.Contains(cmd, meta) {
			return false
		}
	}
	lower := strings.ToLower(cmd)
	for _, sensitive := range []string{".env", "id_rsa", "id_ed25519", "/etc/", "/proc/", "/sys/", "..", "secret", "token", "credential", "passwd", "shadow"} {
		if strings.Contains(lower, sensitive) {
			return false
		}
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "pwd":
		return len(fields) == 1
	case "ls":
		for _, arg := range fields[1:] {
			if strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~") {
				return false
			}
		}
		return true
	case "git":
		if len(fields) < 2 {
			return false
		}
		for _, arg := range fields[2:] {
			low := strings.ToLower(arg)
			if low == "-c" || strings.HasPrefix(low, "--exec-path") || strings.HasPrefix(low, "--git-dir") ||
				strings.HasPrefix(low, "--work-tree") || strings.HasPrefix(low, "--output") || low == "-o" {
				return false
			}
		}
		switch fields[1] {
		case "status", "diff", "log", "show", "rev-parse":
			return true
		case "branch":
			for _, arg := range fields[2:] {
				switch arg {
				case "--show-current", "-a", "--all", "-r", "--remotes", "-v", "-vv", "--list":
				default:
					return false
				}
			}
			return true
		}
	case "go":
		if len(fields) < 2 {
			return false
		}
		for _, arg := range fields[2:] {
			low := strings.ToLower(arg)
			if strings.HasPrefix(low, "-exec") || strings.HasPrefix(low, "-toolexec") {
				return false
			}
		}
		switch fields[1] {
		case "test", "vet", "list", "version", "build":
			return true
		}
	case "npm", "pnpm", "yarn":
		if len(fields) < 2 {
			return false
		}
		for _, arg := range fields[2:] {
			low := strings.ToLower(arg)
			if low == "--eval" || low == "-e" {
				return false
			}
		}
		if fields[1] == "test" {
			return true
		}
		if fields[1] == "run" && len(fields) >= 3 {
			switch fields[2] {
			case "test", "lint", "check", "build", "typecheck":
				return true
			}
		}
	case "pytest":
		return true
	case "cargo":
		if len(fields) >= 2 && (fields[1] == "test" || fields[1] == "check") {
			return true
		}
	}
	return false
}

var approvalSensitiveWriteNames = map[string]bool{
	"dockerfile":        true,
	"makefile":          true,
	"justfile":          true,
	"procfile":          true,
	"package.json":      true,
	"package-lock.json": true,
	"pnpm-lock.yaml":    true,
	"yarn.lock":         true,
	"go.mod":            true,
	"go.sum":            true,
	"compose.yaml":      true,
	"compose.yml":       true,
	"docker-compose.yaml": true,
	"docker-compose.yml":  true,
}

var approvalSensitiveWriteExts = map[string]bool{
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".ps1": true, ".bat": true, ".cmd": true,
	".pem": true, ".key": true, ".p12": true, ".pfx": true, ".crt": true, ".cer": true, ".env": true,
}

func approvalWorkspaceWriteSafePath(raw string) bool {
	target := strings.TrimSpace(raw)
	if target == "" || strings.ContainsAny(target, "\x00\n\r") {
		return false
	}
	normalized := strings.ReplaceAll(target, "\\", "/")
	if strings.HasPrefix(normalized, "/") {
		return false
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return false
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return false
		}
	}
	base := strings.ToLower(path.Base(cleaned))
	if approvalSensitiveWriteNames[base] || strings.HasPrefix(base, "dockerfile.") || strings.HasPrefix(base, "compose.") || strings.HasPrefix(base, "docker-compose.") {
		return false
	}
	if approvalSensitiveWriteExts[strings.ToLower(path.Ext(base))] {
		return false
	}
	return true
}

// summarizeApprovalArguments compacts large argument strings and arrays while
// preserving identifying fields (paths, IDs, commands, flags) and redacting
// credentials before sending the payload to the reviewer model.
func summarizeApprovalArguments(args map[string]any, user domain.User) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = summarizeApprovalValue(v, user, 0)
	}
	return out
}

func summarizeApprovalValue(v any, user domain.User, depth int) any {
	if depth >= assistantApprovalMaxArgDepth {
		return "[已省略深层嵌套内容]"
	}
	switch val := v.(type) {
	case string:
		redacted := redactAgentToolResult(val, user)
		return compactApprovalString(redacted)
	case json.RawMessage:
		var decoded any
		if len(val) > 0 && json.Unmarshal(val, &decoded) == nil {
			return summarizeApprovalValue(decoded, user, depth+1)
		}
		return compactApprovalString(redactAgentToolResult(string(val), user))
	case []byte:
		return compactApprovalString(redactAgentToolResult(string(val), user))
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[k] = summarizeApprovalValue(item, user, depth+1)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(val))
		for k, item := range val {
			out[k] = compactApprovalString(redactAgentToolResult(item, user))
		}
		return out
	case []any:
		if len(val) <= assistantApprovalMaxArgSliceItems {
			out := make([]any, len(val))
			for i, item := range val {
				out[i] = summarizeApprovalValue(item, user, depth+1)
			}
			return out
		}
		out := make([]any, 0, assistantApprovalArgSliceHeadItems+1)
		for i := range assistantApprovalArgSliceHeadItems {
			out = append(out, summarizeApprovalValue(val[i], user, depth+1))
		}
		out = append(out, fmt.Sprintf("…[共 %d 项，已省略其余 %d 项]", len(val), len(val)-assistantApprovalArgSliceHeadItems))
		return out
	default:
		return v
	}
}

func compactApprovalString(s string) string {
	runes := []rune(s)
	if len(runes) <= assistantApprovalMaxArgRunes {
		return s
	}
	omitted := len(runes) - assistantApprovalArgHeadRunes - assistantApprovalArgTailRunes
	return string(runes[:assistantApprovalArgHeadRunes]) +
		fmt.Sprintf("…[已省略 %d 字符]…", omitted) +
		string(runes[len(runes)-assistantApprovalArgTailRunes:])
}

func approvalActionFingerprint(tool string, args map[string]any) string {
	raw, err := json.Marshal(struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args,omitempty"`
	}{Tool: strings.TrimSpace(tool), Args: args})
	if err != nil {
		return strings.TrimSpace(tool)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
