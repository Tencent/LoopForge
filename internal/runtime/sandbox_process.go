package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

// projectAgentPaths are deliberately absent from the executor's filesystem
// view. AgentFlow definitions are the only source of instructions, skills and
// CLI configuration for unattended runs.
var rootProjectAgentPaths = []string{
	"AGENTS.md",
	"AGENTS.override.md",
	"CLAUDE.md",
	"CLAUDE.local.md",
	"CODEBUDDY.md",
	".agents",
	".codex",
	".claude",
	".codebuddy",
	".mcp.json",
}

var projectAgentDirNames = map[string]bool{
	".agents": true, ".codex": true, ".claude": true, ".codebuddy": true,
}

var projectAgentFileNames = map[string]bool{
	"AGENTS.md": true, "AGENTS.override.md": true,
	"CLAUDE.md": true, "CLAUDE.local.md": true,
	"CODEBUDDY.md": true, ".mcp.json": true,
}

func discoverProjectAgentPaths(workspace string) ([]string, error) {
	seen := map[string]bool{}
	for _, rel := range rootProjectAgentPaths {
		seen[filepath.Clean(rel)] = true
	}
	err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil || rel == "." {
			return err
		}
		name := entry.Name()
		if projectAgentDirNames[name] {
			seen[rel] = true
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if name == ".git" || name == ".agentflow" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if projectAgentFileNames[name] {
			seen[rel] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

func validateAgentConfigTarget(workspace, rel string) (os.FileInfo, error) {
	target := filepath.Join(workspace, rel)
	info, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return info, nil
	}
	if projectAgentDirNames[filepath.Base(rel)] {
		return nil, fmt.Errorf("refusing symlinked Agent CLI directory: %s", target)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, err
	}
	inside, err := filepath.Rel(root, resolved)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(os.PathSeparator)) || !projectAgentFileNames[filepath.Base(resolved)] {
		return nil, fmt.Errorf("refusing Agent CLI configuration symlink outside the known project configuration set: %s", target)
	}
	return os.Stat(target)
}

func appendEnvironment(base []string, overlays ...map[string]string) []string {
	values := map[string]string{}
	order := make([]string, 0, len(base))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = item
	}
	for _, overlay := range overlays {
		keys := make([]string, 0, len(overlay))
		for key := range overlay {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if _, exists := values[key]; !exists {
				order = append(order, key)
			}
			values[key] = key + "=" + overlay[key]
		}
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, values[key])
	}
	return out
}

func controlledBaseEnvironment(base []string) []string {
	blocked := []string{
		"CODEBUDDY_API_KEY=",
		"OPENAI_API_KEY=", "CODEX_API_KEY=", "OPENAI_BASE_URL=",
		"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "ANTHROPIC_BASE_URL=",
		"CLAUDE_CODE_OAUTH_TOKEN=", "CODEX_HOME=", "CLAUDE_CONFIG_DIR=",
		"CLAUDE_CODE_USE_BEDROCK=", "CLAUDE_CODE_USE_VERTEX=", "CLAUDE_CODE_USE_FOUNDRY=",
		"CODEBUDDY_INTERNET_ENVIRONMENT=",
		"AGENTFLOW_GIT_TOKEN=",
		"AGENTFLOW_MCP_CONFIG=",
		"AGENTFLOW_BIN=",
		"GIT_CONFIG_COUNT=",
		"GIT_CONFIG_KEY_",
		"GIT_CONFIG_VALUE_",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
	}
	filtered := make([]string, 0, len(base))
	for _, entry := range base {
		drop := false
		for _, prefix := range blocked {
			if strings.HasPrefix(entry, prefix) {
				drop = true
				break
			}
		}
		if !drop {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

type processSandbox struct {
	id, workspace string
	driver        string
	profile       domain.SandboxProfile
	requirements  SandboxRequirements
	binary        string
}

func (s *processSandbox) ID() string                  { return s.id }
func (s *processSandbox) Workspace() string           { return s.workspace }
func (s *processSandbox) Driver() string              { return s.driver }
func (s *processSandbox) Close(context.Context) error { return nil }

func createSeatbelt(id, workspace string, profile domain.SandboxProfile, requirements SandboxRequirements) (Sandbox, error) {
	if goruntime.GOOS != "darwin" {
		return nil, fmt.Errorf("seatbelt sandbox requires macOS")
	}
	binary, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return nil, fmt.Errorf("seatbelt sandbox unavailable: %w", err)
	}
	return &processSandbox{id: id, workspace: workspace, driver: "seatbelt", profile: profile, requirements: requirements, binary: binary}, nil
}

func createBwrap(id, workspace string, profile domain.SandboxProfile, requirements SandboxRequirements) (Sandbox, error) {
	if goruntime.GOOS != "linux" {
		return nil, fmt.Errorf("bwrap sandbox requires Linux")
	}
	binary, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bwrap sandbox unavailable: %w", err)
	}
	return &processSandbox{id: id, workspace: workspace, driver: "bwrap", profile: profile, requirements: requirements, binary: binary}, nil
}

func (s *processSandbox) Exec(ctx context.Context, command CommandSpec, sink EventSink) (string, error) {
	cmd, err := s.command(ctx, command, controlledBaseEnvironment(os.Environ()))
	if err != nil {
		return "", err
	}
	return runCommand(cmd, sink)
}

func (s *processSandbox) command(ctx context.Context, command CommandSpec, base []string) (*exec.Cmd, error) {
	if len(command.Argv) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	var cmd *exec.Cmd
	switch s.driver {
	case "seatbelt":
		profile, err := seatbeltProfileCommand(s.workspace, s.requirements, !trustedScriptCommand(command))
		if err != nil {
			return nil, err
		}
		args := append([]string{"-p", profile}, command.Argv...)
		cmd = exec.CommandContext(ctx, s.binary, args...)
	case "bwrap":
		args, err := bwrapArgsCommand(s.workspace, s.requirements, !trustedScriptCommand(command))
		if err != nil {
			return nil, err
		}
		args = append(args, command.Argv...)
		cmd = exec.CommandContext(ctx, s.binary, args...)
	default:
		return nil, fmt.Errorf("unsupported process sandbox %q", s.driver)
	}
	cmd.Dir = s.workspace
	cmd.Env = appendEnvironment(base, s.profile.Environment, command.environment)
	switch s.driver {
	case "seatbelt":
		cmd.Env = upsertEnv(cmd.Env, "TMPDIR", preferredSeatbeltTemp())
	case "bwrap":
		for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
			cmd.Env = upsertEnv(cmd.Env, key, "/tmp")
		}
	}
	return cmd, nil
}

func seatbeltWritableTemps() []string {
	seen := map[string]bool{}
	var paths []string
	candidates := []string{"/private/tmp"}
	if goruntime.GOOS == "darwin" {
		// macOS frameworks and native clients also use per-user cache and
		// temporary siblings below /private/var/folders. Restricting the
		// allowance to os.TempDir() (.../T) breaks Codex TLS trust evaluation
		// with UnknownIssuer even though ordinary curl requests still work.
		candidates = append(candidates, "/private/var/folders")
	}
	// Keep the concrete TMPDIR last so preferredSeatbeltTemp returns it rather
	// than the broader per-user container root.
	candidates = append(candidates, os.TempDir())
	for _, candidate := range candidates {
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			candidate = resolved
		}
		candidate, _ = filepath.Abs(candidate)
		candidate = filepath.Clean(candidate)
		if candidate != "." && candidate != string(filepath.Separator) && !seen[candidate] {
			seen[candidate] = true
			paths = append(paths, candidate)
		}
	}
	return paths
}

func preferredSeatbeltTemp() string {
	paths := seatbeltWritableTemps()
	if len(paths) > 1 {
		return paths[len(paths)-1]
	}
	if len(paths) == 1 {
		return paths[0]
	}
	return "/private/tmp"
}

func agentStateDirectoryRequirements(requirements SandboxRequirements) (string, error) {
	directories, err := agentStateDirectories(requirements)
	if err != nil || len(directories) == 0 {
		return "", err
	}
	return directories[0].path, nil
}

type resolvedCLIState struct {
	path  string
	files []string
}

func agentStateDirectories(requirements SandboxRequirements) ([]resolvedCLIState, error) {
	states := append([]CLIStateRequirement(nil), requirements.CLIStates...)
	if len(states) == 0 {
		return nil, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	result := make([]resolvedCLIState, 0, len(states))
	seen := map[string]bool{}
	for _, state := range states {
		name := strings.TrimSpace(state.Directory)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, resolvedCLIState{path: filepath.Join(home, name), files: append([]string(nil), state.Files...)})
	}
	return result, nil
}

func seatbeltProfileRequirements(workspace string, requirements SandboxRequirements) (string, error) {
	return seatbeltProfileCommand(workspace, requirements, true)
}

func seatbeltProfileCommand(workspace string, requirements SandboxRequirements, maskAgentConfig bool) (string, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")
	// A broad write denial plus a narrower workspace allowance keeps the host
	// immutable while retaining the normal coding-agent workflow.
	b.WriteString("(deny file-write*)\n")
	fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", strconv.Quote(root))
	for _, temp := range seatbeltWritableTemps() {
		fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", strconv.Quote(temp))
	}
	stateDirs, err := agentStateDirectories(requirements)
	if err != nil {
		return "", err
	}
	for _, state := range stateDirs {
		fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", strconv.Quote(state.path))
	}
	b.WriteString("(allow file-write* (literal \"/dev/null\"))\n")
	if !maskAgentConfig {
		return b.String(), nil
	}
	maskedPaths, err := discoverProjectAgentPaths(root)
	if err != nil {
		return "", err
	}
	for _, rel := range maskedPaths {
		path := filepath.Join(root, rel)
		if _, err := os.Lstat(path); err == nil {
			if _, err := validateAgentConfigTarget(root, rel); err != nil {
				return "", err
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if projectAgentDirNames[filepath.Base(rel)] {
			fmt.Fprintf(&b, "(deny file-read* (subpath %s))\n", strconv.Quote(path))
		} else {
			fmt.Fprintf(&b, "(deny file-read* (literal %s))\n", strconv.Quote(path))
		}
	}
	return b.String(), nil
}

func bwrapArgsRequirements(workspace string, requirements SandboxRequirements) ([]string, error) {
	return bwrapArgsCommand(workspace, requirements, true)
}

func bwrapArgsCommand(workspace string, requirements SandboxRequirements, maskAgentConfig bool) ([]string, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	args := []string{
		"--die-with-parent", "--new-session",
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--ro-bind", "/", "/",
		"--bind", root, root,
		"--chdir", root,
		"--tmpfs", "/tmp",
		"--proc", "/proc",
		"--dev-bind", "/dev", "/dev",
	}
	if requirements.BunFS {
		args = append(args, "--tmpfs", "/$bunfs")
	}
	stateDirs, err := agentStateDirectories(requirements)
	if err != nil {
		return nil, err
	}
	for _, state := range stateDirs {
		if info, statErr := os.Stat(state.path); statErr == nil && info.IsDir() {
			args = append(args, "--bind", state.path, state.path)
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return nil, statErr
		}
	}
	if maskAgentConfig {
		maskArgs, err := bwrapAgentConfigMasks(root)
		if err != nil {
			return nil, err
		}
		args = append(args, maskArgs...)
	}
	args = append(args, "--")
	return args, nil
}

func isolationMaskSources(workspace string) (emptyDir, emptyFile string, err error) {
	return isolationMaskSourcesAt(filepath.Join(workspace, ".agentflow", "isolation"))
}

func isolationMaskSourcesAt(root string) (emptyDir, emptyFile string, err error) {
	if err = os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return "", "", err
	}
	if err = ensureDirectoryNoSymlink(root, 0o700); err != nil {
		return "", "", err
	}
	emptyDir = filepath.Join(root, "empty-dir")
	emptyFile = filepath.Join(root, "empty-file")
	if err = ensureDirectoryNoSymlink(emptyDir, 0o700); err != nil {
		return "", "", err
	}
	if info, statErr := os.Lstat(emptyFile); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return "", "", fmt.Errorf("sandbox mask path is not a regular file: %s", emptyFile)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", "", statErr
	}
	file, openErr := os.OpenFile(emptyFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if openErr != nil {
		return "", "", openErr
	}
	if err = file.Close(); err != nil {
		return "", "", err
	}
	return emptyDir, emptyFile, nil
}

func bwrapAgentConfigMasks(workspace string) ([]string, error) {
	emptyDir, emptyFile, err := isolationMaskSources(workspace)
	if err != nil {
		return nil, err
	}
	args := []string{"--ro-bind", emptyDir, emptyDir, "--ro-bind", emptyFile, emptyFile}
	maskedPaths, err := discoverProjectAgentPaths(workspace)
	if err != nil {
		return nil, err
	}
	for _, rel := range maskedPaths {
		target := filepath.Join(workspace, rel)
		info, statErr := validateAgentConfigTarget(workspace, rel)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return nil, statErr
		}
		source := emptyFile
		if info.IsDir() {
			source = emptyDir
		}
		args = append(args, "--ro-bind", source, target)
	}
	return args, nil
}

func dockerIsolationMaskRoot(workspace string) string {
	workspace = filepath.Clean(workspace)
	return filepath.Join(filepath.Dir(workspace), ".isolation-masks", filepath.Base(workspace))
}

func dockerAgentConfigMasks(workspace string) ([]string, error) {
	emptyDir, emptyFile, err := isolationMaskSourcesAt(dockerIsolationMaskRoot(workspace))
	if err != nil {
		return nil, err
	}
	emptyDirTarget := filepath.ToSlash(filepath.Join("/workspace", ".agentflow", "isolation", "empty-dir"))
	emptyFileTarget := filepath.ToSlash(filepath.Join("/workspace", ".agentflow", "isolation", "empty-file"))
	args := []string{
		"--mount", "type=bind,src=" + emptyDir + ",dst=" + emptyDirTarget + ",readonly",
		"--mount", "type=bind,src=" + emptyFile + ",dst=" + emptyFileTarget + ",readonly",
	}
	maskedPaths, err := discoverProjectAgentPaths(workspace)
	if err != nil {
		return nil, err
	}
	for _, rel := range maskedPaths {
		info, statErr := validateAgentConfigTarget(workspace, rel)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return nil, statErr
		}
		source := emptyFile
		if info.IsDir() {
			source = emptyDir
		}
		containerTarget := filepath.ToSlash(filepath.Join("/workspace", rel))
		args = append(args, "--mount", "type=bind,src="+source+",dst="+containerTarget+",readonly")
	}
	return args, nil
}

// dockerAgentStateMount exposes only the state directory belonging to the CLI
// selected for this invocation. Authentication and session state stay in the
// operator's normal CLI home; they are never copied into the workspace, stored
// in AgentFlow definitions, or passed through docker exec environment flags.
// Command nodes and unknown executables receive no Agent CLI state mount.
func dockerAgentStateMountRequirements(requirements SandboxRequirements) ([]string, error) {
	stateDirs, err := agentStateDirectories(requirements)
	if err != nil || len(stateDirs) == 0 {
		return nil, err
	}
	var args []string
	for _, state := range stateDirs {
		info, statErr := os.Stat(state.path)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return nil, statErr
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("Agent CLI state path is not a directory: %s", state.path)
		}
		target := filepath.ToSlash(filepath.Join("/root", filepath.Base(state.path)))
		args = append(args, "--mount", "type=bind,src="+state.path+",dst="+target)
		for _, name := range state.files {
			stateFile := filepath.Join(filepath.Dir(state.path), name)
			fileInfo, fileErr := os.Stat(stateFile)
			if fileErr == nil {
				if !fileInfo.Mode().IsRegular() {
					return nil, fmt.Errorf("Agent CLI state path is not a regular file: %s", stateFile)
				}
				args = append(args, "--mount", "type=bind,src="+stateFile+",dst=/root/"+name)
			} else if !os.IsNotExist(fileErr) {
				return nil, fileErr
			}
		}
	}
	return args, nil
}
