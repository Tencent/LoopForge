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
		"SSH_AUTH_SOCK=",
		"GIT_SSH=",
		"GIT_SSH_COMMAND=",
		"GIT_SSH_VARIANT=",
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

func (s *processSandbox) ID() string        { return s.id }
func (s *processSandbox) Workspace() string { return s.workspace }
func (s *processSandbox) Driver() string    { return s.driver }
func (s *processSandbox) Close(context.Context) error {
	stopHostBrowsers(s.workspace)
	return nil
}

func createSeatbelt(id, workspace string, profile domain.SandboxProfile, requirements SandboxRequirements) (Sandbox, error) {
	return createProcessSandbox("seatbelt", "darwin", "macOS", "sandbox-exec", id, workspace, profile, requirements)
}

func createBwrap(id, workspace string, profile domain.SandboxProfile, requirements SandboxRequirements) (Sandbox, error) {
	return createProcessSandbox("bwrap", "linux", "Linux", "bwrap", id, workspace, profile, requirements)
}

// createProcessSandbox builds a processSandbox for driver once the host runs
// requiredOS (named osName in errors) and binaryName is on PATH.
func createProcessSandbox(driver, requiredOS, osName, binaryName, id, workspace string, profile domain.SandboxProfile, requirements SandboxRequirements) (Sandbox, error) {
	if goruntime.GOOS != requiredOS {
		return nil, fmt.Errorf("%s sandbox requires %s", driver, osName)
	}
	binary, err := exec.LookPath(binaryName)
	if err != nil {
		return nil, fmt.Errorf("%s sandbox unavailable: %w", driver, err)
	}
	return &processSandbox{id: id, workspace: workspace, driver: driver, profile: profile, requirements: requirements, binary: binary}, nil
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
		profile, err := seatbeltProfileCommand(s.workspace, s.requirements)
		if err != nil {
			return nil, err
		}
		args := append([]string{"-p", profile}, command.Argv...)
		cmd = exec.CommandContext(ctx, s.binary, args...)
	case "bwrap":
		args, err := bwrapArgsCommand(s.workspace, s.requirements)
		if err != nil {
			return nil, err
		}
		args = bwrapPrivateBinds(args, command.privateBinds)
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

func seatbeltProfileCommand(workspace string, requirements SandboxRequirements) (string, error) {
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
	return b.String(), nil
}

// bunFSMountPoint is the virtual root Bun-compiled CLIs (Claude Code,
// CodeBuddy) resolve their embedded files under. A variable for tests.
var bunFSMountPoint = "/$bunfs"

// ensureBunFSMountPoint makes sure bwrap can mount a private tmpfs at
// bunFSMountPoint. bwrap binds the host root read-only, so it cannot create a
// missing mount point and the whole sandbox would fail ("Can't create file
// /$bunfs: Read-only file system"). The host directory is created once when
// the runner may (root, as the builtin runner); otherwise the mount is
// skipped and Bun falls back to its in-process virtual filesystem.
func ensureBunFSMountPoint() bool {
	if info, err := os.Stat(bunFSMountPoint); err == nil {
		return info.IsDir()
	}
	if err := os.Mkdir(bunFSMountPoint, 0o755); err != nil && !os.IsExist(err) {
		return false
	}
	return true
}

// bwrapPrivateBinds adds per-process binds before the "--" that ends the
// bwrap options. They come after --tmpfs /tmp, so a directory under the host
// /tmp is visible to this process only.
func bwrapPrivateBinds(args, binds []string) []string {
	if len(binds) == 0 || len(args) == 0 || args[len(args)-1] != "--" {
		return args
	}
	out := append([]string{}, args[:len(args)-1]...)
	for _, dir := range binds {
		out = append(out, "--bind", dir, dir)
	}
	return append(out, "--")
}

func bwrapArgsCommand(workspace string, requirements SandboxRequirements) ([]string, error) {
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
	if requirements.BunFS && ensureBunFSMountPoint() {
		args = append(args, "--tmpfs", bunFSMountPoint)
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
	args = append(args, "--")
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
