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
	Version       = "agentflow-script-view-v2"
)

type Config struct {
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
	if config.Refresh {
		if len(config.Argv) != 0 || len(config.Masks) != 0 {
			return config, fmt.Errorf("instruction mask refresh cannot execute commands or detach masks")
		}
	} else {
		if len(config.Hide) != 0 {
			return config, fmt.Errorf("workspace script commands cannot modify canonical instruction masks")
		}
		if len(config.Argv) == 0 || config.Argv[0] == "" {
			return config, fmt.Errorf("workspace script view command is empty")
		}
	}
	for _, argument := range config.Argv {
		if strings.IndexByte(argument, 0) >= 0 {
			return config, fmt.Errorf("workspace script view command contains a null byte")
		}
	}
	for _, list := range [][]string{config.Masks, config.Hide} {
		seen := make(map[string]bool, len(list))
		for _, target := range list {
			if !ValidMask(target) || seen[target] {
				return config, fmt.Errorf("invalid or duplicate workspace instruction mask %q", target)
			}
			seen[target] = true
		}
	}
	// Children must be detached before their parent when a legacy container has
	// both a directory mask and a file mask below it.
	slices.SortFunc(config.Masks, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	slices.SortFunc(config.Hide, func(a, b string) int {
		if len(a) != len(b) {
			return len(a) - len(b)
		}
		return strings.Compare(a, b)
	})
	return config, nil
}

// ValidMask accepts only repository instruction paths that the platform masks.
// Runtime state, the workspace root and arbitrary mounts are never eligible.
func ValidMask(target string) bool {
	if !strings.HasPrefix(target, "/workspace/") || path.Clean(target) != target || strings.ContainsAny(target, "\x00\n\r") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(target, "/workspace/"), "/")
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
