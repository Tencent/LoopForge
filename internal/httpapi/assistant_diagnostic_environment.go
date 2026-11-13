package httpapi

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
)

var assistantDiagnosticInstance = domain.AssistantDiagnosticEnvironment{
	PlatformVersion: "development", GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
	InstanceID: "api-" + newAssistantSessionID(), InstanceStartedAt: time.Now().UTC(),
}

func assistantDiagnosticEnvironment() domain.AssistantDiagnosticEnvironment {
	e := assistantDiagnosticInstance
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" {
			e.PlatformVersion = info.Main.Version
		}
		for _, item := range info.Settings {
			switch item.Key {
			case "vcs.revision":
				e.Revision = item.Value
			case "vcs.modified":
				modified := item.Value == "true"
				e.Modified = &modified
			}
		}
	}
	return e
}

var diagnosticVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)

// Read installation metadata rather than launching a second CLI process. A
// version probe can itself hang or mutate a continuation cache. The native
// handshake can fill the version later when installation metadata is absent.
func assistantDiagnosticCLIVersion(engine, bin string) (version, source string) {
	if root := strings.TrimSpace(os.Getenv("AGENTFLOW_MANAGED_CLI_ROOT")); root != "" {
		if inventory, err := managedcli.ReadInventory(root); err == nil {
			if install, ok := inventory[engine]; ok && install.Binary == bin && diagnosticVersion.MatchString(install.Version) {
				return install.Version, "managed_inventory"
			}
		}
	}
	packages := map[string]string{"codex": "@openai/codex", "codebuddy": "@tencent-ai/codebuddy-code", "claude-code": "@anthropic-ai/claude-code"}
	resolved, err := filepath.EvalSymlinks(bin)
	if err == nil && packages[engine] != "" {
		for dir, depth := filepath.Dir(resolved), 0; depth < 5; dir, depth = filepath.Dir(dir), depth+1 {
			info, err := os.Stat(filepath.Join(dir, "package.json"))
			if err != nil || info.Size() > 64<<10 {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
			var pkg struct{ Name, Version string }
			if err == nil && json.Unmarshal(raw, &pkg) == nil && pkg.Name == packages[engine] && diagnosticVersion.MatchString(pkg.Version) {
				return pkg.Version, "package_metadata"
			}
		}
	}
	return "", "not_reported"
}

func (a *assistantDiagnosticAttempt) configure(bin string, req assistantCLIRequest) {
	if a == nil {
		return
	}
	engine, _ := assistantModelEngine(assistantRequestModel(req.Executor, req.Model))
	version, source := assistantDiagnosticCLIVersion(engine, bin)
	endpoint, protocol := "", "stream-json"
	switch engine {
	case "codex":
		endpoint, protocol = a.user.CodexBaseURL, "responses / app-server JSON-RPC"
		if endpoint == "" {
			endpoint = "https://api.openai.com/v1"
		}
	case "claude-code":
		endpoint, protocol = a.user.ClaudeCodeBaseURL, "messages / stream-json"
		if endpoint == "" {
			endpoint = "https://api.anthropic.com"
		}
	case "codebuddy":
		// The CLI selects this endpoint internally. Do not guess a hostname.
		env := strings.TrimSpace(a.user.CodeBuddyInternetEnvironment)
		if env == "" {
			env = "default"
		}
		endpoint = "CLI-managed (" + env + ")"
	default:
		credential := a.user.AgentCLICredentials[engine]
		endpoint, protocol = credential.BaseURL, credential.Protocol
	}
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		endpoint = u.String()
	}
	binarySource := "discovered"
	key := "AGENTFLOW_" + strings.ToUpper(strings.ReplaceAll(engine, "-", "_")) + "_BIN"
	if engine == "claude-code" {
		key = "AGENTFLOW_CLAUDE_BIN"
	}
	if strings.TrimSpace(os.Getenv(key)) == bin {
		binarySource = "explicit_override"
	} else if source == "managed_inventory" {
		binarySource = "managed_inventory"
	} else if root := strings.TrimSpace(os.Getenv("AGENTFLOW_MANAGED_CLI_ROOT")); root != "" && strings.HasPrefix(bin, filepath.Clean(root)+string(filepath.Separator)) {
		binarySource = "managed_inventory"
	}
	a.update(func(attempt *domain.AssistantCLIAttempt) {
		attempt.CLIVersion, attempt.VersionSource = version, source
		attempt.BinarySource = binarySource
		attempt.Endpoint = sanitizeAssistantDiagnostic(endpoint, a.user, 300)
		attempt.Protocol = sanitizeAssistantDiagnostic(protocol, a.user, 80)
		attempt.Effort = sanitizeAssistantDiagnostic(req.Effort, a.user, 40)
	})
}
