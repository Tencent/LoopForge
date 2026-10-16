package runtime

import (
	"agentflow/internal/domain"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type dockerSandbox struct {
	id, name, workspace string
	profile             domain.SandboxProfile
	requirements        SandboxRequirements
	dockerfile          domain.DockerfileDefinition
	builtImage          string
	mu                  sync.Mutex
	started             bool
}

func (s *dockerSandbox) ID() string        { return s.id }
func (s *dockerSandbox) Workspace() string { return s.workspace }
func (s *dockerSandbox) Driver() string    { return "docker" }

// Close tears the sandbox down by name rather than by the in-memory started
// flag. A worker that restarted inherits a live container but a fresh struct
// with started=false and no builtImage, so trusting those fields leaks the
// container and its image for good: nothing else ever removes them, and Docker
// cannot prune a container that is still "running".
func (s *dockerSandbox) Close(ctx context.Context) error {
	s.mu.Lock()
	s.started = false
	image := s.builtImage
	s.builtImage = ""
	s.mu.Unlock()
	if image == "" {
		image = tempDockerImageName(s.id)
	}
	removeDockerSandbox(ctx, s.name, image)
	return nil
}

// removeDockerSandbox drops a sandbox container. Per-run throwaway images
// (agentflow-tmp-*) are removed with it; content-hashed cache images stay so
// the next SOP with the same Dockerfile does not rebuild from node:22.
func removeDockerSandbox(ctx context.Context, container, image string) {
	if !dockerAvailable() {
		return
	}
	// Retry holds the engine state mutex across this call, so an unresponsive
	// daemon must not be able to block it indefinitely. rm -f is a SIGKILL and
	// the sweep retries every 20s, so giving up early costs nothing.
	ctx, cancel := context.WithTimeout(ctx, dockerTeardownTimeout)
	defer cancel()
	if strings.TrimSpace(container) != "" {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", dockerScriptContainerName(container)).Run()
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", container).Run()
	}
	if ephemeralDockerImage(image) {
		_ = exec.CommandContext(ctx, "docker", "rmi", "-f", image).Run()
	}
}

const dockerTeardownTimeout = 60 * time.Second

// dockerAvailable keeps run teardown from spawning processes on control planes
// that only ever use the local driver. Cleanup runs per finished run and on a
// 20s sweep, so this is on a hot path.
var dockerAvailable = sync.OnceValue(func() bool {
	_, err := exec.LookPath("docker")
	return err == nil
})

var dockerReady struct {
	sync.Mutex
	checked time.Time
	ok      bool
}

// DockerReady reports whether the Docker daemon answers, cached for a minute
// because the UI asks on every page load.
func DockerReady() bool {
	dockerReady.Lock()
	defer dockerReady.Unlock()
	if time.Since(dockerReady.checked) < time.Minute {
		return dockerReady.ok
	}
	ok := false
	if dockerAvailable() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ok = exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").Run() == nil
		cancel()
	}
	dockerReady.checked, dockerReady.ok = time.Now(), ok
	return ok
}

func (s *dockerSandbox) Exec(ctx context.Context, command CommandSpec, sink EventSink) (string, error) {
	if err := s.ensureStarted(ctx); err != nil {
		return "", err
	}
	out, err := s.execOnce(ctx, command, sink)
	if err != nil && isDockerCwdUnsafe(out, err) {
		if recErr := s.Recreate(ctx); recErr != nil {
			return out, fmt.Errorf("current working directory is outside of container mount namespace: %w", err)
		}
		out, err = s.execOnce(ctx, command, sink)
		if err != nil && isDockerCwdUnsafe(out, err) {
			return out, fmt.Errorf("current working directory is outside of container mount namespace: %w", err)
		}
	}
	// docker CLI reports "No such exec instance" on stderr and exits 1.
	// The engine only sees `exit status 1` unless that daemon line is kept
	// on the error; Recreate here would docker rm a still-bound workspace.
	if err != nil {
		err = annotateDockerExecError(out, err)
		if isMissingDockerExec(err) {
			s.invalidate()
		}
	}
	return out, err
}

func (s *dockerSandbox) execOnce(ctx context.Context, command CommandSpec, sink EventSink) (string, error) {
	if trustedScriptCommand(command) {
		return s.execScript(ctx, command, sink)
	}
	if maskedAgentCommand(command) {
		if err := s.refreshAgentMasks(ctx); err != nil {
			return "", err
		}
	}
	args := []string{"exec"}
	for _, environment := range appendEnvironment(nil, s.profile.Environment, command.environment) {
		args = append(args, "-e", environment)
	}
	args = append(args, s.name)
	args = append(args, dockerWorkspaceExecArgv(command.Argv)...)
	return runCommand(exec.CommandContext(ctx, "docker", args...), sink)
}

// dockerWorkspaceExecArgv runs argv inside /workspace without docker exec -w.
// runc treats a failed getcwd after -w as exit 126 ("cwd outside mount namespace").
// The outer shell is a login shell so the image profile can set php/composer
// PATH; helper bin is prepended after that, and an inner `sh -lc` is demoted
// to `sh -c` so a second login does not wipe the workspace bin.
func dockerWorkspaceExecArgv(argv []string) []string {
	launch := dockerMCPCallLaunch
	if len(argv) == 0 {
		return []string{"sh", "-lc", launch + "pwd"}
	}
	return append([]string{"sh", "-lc", launch + `exec "$0" "$@"`}, dropShellLoginFlag(argv)...)
}

const dockerMCPCallLaunch = `cd /workspace || exit
if [ -x /workspace/.agentflow/runtime/bin/agentflow ]; then
  PATH=/workspace/.agentflow/runtime/bin:$PATH
  export PATH
fi
`

func dropShellLoginFlag(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	switch filepath.Base(argv[0]) {
	case "sh", "bash", "dash", "zsh":
	default:
		return argv
	}
	out := []string{argv[0]}
	for _, arg := range argv[1:] {
		switch arg {
		case "-lc", "-cl":
			out = append(out, "-c")
		case "-l", "--login":
			continue
		default:
			out = append(out, arg)
		}
	}
	return out
}

func (s *dockerSandbox) Recreate(ctx context.Context) error {
	s.mu.Lock()
	s.started = false
	name := s.name
	s.mu.Unlock()
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", dockerScriptContainerName(name)).Run()
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	return s.ensureStarted(ctx)
}

// invalidate drops the cached started flag without docker rm. The next
// ensureStarted revalidates: a live container on the same workspace bind is
// restarted and reused, and only a genuinely broken one gets recreated.
func (s *dockerSandbox) invalidate() {
	s.mu.Lock()
	s.started = false
	s.mu.Unlock()
}

// BindWorkspace starts the container against the current workspace inode.
// Unlike Recreate, a healthy container started after checkout is reused so
// later nodes and retries do not docker rm a live bind.
func (s *dockerSandbox) BindWorkspace(ctx context.Context) error {
	return s.ensureStarted(ctx)
}

func IsCwdMountFailure(err error) bool {
	return isDockerCwdUnsafe("", err)
}

// IsSandboxInterrupt reports deploy/restart teardown: context cancel, a
// SIGTERM'd helper, or Docker destroying the exec/container underneath us.
// These must park the node for resume instead of consuming MaxAttempts.
func IsSandboxInterrupt(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "signal: killed") ||
		strings.Contains(msg, "signal: terminated") ||
		isMissingDockerExec(err)
}

func isMissingDockerExec(err error) bool {
	if err == nil {
		return false
	}
	return looksLikeDockerExecGone(err.Error())
}

func looksLikeDockerExecGone(text string) bool {
	msg := strings.ToLower(text)
	return strings.Contains(msg, "no such exec instance") ||
		strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "container not found") ||
		(strings.Contains(msg, "error response from daemon") && strings.Contains(msg, "is not running"))
}

func annotateDockerExecError(out string, err error) error {
	if err == nil || looksLikeDockerExecGone(err.Error()) {
		return err
	}
	excerpt := dockerInterruptExcerpt(out)
	if excerpt == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, excerpt)
}

// dockerInterruptExcerpt returns the docker CLI's own failure line from the
// tail of out. Everything else in out is agent-controlled text, and an
// interrupt parks the node without consuming an attempt, so a build log that
// merely mentions "no such container" must not be able to requeue forever.
func dockerInterruptExcerpt(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > dockerInterruptTailLines {
		lines = lines[len(lines)-dockerInterruptTailLines:]
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "error response from daemon:") && !strings.HasPrefix(lower, "error: no such") {
			continue
		}
		if !looksLikeDockerExecGone(lower) {
			continue
		}
		if len(line) > 300 {
			return line[:300]
		}
		return line
	}
	return ""
}

const dockerInterruptTailLines = 5

func dockerContainerReusable(source string, sourceErr error, workspace string) bool {
	return sourceErr == nil && dockerBindMatchesWorkspace(source, workspace)
}

func isDockerCwdUnsafe(out string, err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(out + "\n" + err.Error())
	return strings.Contains(msg, "outside of container mount") || strings.Contains(msg, "container breakout") || strings.Contains(msg, "oci runtime exec failed") || strings.Contains(msg, "exit status 126")
}

func dockerWorkspaceBindSource(ctx context.Context, name string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", `{{range .Mounts}}{{if eq .Destination "/workspace"}}{{.Source}}{{end}}{{end}}`, name).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker inspect mounts: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func dockerBindMatchesWorkspace(source, workspace string) bool {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(workspace) == "" {
		return false
	}
	want, err := filepath.Abs(workspace)
	if err != nil {
		return false
	}
	got, err := filepath.Abs(source)
	if err != nil {
		return false
	}
	return filepath.Clean(got) == filepath.Clean(want)
}

// dockerSandboxesUnder maps sandbox container name -> workspace directory name
// for every container whose /workspace bind lives directly under root.
//
// Ownership is decided by the bind mount, not by the name, because a runner
// daemon sharing the host creates agentflow-rt-* containers against its own
// workspace root and cleans them up itself.
func dockerSandboxesUnder(ctx context.Context, root string) (map[string]string, error) {
	if !dockerAvailable() {
		return nil, nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	ids, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "name=^/"+dockerSandboxPrefix).Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	out, err := collectDockerSandboxMounts(ctx, strings.Fields(string(ids)))
	if err != nil {
		return nil, err
	}
	return parseDockerSandboxMounts(out, absRoot), nil
}

// dockerSandboxMountFormat is one inspect line: container name, tab, the host
// path bound at /workspace.
const dockerSandboxMountFormat = `{{.Name}}` + "\t" + `{{range .Mounts}}{{if eq .Destination "/workspace"}}{{.Source}}{{end}}{{end}}`

// collectDockerSandboxMounts inspects each container on its own. A container
// that disappears between ps and inspect is skipped; one failure used to abort
// the whole reclaim pass.
func collectDockerSandboxMounts(ctx context.Context, ids []string) (string, error) {
	var b strings.Builder
	for _, id := range ids {
		out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", dockerSandboxMountFormat, id).CombinedOutput()
		if err != nil {
			if dockerObjectGone(string(out), err) {
				continue
			}
			return "", fmt.Errorf("docker inspect %s: %w: %s", id, err, strings.TrimSpace(string(out)))
		}
		line := strings.TrimSpace(string(out))
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func dockerObjectGone(output string, err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(output + "\n" + err.Error())
	return strings.Contains(msg, "no such object") ||
		strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "container not found")
}

// parseDockerSandboxMounts reads `docker inspect` lines of "name\tbindSource"
// and keeps only the containers bound one level under root.
func parseDockerSandboxMounts(out, absRoot string) map[string]string {
	found := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, source, ok := strings.Cut(strings.TrimSpace(line), "\t")
		name = strings.TrimPrefix(strings.TrimSpace(name), "/")
		source = strings.TrimSpace(source)
		if !ok || name == "" || source == "" {
			continue
		}
		abs, err := filepath.Abs(source)
		if err != nil || filepath.Dir(abs) != absRoot {
			continue
		}
		found[name] = filepath.Base(abs)
	}
	return found
}

func createDocker(ctx context.Context, id, workspace string, p domain.SandboxProfile, requirements SandboxRequirements, dockerfile domain.DockerfileDefinition) (Sandbox, error) {
	if strings.TrimSpace(dockerfile.Content) == "" {
		return nil, fmt.Errorf("docker sandbox requires a dockerfile")
	}
	// Delay docker create until the first Exec so the bind-mount is taken
	// after prepareRepository has cloned into this directory. Creating the
	// container first, then letting git replace the workspace inode, is the
	// runc 126 "cwd outside mount namespace" failure.
	return &dockerSandbox{id: id, name: dockerContainerName(id), workspace: workspace, profile: p, requirements: requirements, dockerfile: dockerfile}, nil
}

const dockerSandboxPrefix = "agentflow-"
const dockerCachedImagePrefix = "agentflow-img-"
const dockerTempImagePrefix = "agentflow-tmp-"

func dockerContainerName(id string) string { return dockerSandboxPrefix + id }

func tempDockerImageName(id string) string { return dockerTempImagePrefix + safeID(id) }

func ephemeralDockerImage(image string) bool {
	return strings.HasPrefix(strings.TrimSpace(image), dockerTempImagePrefix)
}

func dropEphemeralDockerImage(image string) {
	if !ephemeralDockerImage(image) {
		return
	}
	_ = exec.Command("docker", "rmi", "-f", image).Run()
}

func cachedDockerImageName(dockerfile domain.DockerfileDefinition) string {
	sum := sha256.Sum256(dockerImageCacheKey(dockerfile))
	return dockerCachedImagePrefix + hex.EncodeToString(sum[:])[:16]
}

func dockerImageCacheKey(dockerfile domain.DockerfileDefinition) []byte {
	var b strings.Builder
	b.WriteString(dockerfile.Content)
	b.WriteByte(0)
	names := make([]string, 0, len(dockerfile.Files))
	for name := range dockerfile.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(0)
		b.WriteString(dockerfile.Files[name])
		b.WriteByte(0)
	}
	return []byte(b.String())
}

func dockerImageExists(ctx context.Context, image string) bool {
	if strings.TrimSpace(image) == "" || !dockerAvailable() {
		return false
	}
	return exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil
}

// DockerImageCached reports whether this Dockerfile already has a local cache
// tag. Used by the engine to tell first-run users that a long image build is
// about to start, instead of looking like the node never began.
func DockerImageCached(ctx context.Context, dockerfile domain.DockerfileDefinition) bool {
	if strings.TrimSpace(dockerfile.Content) == "" {
		return false
	}
	return dockerImageExists(ctx, cachedDockerImageName(dockerfile))
}

func reclaimCachedDockerImages(ctx context.Context, keep map[string]struct{}) (int, error) {
	listed, err := listCachedDockerImages(ctx)
	if err != nil {
		return 0, err
	}
	inUse, err := cachedDockerImagesInUse(ctx)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, image := range staleCachedDockerImages(listed, keep, inUse) {
		if out, err := exec.CommandContext(ctx, "docker", "rmi", "-f", image).CombinedOutput(); err != nil {
			return removed, fmt.Errorf("docker rmi %s: %w: %s", image, err, strings.TrimSpace(string(out)))
		}
		removed++
	}
	return removed, nil
}

func listCachedDockerImages(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "docker", "images", "--format", "{{.Repository}}", "--filter", "reference="+dockerCachedImagePrefix+"*").Output()
	if err != nil {
		return nil, fmt.Errorf("docker images: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return parseCachedDockerImageNames(string(out)), nil
}

func cachedDockerImagesInUse(ctx context.Context) (map[string]struct{}, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a", "--format", "{{.Image}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w: %s", err, strings.TrimSpace(string(out)))
	}
	used := map[string]struct{}{}
	for _, image := range parseCachedDockerImageNames(string(out)) {
		used[image] = struct{}{}
	}
	return used, nil
}

func parseCachedDockerImageNames(out string) []string {
	var names []string
	seen := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		name := normalizeCachedDockerImageName(line)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func normalizeCachedDockerImageName(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		return ""
	}
	if tag := strings.LastIndex(name, ":"); tag > 0 && !strings.Contains(name[tag:], "/") {
		name = name[:tag]
	}
	if !strings.HasPrefix(name, dockerCachedImagePrefix) {
		return ""
	}
	return name
}

func staleCachedDockerImages(listed []string, keep, inUse map[string]struct{}) []string {
	var stale []string
	for _, image := range listed {
		if !strings.HasPrefix(image, dockerCachedImagePrefix) {
			continue
		}
		if _, ok := keep[image]; ok {
			continue
		}
		if _, ok := inUse[image]; ok {
			continue
		}
		stale = append(stale, image)
	}
	return stale
}

func (s *dockerSandbox) ensureStarted(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	name, workspace, p := s.name, s.workspace, s.profile
	if err := exec.CommandContext(ctx, "docker", "inspect", name).Run(); err == nil {
		source, sourceErr := dockerWorkspaceBindSource(ctx, name)
		if dockerContainerReusable(source, sourceErr, workspace) {
			if startErr := exec.CommandContext(ctx, "docker", "start", name).Run(); startErr != nil {
				_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
			} else if readyErr := waitDockerWorkspaceReady(ctx, name); readyErr != nil {
				_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
			} else {
				s.started = true
				return nil
			}
		} else if sourceErr != nil {
			_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
		} else {
			return fmt.Errorf("docker sandbox %s is bound to %s, not %s", name, source, workspace)
		}
	}
	built, err := ensureDockerImage(ctx, workspace, s.dockerfile)
	if err != nil {
		return err
	}
	image := built
	s.builtImage = built
	args := []string{"create", "--name", name, "--init", "--cap-drop=ALL", "--security-opt", "no-new-privileges", "-v", workspace + ":/workspace"}
	if network := strings.TrimSpace(os.Getenv("AGENTFLOW_DOCKER_NETWORK")); network != "" {
		args = append(args, "--network", network)
	}
	maskArgs, err := dockerAgentConfigMasks(workspace)
	if err != nil {
		return err
	}
	args = append(args, maskArgs...)
	stateArgs, err := dockerAgentStateMountRequirements(s.requirements)
	if err != nil {
		return err
	}
	args = append(args, stateArgs...)
	if p.ReadOnlyRootFS {
		args = append(args, "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=256m")
	}
	args = append(args, image, "sh", "-lc", "trap : TERM INT; sleep infinity & wait")
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		dropEphemeralDockerImage(s.builtImage)
		s.builtImage = ""
		return fmt.Errorf("docker create: %w: %s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "docker", "start", name).CombinedOutput(); err != nil {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
		dropEphemeralDockerImage(s.builtImage)
		s.builtImage = ""
		return fmt.Errorf("docker start: %w: %s", err, out)
	}
	if err := waitDockerWorkspaceReady(ctx, name); err != nil {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		s.started = false
		return err
	}
	s.started = true
	return nil
}

func waitDockerWorkspaceReady(ctx context.Context, name string) error {
	deadline := time.Now().Add(20 * time.Second)
	var lastOut []byte
	var lastErr error
	for {
		cmd := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-lc", "cd /workspace && pwd")
		lastOut, lastErr = cmd.CombinedOutput()
		if lastErr == nil && strings.Contains(string(lastOut), "/workspace") {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			if lastErr == nil {
				lastErr = fmt.Errorf("unexpected pwd output %q", strings.TrimSpace(string(lastOut)))
			}
			return fmt.Errorf("docker workspace not ready: %w: %s", lastErr, strings.TrimSpace(string(lastOut)))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func dockerBuildArgs(image, contextDir string) []string {
	return []string{"build", "-t", image, contextDir}
}

func prepareDockerfileBuildContext(workspace string, dockerfile domain.DockerfileDefinition) (string, error) {
	if strings.TrimSpace(dockerfile.Content) == "" {
		return "", fmt.Errorf("dockerfile content is empty")
	}
	contextID, err := catalogDirName(dockerfile.ID)
	if err != nil {
		contextID = "default"
	}
	root, err := resetControlDirectory(workspace, "docker", contextID)
	if err != nil {
		return "", err
	}
	if err := writeManagedFile(filepath.Join(root, "Dockerfile"), []byte(dockerfile.Content), 0o600); err != nil {
		return "", err
	}
	for name, data := range dockerfile.Files {
		if err := domain.ValidateRelativeFilePath(name); err != nil {
			return "", fmt.Errorf("dockerfile file: %w", err)
		}
		if filepath.Base(name) == "Dockerfile" && filepath.Dir(filepath.FromSlash(name)) == "." {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(filepath.Clean(name)))
		rel, relErr := filepath.Rel(root, target)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("invalid dockerfile file path %q", name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := writeManagedFile(target, []byte(data), 0o644); err != nil {
			return "", err
		}
	}
	return root, nil
}

func ensureDockerImage(ctx context.Context, workspace string, dockerfile domain.DockerfileDefinition) (string, error) {
	image := cachedDockerImageName(dockerfile)
	if dockerImageExists(ctx, image) {
		return image, nil
	}
	contextDir, err := prepareDockerfileBuildContext(workspace, dockerfile)
	if err != nil {
		return "", err
	}
	args := dockerBuildArgs(image, contextDir)
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker build: %w: %s", err, out)
	}
	return image, nil
}

// IsolatedDockerfile matches the public "generic-backend" environment so both
// share one cached image.
const IsolatedDockerfile = "FROM node:22-bookworm\nRUN apt-get update && apt-get install -y --no-install-recommends git python3 ripgrep ca-certificates curl && rm -rf /var/lib/apt/lists/*\nRUN npm install -g @openai/codex @anthropic-ai/claude-code @tencent-ai/codebuddy-code\nWORKDIR /workspace\n"

// PrebuildIsolatedImage warms the shared sandbox image in the background so
// the first SOP on a fresh container install does not wait for the build.
func PrebuildIsolatedImage() {
	if strings.TrimSpace(os.Getenv("AGENTFLOW_LOCAL_SANDBOX")) != "docker" || !DockerReady() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		dir, err := os.MkdirTemp("", "agentflow-prebuild-")
		if err != nil {
			return
		}
		defer os.RemoveAll(dir)
		if _, err := ensureDockerImage(ctx, dir, domain.DockerfileDefinition{ID: "isolated", Content: IsolatedDockerfile}); err != nil {
			log.Printf("prebuild sandbox image: %v", err)
		}
	}()
}

// isolateLocalDriver runs "local" profiles in Docker when
// AGENTFLOW_LOCAL_SANDBOX=docker, so no SOP command executes inside a platform
// container that can reach the host Docker daemon.
func isolateLocalDriver(profile domain.SandboxProfile, dockerfile domain.DockerfileDefinition) (domain.SandboxProfile, domain.DockerfileDefinition) {
	driver := strings.TrimSpace(profile.Driver)
	if (driver != "" && driver != "local") || strings.TrimSpace(os.Getenv("AGENTFLOW_LOCAL_SANDBOX")) != "docker" {
		return profile, dockerfile
	}
	profile.Driver = "docker"
	if strings.TrimSpace(dockerfile.Content) == "" {
		dockerfile = domain.DockerfileDefinition{ID: "generic-backend", Name: "Isolated local sandbox", Content: IsolatedDockerfile}
	}
	return profile, dockerfile
}
