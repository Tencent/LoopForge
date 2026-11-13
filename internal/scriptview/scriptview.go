// Package scriptview provides the trusted bootstrap for SOP scripts sharing a
// live Docker sandbox. It is deliberately a separate mode of the existing
// static gate helper so installed runners do not need another runtime artifact.
//
// Only the platform invokes this mode, inside its dedicated helper container.
// Script execution clones the Agent container's mount namespace before removing
// platform instruction masks, and drops all bootstrap privileges before exec.
// A separate refresh mode may only add read-only masks to the canonical mount
// namespace; Agents themselves remain in the original hardened container.
package scriptview

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
)

const (
	Argument      = "--workspace-script-view"
	ProbeArgument = "--workspace-script-view-version"
	Version       = "agentflow-script-view-v3"
)

type Config struct {
	Aliases []string `json:"aliases,omitempty"`
	Masks   []string `json:"masks"`
	Hide    []string `json:"hide,omitempty"`
	Argv    []string `json:"argv"`
	Refresh bool     `json:"refresh,omitempty"`
}

// Run handles only private script-view modes. On Linux script execution replaces
// the process; refresh and version probes return. Callers must terminate the
// helper after a handled result, without falling back to unisolated execution.
func Run(args []string) (bool, error) {
	if len(args) > 0 && args[0] == ProbeArgument {
		if len(args) != 1 {
			return true, fmt.Errorf("workspace script view version probe accepts no arguments")
		}
		_, err := fmt.Fprintln(os.Stdout, Version)
		return true, err
	}
	if len(args) == 0 || args[0] != Argument {
		return false, nil
	}
	if len(args) != 2 {
		return true, fmt.Errorf("workspace script view requires one configuration argument")
	}
	config, err := parseConfig(args[1])
	if err != nil {
		return true, err
	}
	return true, execute(config)
}

func parseConfig(raw string) (Config, error) {
	var config Config
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode workspace script view configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return config, fmt.Errorf("workspace script view configuration must contain one JSON object")
	}
	if err := validateConfigMode(config); err != nil {
		return config, err
	}
	for _, alias := range config.Aliases {
		if !strings.HasPrefix(alias, "/") || path.Clean(alias) != alias || !strings.HasSuffix(alias, "/ws") || strings.ContainsAny(alias, "\x00\n\r") {
			return config, fmt.Errorf("invalid workspace alias %q", alias)
		}
	}
	if err := validateAliasedMaskLists(config.Aliases, config.Masks, config.Hide); err != nil {
		return config, err
	}
	// Children must be detached before their parent when a legacy container has
	// both a directory mask and a file mask below it.
	slices.SortFunc(config.Masks, maskOrder(true))
	slices.SortFunc(config.Hide, maskOrder(false))
	return config, nil
}

// validateConfigMode enforces that refresh requests never execute commands or
// detach masks, and that script executions never modify canonical masks.
func validateConfigMode(config Config) error {
	if config.Refresh {
		if len(config.Argv) != 0 || len(config.Masks) != 0 {
			return fmt.Errorf("instruction mask refresh cannot execute commands or detach masks")
		}
	} else {
		if len(config.Hide) != 0 {
			return fmt.Errorf("workspace script commands cannot modify canonical instruction masks")
		}
		if len(config.Argv) == 0 || config.Argv[0] == "" {
			return fmt.Errorf("workspace script view command is empty")
		}
	}
	for _, argument := range config.Argv {
		if strings.IndexByte(argument, 0) >= 0 {
			return fmt.Errorf("workspace script view command contains a null byte")
		}
	}
	return nil
}

// validateMaskLists rejects invalid or duplicate entries within each list.
func validateMaskLists(lists ...[]string) error {
	return validateAliasedMaskLists(nil, lists...)
}

// validateAliasedMaskLists is validateMaskLists for a view whose workspace is
// also mounted at the given conversation aliases.
func validateAliasedMaskLists(aliases []string, lists ...[]string) error {
	for _, list := range lists {
		seen := make(map[string]bool, len(list))
		for _, target := range list {
			if !ValidMask(target, aliases...) || seen[target] {
				return fmt.Errorf("invalid or duplicate workspace instruction mask %q", target)
			}
			seen[target] = true
		}
	}
	return nil
}

// maskOrder sorts mask paths by length (longest or shortest first), breaking
// ties lexically so the order is deterministic.
func maskOrder(longestFirst bool) func(a, b string) int {
	return func(a, b string) int {
		if len(a) != len(b) {
			if longestFirst {
				return len(b) - len(a)
			}
			return len(a) - len(b)
		}
		return strings.Compare(a, b)
	}
}

// ValidMask accepts only repository instruction paths that the platform masks.
// Runtime state, the workspace root and arbitrary mounts are never eligible.
func ValidMask(target string, aliases ...string) bool {
	base := maskBase(target, aliases)
	if base == "" || path.Clean(target) != target || strings.ContainsAny(target, "\x00\n\r") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(target, base+"/"), "/")
	for _, part := range parts[:len(parts)-1] {
		if part == ".agentflow" || part == ".git" {
			return false
		}
	}
	switch parts[len(parts)-1] {
	case "AGENTS.md", "AGENTS.override.md", "CLAUDE.md", "CLAUDE.local.md", "CODEBUDDY.md", ".mcp.json", ".agents", ".codex", ".claude", ".codebuddy":
		return true
	}
	return false
}

func maskBase(target string, aliases []string) string {
	for _, base := range append([]string{"/workspace"}, aliases...) {
		if strings.HasPrefix(target, base+"/") {
			return base
		}
	}
	return ""
}
