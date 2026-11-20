package managedcli

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Update safety bounds are fixed; no task, local policy or environment may
// change the registry, supported packages, or observation and timeout windows.
const (
	npmRegistry          = "https://registry.npmjs.org"
	observationWindow    = 24 * time.Hour
	maintenanceTimeout   = 30 * time.Minute
	commandTimeout       = 10 * time.Minute
	providerAllowlistEnv = "AGENTFLOW_MANAGED_CLIS"
)

type providerSpec struct {
	Package      string
	Binary       string
	Help         []string
	Flags        []string
	RelaxEngines bool
}

var providerSpecs = map[string]providerSpec{
	"agy": {
		Binary: "agy", Help: []string{"--help"},
		Flags: []string{"--output-format", "--model", "--settings", "--conversation", "--disable-slash-commands"},
	},
	"cursor": {
		Binary: "cursor-agent", Help: []string{"--help"},
		Flags: []string{"--output-format", "--stream-partial-output", "--model", "--resume"},
	},
	"dsh": {
		Package: "@deepseek-ai/dsh", Binary: "dsh", Help: []string{"--profile", "headless", "--help"},
		Flags: []string{"--json", "--permission-mode", "--approval-policy", "--session-id"},
		// The runtime already uses DSH on Node 22. Probe the actual binary
		// on this worker before activating a release that declares Node 24.
		RelaxEngines: true,
	},
	"kimi": {
		Package: "@moonshot-ai/kimi-code", Binary: "kimi", Help: []string{"--help"},
		Flags: []string{"--output-format", "--agent-file", "--session"},
	},
	"opencode": {
		Package: "opencode-ai", Binary: "opencode", Help: []string{"run", "--help"},
		Flags: []string{"--format", "--thinking", "--model", "--session"},
	},
	"pi": {
		Package: "@earendil-works/pi-coding-agent", Binary: "pi", Help: []string{"--help"},
		Flags: []string{"--mode", "--session-id", "--model", "--tools", "--exclude-tools", "--append-system-prompt"},
	},
	"codex": {
		Package: "@openai/codex", Binary: "codex", Help: []string{"exec", "--help"},
		Flags: []string{"--json", "--sandbox", "--skip-git-repo-check", "--model"},
	},
	"codebuddy": {
		Package: "@tencent-ai/codebuddy-code", Binary: "codebuddy", Help: []string{"--help"},
		Flags: []string{"--print", "--output-format", "--permission-mode", "--setting-sources", "--system-prompt-file", "--mcp-config", "--strict-mcp-config", "--settings", "--resume", "--plugin-dir"},
	},
	"claude-code": {
		Package: "@anthropic-ai/claude-code", Binary: "claude", Help: []string{"--help"},
		Flags: []string{"--print", "--output-format", "--verbose", "--permission-mode", "--setting-sources", "--system-prompt-file", "--mcp-config", "--settings", "--resume", "--plugin-dir"},
	},
}

// EnabledProviders returns the deployment's update allowlist, ignoring empty
// entries. A list with no names uses the defaults. Existing installations and
// rollback remain accessible when a provider is excluded.
func EnabledProviders() ([]string, error) {
	selected := map[string]struct{}{}
	for _, item := range strings.Split(os.Getenv(providerAllowlistEnv), ",") {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		if !Supports(name) {
			return nil, fmt.Errorf("%s contains unsupported CLI %q", providerAllowlistEnv, name)
		}
		selected[name] = struct{}{}
	}
	if len(selected) == 0 {
		return []string{"claude-code", "codebuddy", "codex"}, nil
	}
	providers := make([]string, 0, len(selected))
	for name := range selected {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	return providers, nil
}

// AutoUpdateEnvironment disables a provider's built-in updater during managed execution.
// Pi replaces its CLI through the explicit pi update command. The shipped DSH
// headless profile relies on package-manager updates for the CLI.
func AutoUpdateEnvironment(provider string) map[string]string {
	switch provider {
	case "claude-code", "codebuddy":
		return map[string]string{"DISABLE_AUTOUPDATER": "1", "DISABLE_UPDATES": "1"}
	case "opencode":
		return map[string]string{"OPENCODE_DISABLE_AUTOUPDATE": "1"}
	case "kimi":
		return map[string]string{"KIMI_CODE_NO_AUTO_UPDATE": "1"}
	}
	return nil
}

var cursorBuild = regexp.MustCompile(`^[0-9]{4}\.[0-9]{2}\.[0-9]{2}-[a-f0-9]{7,40}$`)
var releaseCandidateNumber = regexp.MustCompile("^(0|[1-9][0-9]*)$")

func validProviderVersion(provider, version string) bool {
	if provider == "cursor" {
		if !cursorBuild.MatchString(version) {
			return false
		}
		_, err := time.Parse("2006.01.02", version[:10])
		return err == nil
	}
	if provider == "dsh" {
		if core, candidate, ok := strings.Cut(version, "-rc."); ok {
			return releaseVersion.MatchString(core) && releaseCandidateNumber.MatchString(candidate)
		}
	}
	return releaseVersion.MatchString(version)
}

// Supports reports whether the platform updater owns this provider.
func Supports(provider string) bool {
	_, ok := providerSpecs[provider]
	return ok
}
