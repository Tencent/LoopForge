package runtime

import (
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
	Running, Paused, OOMKilled bool
	ExitCode                   int
}

func inspectDockerWorkspaceState(ctx context.Context, name string) (dockerWorkspaceState, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerInspectLimit)
	defer cancel()
	const format = `{"ID":{{json .Id}},"StartedAt":{{json .State.StartedAt}},"Status":{{json .State.Status}},"Running":{{.State.Running}},"Paused":{{.State.Paused}},"ExitCode":{{.State.ExitCode}},"OOMKilled":{{.State.OOMKilled}}}`
	output, err := exec.CommandContext(ctx, "docker", "inspect", "--type", "container", "--format", format, name).CombinedOutput()
	var state dockerWorkspaceState
	if ctx.Err() != nil {
		return state, fmt.Errorf("inspect Docker workspace %s: %w", name, ctx.Err())
	}
	if dockerObjectGone(string(output), err) {
		state.Status = "missing"
		return state, nil
	}
	if err != nil {
		return state, dockerStageError("inspect workspace state", name, output, err)
	}
	if json.Unmarshal(output, &state) != nil {
		return dockerWorkspaceState{}, fmt.Errorf("invalid Docker workspace state for %s", name)
	}
	switch state.Status {
	case "running", "exited", "dead", "created", "paused", "restarting", "removing":
		return state, nil
	default:
		return dockerWorkspaceState{}, fmt.Errorf("unknown Docker workspace state for %s", name)
	}
}

func dockerWorkspaceGeneration(ctx context.Context, name string) (string, error) {
	state, err := inspectDockerWorkspaceState(ctx, name)
	if err != nil {
		return "", err
	}
	switch state.Status {
	case "missing", "exited", "dead", "created", "paused", "restarting", "removing":
		return "", fmt.Errorf("%w: %s (state=%s, exit=%d, OOMKilled=%t)", errDockerWorkspaceUnavailable, name, state.Status, state.ExitCode, state.OOMKilled)
	}
	if generation := state.generation(); generation != "" {
		return generation, nil
	}
	return "", fmt.Errorf("cannot confirm Docker workspace %s generation", name)
}

func (state dockerWorkspaceState) generation() string {
	started, err := time.Parse(time.RFC3339Nano, state.StartedAt)
	if !state.Running || state.Paused || state.Status != "running" || !validDockerContainerID(state.ID) || err != nil || started.IsZero() {
		return ""
	}
	return state.ID + "|" + state.StartedAt
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
	return fmt.Errorf("Docker %s (%s): %s: %w", stage, object, reason, err)
}
