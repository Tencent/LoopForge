package managedcli

import (
	"sort"
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
	Package string
	Binary  string
	Help    []string
	Flags   []string
}

var providerSpecs = map[string]providerSpec{
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
