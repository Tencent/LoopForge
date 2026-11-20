package httpapi

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Workspace files and commands stay inside the sandbox and need no approval.
// Commands that write to systems outside it (pushes, PRs, publishing, HTTP
// writes) follow the conversation's approval mode. This is a guardrail
// against unintended external writes, not a security boundary: a script can
// still reach the network without matching any pattern.
var nativeExternalWrites = []struct {
	label string
	re    *regexp.Regexp
}{
	{"git push", regexp.MustCompile(`\bgit(\s+(-C|-c)\s+\S+|\s+--?[\w-]+(=\S+)?)*\s+push\b`)},
	{"GitHub CLI 写操作", regexp.MustCompile(`\bgh\s+(pr|issue|release|repo|gist|label|secret|variable|workflow|run|project|ruleset|cache)\s+(create|merge|close|reopen|comment|edit|review|delete|ready|lock|unlock|transfer|pin|unpin|develop|rename|archive|unarchive|fork|set|upload|enable|disable|cancel|rerun|sync|item-add|item-edit|item-delete)\b`)},
	{"GitHub API 写请求", regexp.MustCompile(`\bgh\s+api\b[^;&|\n]*(\s-X\s*|\s--method[\s=]+)['"]?(POST|PUT|PATCH|DELETE)\b|\bgh\s+api\b[^;&|\n]*\s(-f|-F|--field|--raw-field|--input)\b`)},
	{"GitLab CLI 写操作", regexp.MustCompile(`\bglab\s+(mr|issue|release|repo|label|variable|ci|schedule|snippet|milestone|incident|deploy-key|ssh-key|gpg-key|cluster|stack)\s+(create|merge|close|reopen|note|update|approve|revoke|delete|rebase|subscribe|unsubscribe|set|retry|run|cancel|trigger|play|fork|archive|transfer|upload|add|remove|save|sync|amend|push|reorder|move)\b`)},
	{"GitLab API 写请求", regexp.MustCompile(`\bglab\s+api\b[^;&|\n]*(\s-X\s*|\s--method[\s=]+)['"]?(POST|PUT|PATCH|DELETE)\b|\bglab\s+api\b[^;&|\n]*\s(-f|-F|--field|--raw-field|--input)\b`)},
	{"Gitea CLI 写操作", regexp.MustCompile(`\btea\s+(pulls?|pr|issues?|i|releases?|r|labels?|milestones?|ms|repos?|webhooks?|org|organizations?)\s+(create|c|merge|m|close|reopen|edit|e|delete|rm|approve|reject|review|fork|migrate)\b|\btea\s+(comment|c)\s`)},
	{"HTTP 写请求", regexp.MustCompile(`\bcurl\b[^;&|\n]*(\s-X\s*|\s--request[\s=]+)['"]?(POST|PUT|PATCH|DELETE)\b|\bcurl\b[^;&|\n]*\s(-d|--data[a-z-]*|-F|--form[a-z-]*|-T|--upload-file|--json)\b|\bwget\b[^;&|\n]*\s--(post-data|post-file|method|body-data|body-file)\b`)},
	{"发布软件包", regexp.MustCompile(`\b(npm|pnpm|yarn|bun|poetry|cargo)\s+publish\b|\btwine\s+upload\b|\bgem\s+push\b`)},
	{"推送镜像", regexp.MustCompile(`\b(docker|podman|buildah)\s+push\b|\bbuildx\s+build\b[^;&|\n]*\s--push\b`)},
	{"集群或基础设施变更", regexp.MustCompile(`\bkubectl\s+(apply|create|delete|patch|replace|scale|rollout|set|edit|annotate|label|drain|cordon)\b|\bhelm\s+(install|upgrade|uninstall|rollback|push)\b|\bterraform\s+(apply|destroy|import)\b`)},
}

// nativeExternalWrite reports whether a native tool call runs a command that
// writes outside the sandbox, and names the kind of write. Any tool counts
// when its input carries a command, so CLI-specific tool names do not matter.
func nativeExternalWrite(input json.RawMessage) (string, bool) {
	var value any
	if json.Unmarshal(input, &value) != nil {
		return "", false
	}
	text := strings.Join(nativeCommandTexts(value, false), "\n")
	if text == "" {
		return "", false
	}
	for _, rule := range nativeExternalWrites {
		if rule.re.MatchString(text) {
			return rule.label, true
		}
	}
	return "", false
}

func nativeCommandTexts(value any, command bool) []string {
	switch v := value.(type) {
	case string:
		if command {
			return []string{v}
		}
	case []any:
		parts := []string{}
		for _, item := range v {
			parts = append(parts, nativeCommandTexts(item, command)...)
		}
		if command {
			return []string{strings.Join(parts, " ")}
		}
		return parts
	case map[string]any:
		out := []string{}
		for key, item := range v {
			switch strings.ToLower(key) {
			case "command", "cmd", "commands", "argv", "args", "script", "commandline", "command_line":
				out = append(out, nativeCommandTexts(item, true)...)
			default:
				out = append(out, nativeCommandTexts(item, command)...)
			}
		}
		return out
	}
	return nil
}
