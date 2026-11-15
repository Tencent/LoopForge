package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

// PrepareHostShellCommand avoids Git/MSYS silently truncating long Windows -c
// arguments. The short bootstrap sources an exact private script file, keeping
// the original shell flags, positional arguments, stdin, cwd and environment.
// It must only be used for host processes, never paths inside a container.
func PrepareHostShellCommand(command CommandSpec) (CommandSpec, func(), error) {
	cleanup := func() {}
	if goruntime.GOOS != "windows" || len(command.Argv) < 3 {
		return command, cleanup, nil
	}
	shell := strings.TrimSuffix(strings.ToLower(filepath.Base(command.Argv[0])), ".exe")
	if shell != "sh" && shell != "bash" {
		return command, cleanup, nil
	}
	command.Argv = append([]string{}, command.Argv...)
	if !filepath.IsAbs(command.Argv[0]) {
		binary, err := findWindowsHostShell(shell)
		if err != nil {
			return command, cleanup, err
		}
		command.Argv[0] = binary
	}
	flags := command.Argv[1]
	if flags != "-c" && flags != "-lc" && flags != "-cl" || len(command.Argv[2]) < 6000 {
		return command, cleanup, nil
	}
	directory, err := os.MkdirTemp("", "agentflow-shell-")
	if err != nil {
		return command, cleanup, fmt.Errorf("prepare native shell script: %w", err)
	}
	file := filepath.Join(directory, "script.sh")
	cleanup = func() {
		_ = os.Remove(file)
		_ = os.Remove(directory)
	}
	if err := os.WriteFile(file, []byte(command.Argv[2]), 0600); err != nil {
		cleanup()
		return command, func() {}, fmt.Errorf("write native shell script: %w", err)
	}
	command.Argv[2] = ". " + shellSingleQuote(filepath.ToSlash(file))
	return command, cleanup, nil
}

// findWindowsHostShell prefers Git for Windows and never launches a WSL alias.
func findWindowsHostShell(shell string) (string, error) {
	var roots []string
	if git, err := exec.LookPath("git.exe"); err == nil {
		roots = append(roots, filepath.Dir(filepath.Dir(git)))
	}
	for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		if base != "" {
			roots = append(roots, filepath.Join(base, "Git"))
		}
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		roots = append(roots, filepath.Join(local, "Programs", "Git"))
	}
	var candidates []string
	for _, root := range roots {
		candidates = append(candidates, filepath.Join(root, "bin", shell+".exe"), filepath.Join(root, "usr", "bin", shell+".exe"))
	}
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(directory) {
			candidates = append(candidates, filepath.Join(directory, shell+".exe"))
		}
	}
	for _, candidate := range candidates {
		path := strings.ToLower(filepath.ToSlash(candidate))
		if strings.Contains(path, "/windowsapps/") || strings.Contains(path, "/system32/") || strings.Contains(path, "/sysnative/") {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("Windows Runner requires Git for Windows: install Git Bash and restart the runner")
}
