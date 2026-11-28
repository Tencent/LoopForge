package runtime

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const dockerInspectLimit = 3 * time.Second

var errDockerWorkspaceUnavailable = errors.New("Docker workspace unavailable")

type dockerWorkspaceState struct {
	ID, StartedAt, Status      string
	Running, Paused, OOMKilled *bool
	ExitCode                   *int
	Image                      string
	Labels                     map[string]string
}

func inspectDockerWorkspaceState(ctx context.Context, name string) (dockerWorkspaceState, error) {
	return inspectDockerContainerState(ctx, "inspect workspace state", name)
}

func inspectDockerContainerState(ctx context.Context, stage, name string) (dockerWorkspaceState, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerInspectLimit)
	defer cancel()
	const format = `{"ID":{{json .Id}},"StartedAt":{{json .State.StartedAt}},"Status":{{json .State.Status}},"Running":{{.State.Running}},"Paused":{{.State.Paused}},"ExitCode":{{.State.ExitCode}},"OOMKilled":{{.State.OOMKilled}},"Image":{{json .Config.Image}},"Labels":{{json .Config.Labels}}}`
	output, missing, err := dockerContainerOutput(ctx, stage, name, "inspect", "--type", "container", "--format", format, name)
	if err != nil {
		return dockerWorkspaceState{}, err
	}
	if missing {
		return dockerWorkspaceState{Status: "missing"}, nil
	}
	var state dockerWorkspaceState
	if json.Unmarshal(output, &state) != nil || !state.valid(name) {
		return dockerWorkspaceState{}, fmt.Errorf("Docker %s (%s): invalid container state", stage, name)
	}
	return state, nil
}

func dockerContainerOutput(ctx context.Context, stage, name string, args ...string) (output []byte, missing bool, err error) {
	output, err = exec.CommandContext(ctx, "docker", args...).Output()
	if ctx.Err() != nil {
		return nil, false, fmt.Errorf("Docker %s (%s): %w", stage, name, ctx.Err())
	}
	if err == nil {
		return output, false, nil
	}
	stderr := dockerCommandStderr(err)
	if len(bytes.TrimSpace(output)) == 0 && dockerContainerNotFound(name, stderr, err) {
		return nil, true, nil
	}
	return nil, false, dockerStageError(stage, name, stderr, err)
}

func dockerCommandStderr(err error) []byte {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.Stderr
	}
	return nil
}

func dockerContainerNotFound(name string, stderr []byte, err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return false
	}
	switch strings.TrimSpace(string(stderr)) {
	case "Error response from daemon: No such container: " + name, "Error: No such container: " + name, "Error: No such object: " + name:
		return true
	}
	return false
}

func (state dockerWorkspaceState) valid(name string) bool {
	started, err := time.Parse(time.RFC3339Nano, state.StartedAt)
	if !validDockerContainerID(state.ID) || (validDockerContainerID(name) && state.ID != name) ||
		err != nil || state.Running == nil || state.Paused == nil || state.ExitCode == nil || state.OOMKilled == nil {
		return false
	}
	switch state.Status {
	case "running", "restarting":
		return *state.Running && !*state.Paused && !started.IsZero()
	case "paused":
		return *state.Running && *state.Paused && !started.IsZero()
	case "created":
		return !*state.Running && !*state.Paused && started.IsZero() && *state.ExitCode == 0 && !*state.OOMKilled
	case "exited", "dead", "removing":
		return !*state.Running && !*state.Paused
	}
	return false
}

func dockerWorkspaceGeneration(ctx context.Context, name string) (string, error) {
	state, err := inspectDockerWorkspaceState(ctx, name)
	if err != nil {
		return "", err
	}
	if generation := state.generation(); generation != "" {
		return generation, nil
	}
	return "", fmt.Errorf("%w: %s (%s)", errDockerWorkspaceUnavailable, name, state.diagnostics("workspace keepalive (sh / sleep infinity)"))
}

func (state dockerWorkspaceState) generation() string {
	if state.Status != "running" {
		return ""
	}
	return state.ID + "|" + state.StartedAt
}

func (state dockerWorkspaceState) diagnostics(executable string) string {
	if state.ExitCode == nil {
		return "state=" + state.Status
	}
	return fmt.Sprintf("state=%s, exit=%d, OOMKilled=%t%s", state.Status, *state.ExitCode, *state.OOMKilled, dockerExitHint(*state.ExitCode, executable))
}

func dockerExitHint(code int, executable string) string {
	switch code {
	case 126:
		return "; " + executable + " could not be executed; check permissions"
	case 127:
		return "; " + executable + " or a required command was not found"
	}
	return ""
}

func validDockerContainerID(id string) bool {
	_, err := hex.DecodeString(id)
	return len(id) == 64 && err == nil
}

func dockerStageError(stage, object string, output []byte, err error) error {
	text := strings.ToLower(string(output))
	reason := "check Docker diagnostics on the execution host"
	switch {
	case strings.Contains(text, "executable file not found"), strings.Contains(text, "exec format error"):
		reason = "required executable is missing or incompatible"
	case strings.Contains(text, "unable to find user"), strings.Contains(text, "no matching entries in passwd"):
		reason = "configured image user is unavailable"
	case stage == "workspace shell probe" && (strings.Contains(text, "can't cd") || strings.Contains(text, "cannot cd")):
		reason = "cannot enter /workspace; check the workspace mount and runtime user permissions"
	case strings.Contains(text, "permission denied"):
		reason = "permission denied; check mounts and runtime user"
	case strings.Contains(text, "invalid mount"), strings.Contains(text, "mount source path does not exist"):
		reason = "invalid workspace mount"
	case strings.Contains(text, "network") && strings.Contains(text, "not found"):
		reason = "configured Docker network is unavailable"
	case strings.Contains(text, "cannot connect to the docker daemon"):
		reason = "Docker daemon unreachable"
	case strings.Contains(text, "manifest unknown"), strings.Contains(text, "no such image"):
		reason = "Docker image or tag not found"
	case strings.Contains(text, "pull access denied"), strings.Contains(text, "authentication required"):
		reason = "Docker registry access denied"
	}
	var exit *exec.ExitError
	if stage == "workspace shell probe" && errors.As(err, &exit) {
		reason += dockerExitHint(exit.ExitCode(), "workspace login shell")
	}
	return fmt.Errorf("Docker %s (%s): %s: %w", stage, object, reason, err)
}
