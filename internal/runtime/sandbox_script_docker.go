package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"agentflow/internal/scriptview"
)

const scriptViewLabel = "agentflow.script-view-owner"
const scriptViewBinary = "/.agentflow-script-view"

func dockerScriptContainerName(name string) string { return name + "-scripts" }

// Scripts use a private copy of the existing mount namespace, rather than a
// fresh image: apt installs, PHP extensions and running services remain shared.
// This companion stays alive for the run so services started by scripts survive
// an individual exec. Its bootstrap capabilities are dropped before every script.
func (s *dockerSandbox) ensureScriptContainer(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var base struct {
		ID    string `json:"Id"`
		Image string
		State struct {
			Running   bool
			StartedAt string
		}
	}
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .}}", s.name).CombinedOutput()
	if err != nil {
		if dockerObjectGone(string(raw), err) {
			s.started = false
			return fmt.Errorf("Error response from daemon: no such container: %s", s.name)
		}
		return fmt.Errorf("inspect script environment: %w", err)
	}
	if err = json.Unmarshal(raw, &base); err != nil {
		return err
	}
	if !base.State.Running {
		s.started = false
		return fmt.Errorf("Error response from daemon: workspace container %s is not running", s.name)
	}
	if base.ID == "" || base.Image == "" {
		return fmt.Errorf("script execution environment metadata is incomplete")
	}
	arch, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Architecture}}", base.Image).Output()
	if err != nil {
		return fmt.Errorf("inspect script environment architecture: %w", err)
	}
	binary, err := resolveGateMCPBinary("linux", strings.TrimSpace(string(arch)))
	if err != nil {
		return fmt.Errorf("script view helper unavailable: %w", err)
	}
	contents, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(contents)
	owner := base.ID + "|" + base.State.StartedAt + "|" + scriptview.Version + "|" + hex.EncodeToString(sum[:])
	name := dockerScriptContainerName(s.name)
	existing, probeErr := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{.State.Running}}|{{index .Config.Labels "`+scriptViewLabel+`"}}`, name).CombinedOutput()
	if probeErr == nil && strings.TrimSpace(string(existing)) == "true|"+owner {
		return nil
	}
	if probeErr != nil && !dockerObjectGone(string(existing), probeErr) {
		return fmt.Errorf("inspect script view: %w", probeErr)
	}
	// A stopped helper lost any services in its cgroup. Recreate it so its ID and
	// start time invalidate old preparation receipts instead of claiming readiness.
	if probeErr == nil {
		if out, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
			return fmt.Errorf("remove stale script view: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	args := dockerScriptCreateArgs(name, s.name, s.workspace, base.Image, owner)
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("create script view: %w: %s", err, strings.TrimSpace(string(out)))
	}
	ready := false
	defer func() {
		if !ready {
			_ = exec.Command("docker", "rm", "-f", name).Run()
		}
	}()
	if out, err := exec.CommandContext(ctx, "docker", "cp", binary, name+":"+scriptViewBinary).CombinedOutput(); err != nil {
		return fmt.Errorf("install script view helper: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(ctx, "docker", "start", name).CombinedOutput(); err != nil {
		return fmt.Errorf("start script view: %w: %s", err, strings.TrimSpace(string(out)))
	}
	probe, err := exec.CommandContext(ctx, "docker", "exec", name, scriptViewBinary, scriptview.ProbeArgument).CombinedOutput()
	if err != nil || strings.TrimSpace(string(probe)) != scriptview.Version {
		return fmt.Errorf("script view helper version is incompatible; deploy the matching platform helper")
	}
	ready = true
	return nil
}

func dockerScriptCreateArgs(name, base, workspace, image, owner string) []string {
	args := []string{"create", "--name", name, "--user", "0:0", "--pid=container:" + base, "--network=container:" + base,
		"--cap-drop=ALL", "--security-opt", "no-new-privileges", "--security-opt", "seccomp=unconfined", "--security-opt", "apparmor=unconfined",
		"--label", scriptViewLabel + "=" + owner,
		// This bind also lets the existing workspace sweeper find orphan helpers.
		"-v", workspace + ":/workspace"}
	for _, capability := range []string{"SYS_ADMIN", "SYS_CHROOT", "SYS_PTRACE", "SETPCAP", "SETUID", "SETGID", "DAC_OVERRIDE"} {
		args = append(args, "--cap-add="+capability)
	}
	return append(args, "--entrypoint", "sh", image, "-c", "trap : TERM INT; sleep infinity & wait")
}

func (s *dockerSandbox) execScript(ctx context.Context, command CommandSpec, sink EventSink) (string, error) {
	if err := s.ensureScriptContainer(ctx); err != nil {
		return "", err
	}
	masks, err := s.scriptMasks(ctx)
	if err != nil {
		return "", err
	}
	view := scriptview.Config{Masks: masks, Argv: dockerWorkspaceExecArgv(command.Argv)}
	config, err := json.Marshal(view)
	if err != nil {
		return "", err
	}
	args := []string{"exec"}
	for _, environment := range appendEnvironment(nil, s.profile.Environment, command.environment) {
		args = append(args, "-e", environment)
	}
	args = append(args, dockerScriptContainerName(s.name), scriptViewBinary, "--workspace-script-view", string(config))
	return runCommand(exec.CommandContext(ctx, "docker", args...), sink)
}

func (s *dockerSandbox) scriptMasks(ctx context.Context) ([]string, error) {
	// Keep actual mounts, including legacy masks attached to replaced directories,
	// and combine them below with refreshed masks discovered in current files.
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Mounts}}", s.name).Output()
	if err != nil {
		return nil, fmt.Errorf("inspect script view masks: %w", err)
	}
	var mounts []struct {
		Source, Destination string
		RW                  bool
	}
	if err = json.Unmarshal(raw, &mounts); err != nil {
		return nil, err
	}
	dir, file, err := isolationMaskSourcesAt(dockerIsolationMaskRoot(s.workspace))
	if err != nil {
		return nil, err
	}
	var masks []string
	for _, m := range mounts {
		if m.RW || (m.Source != dir && m.Source != file) {
			continue
		}
		if scriptview.ValidMask(m.Destination) {
			masks = append(masks, m.Destination)
		}
	}
	current, err := s.currentAgentMasks()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, target := range masks {
		seen[target] = true
	}
	for _, target := range current {
		if !seen[target] {
			masks = append(masks, target)
			seen[target] = true
		}
	}
	return masks, nil
}

// Preserve ordinary process isolation but omit instruction-file masks for
// platform-defined scripts. Agent invocations keep their original masks.
func trustedScriptCommand(command CommandSpec) bool { return command.Provider == "script" }

// Agents receive refreshed masks: a dependency update can replace
// the directory that a canonical container's old bind mount was attached to.
// Rediscover masks for every invocation so replacement instruction files cannot
// become visible after a script legitimately removes/reinstalls a package.
func maskedAgentCommand(command CommandSpec) bool {
	switch command.Provider {
	case "codebuddy", "codex", "claude-code":
		return true
	}
	return false
}

func (s *dockerSandbox) currentAgentMasks() ([]string, error) {
	paths, err := discoverProjectAgentPaths(s.workspace)
	if err != nil {
		return nil, err
	}
	var hide []string
	for _, rel := range paths {
		_, err := validateAgentConfigTarget(s.workspace, rel)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		hide = append(hide, filepath.ToSlash(filepath.Join("/workspace", rel)))
	}
	return hide, nil
}

// Add fresh readonly masks before launching an Agent in the canonical container.
// Only this trusted setup runs in the helper; the Agent retains the canonical
// Docker seccomp/AppArmor policy as well as its capability restrictions.
func (s *dockerSandbox) refreshAgentMasks(ctx context.Context) error {
	if err := s.ensureScriptContainer(ctx); err != nil {
		return err
	}
	hide, err := s.currentAgentMasks()
	if err != nil {
		return err
	}
	config, err := json.Marshal(scriptview.Config{Refresh: true, Hide: hide})
	if err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "docker", "exec", dockerScriptContainerName(s.name), scriptViewBinary, scriptview.Argument, string(config)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("refresh agent instruction isolation: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
