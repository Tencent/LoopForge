package managedcli

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Update safety bounds are fixed; no task, local policy or environment may
// change the registry, supported packages, or observation and timeout windows.
const (
	npmRegistry        = "https://registry.npmjs.org"
	observationWindow  = 24 * time.Hour
	maintenanceTimeout = 30 * time.Minute
	commandTimeout     = 10 * time.Minute
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

func providerNames() []string {
	names := make([]string, 0, len(providerSpecs))
	for name := range providerSpecs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
