package runtime

import (
	"fmt"
	"os"
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
	command.Argv = append([]string{}, command.Argv...)
	command.Argv[2] = ". " + shellSingleQuote(filepath.ToSlash(file))
	return command, cleanup, nil
}
