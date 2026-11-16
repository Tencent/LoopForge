package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestDockerConversationProcessPreservesInteractiveInput(t *testing.T) {
	requireConversationSubreaper(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pid := filepath.Join(t.TempDir(), "owned-process.pid")
	argv := dockerConversationProcessArgv(pid, []string{"sh", "-c", `read first; read second; printf '%s/%s' "$first" "$second"`})
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader("initialize\nthread/resume\n")
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "initialize/thread/resume" {
		t.Fatalf("interactive process lost input: %q %v", output, err)
	}
	if data, err := os.ReadFile(pid); err != nil || strings.TrimSpace(string(data)) == "" {
		t.Fatalf("process ownership PID not recorded: %q %v", data, err)
	}
}

func TestDockerSandboxLifecycleWithFakeCLI(t *testing.T) {
	workspace := t.TempDir()
	cvRInstallFakeDocker(t, workspace)
	dockerfile := domain.DockerfileDefinition{ID: "cvR", Content: "FROM scratch\n", Files: map[string]string{"asset.txt": "asset"}}
	sbAny, err := createDocker(context.Background(), "cvR-run", workspace,
		domain.SandboxProfile{Driver: "docker", ReadOnlyRootFS: true, Environment: map[string]string{"A": "1"}},
		SandboxRequirements{BunFS: true}, dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	sb := sbAny.(*dockerSandbox)
	if sb.ID() != "cvR-run" || sb.Workspace() != workspace || sb.Driver() != "docker" {
		t.Fatalf("sandbox=%+v", sb)
	}
	out, err := sb.Exec(context.Background(), CommandSpec{Argv: []string{"sh", "-lc", "echo ok"}, environment: map[string]string{"B": "2"}}, nil)
	if err != nil || !strings.Contains(out, "/workspace") {
		t.Fatalf("exec=%q err=%v", out, err)
	}
	if !sb.started || sb.builtImage == "" {
		t.Fatalf("sandbox did not start: %+v", sb)
	}
	if err := sb.BindWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}
	sb.invalidate()
	if sb.started {
		t.Fatal("invalidate retained started")
	}
	if err := sb.Recreate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDockerDiscoveryImagesAndBuildHelpers(t *testing.T) {
	workspace := t.TempDir()
	cvRInstallFakeDocker(t, workspace)
	ctx := context.Background()
	info, err := inspectDockerWorkspaceMounts(ctx, "box")
	if err != nil || info == nil || info.source != workspace || info.legacyMasks {
		t.Fatalf("mounts=%+v err=%v", info, err)
	}
	found, err := dockerSandboxesUnder(ctx, filepath.Dir(workspace))
	if err != nil || found["agentflow-cvr"] != filepath.Base(workspace) {
		t.Fatalf("found=%v err=%v", found, err)
	}
	mounts, err := collectDockerSandboxMounts(ctx, []string{"id1"})
	if err != nil || !strings.Contains(mounts, "agentflow-cvr") {
		t.Fatalf("mounts=%q err=%v", mounts, err)
	}
	if !dockerObjectGone("No such container", os.ErrNotExist) || dockerObjectGone("", nil) {
		t.Fatal("docker gone classification")
	}
	listed, err := listCachedDockerImages(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("listed=%v err=%v", listed, err)
	}
	inUse, err := cachedDockerImagesInUse(ctx)
	if err != nil || len(inUse) != 1 {
		t.Fatalf("inUse=%v err=%v", inUse, err)
	}
	removed, err := reclaimCachedDockerImages(ctx, map[string]struct{}{})
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if got := staleCachedDockerImages([]string{"agentflow-img-a", "other"}, map[string]struct{}{}, map[string]struct{}{}); len(got) != 1 {
		t.Fatalf("stale=%v", got)
	}
	if normalizeCachedDockerImageName("agentflow-img-a:latest") != "agentflow-img-a" || normalizeCachedDockerImageName("ubuntu") != "" {
		t.Fatal("image normalization")
	}
	dockerfile := domain.DockerfileDefinition{ID: "../bad", Content: "FROM scratch", Files: map[string]string{"Dockerfile": "ignored", "sub/a": "a"}}
	dir, err := prepareDockerfileBuildContext(workspace, dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "a")); err != nil {
		t.Fatal(err)
	}
	image, err := ensureDockerImage(ctx, workspace, dockerfile)
	if err != nil || !strings.HasPrefix(image, dockerCachedImagePrefix) {
		t.Fatalf("image=%q err=%v", image, err)
	}
	if _, err := createDocker(ctx, "x", workspace, domain.SandboxProfile{}, SandboxRequirements{}, domain.DockerfileDefinition{}); err == nil {
		t.Fatal("empty dockerfile accepted")
	}
}

func TestDockerArgumentAndErrorBranches(t *testing.T) {
	if got := dockerWorkspaceExecArgv(nil); !strings.Contains(strings.Join(got, " "), "pwd") {
		t.Fatalf("argv=%v", got)
	}
	got := dockerWorkspaceExecArgv([]string{"bash", "-lc", "echo x"})
	if len(got) < 3 || got[len(got)-2] != "-c" || !cvRContains(got, "-c") {
		t.Fatalf("login flag retained: %v", got)
	}
	if out := dropShellLoginFlag([]string{"tool", "-lc"}); out[1] != "-lc" {
		t.Fatalf("non-shell changed: %v", out)
	}
	for _, text := range []string{"No such exec instance", "No such container", "container not found", "Error response from daemon: box is not running"} {
		err := os.ErrNotExist
		if !looksLikeDockerExecGone(text) || !isMissingDockerExec(cvRErrorsJoin(err, text)) || !IsSandboxInterrupt(cvRErrorsJoin(err, text)) {
			t.Fatalf("not classified: %s", text)
		}
	}
	if !IsSandboxInterrupt(context.Canceled) || IsSandboxInterrupt(nil) {
		t.Fatal("interrupt classification")
	}
	if !IsCwdMountFailure(cvRErrorsJoin(os.ErrInvalid, "exit status 126")) || isDockerCwdUnsafe("", nil) {
		t.Fatal("cwd classification")
	}
	line := "Error response from daemon: No such container: box"
	if excerpt := dockerInterruptExcerpt("noise\n" + line + "\n"); excerpt != line {
		t.Fatalf("excerpt=%q", excerpt)
	}
	if annotateDockerExecError(line, os.ErrInvalid) == os.ErrInvalid {
		t.Fatal("docker error was not annotated")
	}
	if !ephemeralDockerImage(tempDockerImageName("x")) || ephemeralDockerImage(dockerCachedImagePrefix+"x") {
		t.Fatal("ephemeral image classification")
	}
}

func TestDockerManagerReclaimsCachedImages(t *testing.T) {
	workspace := t.TempDir()
	cvRInstallFakeDocker(t, workspace)
	m := NewManager(workspace)
	count, err := m.ReclaimCachedDockerImages(context.Background(), nil)
	if err != nil || count != 1 {
		t.Fatalf("manager image reclaim=%d err=%v", count, err)
	}
}

func TestDockerExecRecoversFromUnsafeCWD(t *testing.T) {
	workspace := t.TempDir()
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "failed-once")
	script := `#!/bin/sh
workspace="${CVR_DOCKER_WORKSPACE}"
marker="${CVR_DOCKER_MARKER}"
if [ "${1-}" = inspect ]; then
  printf '[{"Destination":"/workspace","Source":"%s"}]\n' "$workspace"
  exit 0
fi
if [ "${1-}" = exec ]; then
  case "$*" in *"cd /workspace && pwd"*) echo /workspace; exit 0;; esac
  if [ ! -e "$marker" ]; then
    : > "$marker"
    echo 'OCI runtime exec failed: cwd is outside of container mount namespace'
    exit 126
  fi
  echo recovered
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CVR_DOCKER_WORKSPACE", workspace)
	t.Setenv("CVR_DOCKER_MARKER", marker)
	sb := &dockerSandbox{id: "recover", name: "box", workspace: workspace, started: true}
	out, err := sb.Exec(context.Background(), CommandSpec{Argv: []string{"sh", "-c", "echo x"}}, nil)
	if err != nil || !strings.Contains(out, "recovered") || !sb.started {
		t.Fatalf("out=%q started=%v err=%v", out, sb.started, err)
	}
}

func TestDockerExecInvalidatesMissingContainer(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\necho 'Error: No such container: box'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	sb := &dockerSandbox{name: "box", workspace: t.TempDir(), started: true}
	if _, err := sb.Exec(context.Background(), CommandSpec{Argv: []string{"true"}}, nil); err == nil || sb.started {
		t.Fatalf("started=%v err=%v", sb.started, err)
	}
}

func cvRInstallFakeDocker(t *testing.T, workspace string) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
set -eu
workspace="${CVR_DOCKER_WORKSPACE}"
if [ "${1-}" = inspect ] && [ "${2-}" = -f ]; then
  printf '/agentflow-cvr\t%s\n' "$workspace"
  exit 0
fi
case "$*" in
  "inspect --type container --format {{json .Mounts}} box")
    printf '[{"Destination":"/workspace","Source":"%s"}]\n' "$workspace"; exit 0 ;;
  "inspect --type container "*) echo 'Error: No such container' >&2; exit 1 ;;
  "inspect --format {{.State.Running}}|{{.Id}}|{{.State.StartedAt}} "*) echo 'true|container-id|now'; exit 0 ;;
  "image inspect --format {{.Config.User}} "*) exit 0 ;;
  "image inspect "*) exit 1 ;;
  "images --format {{.Repository}} --filter reference=agentflow-img-*"*) printf 'agentflow-img-a:latest\nagentflow-img-b\nagentflow-img-a\n'; exit 0 ;;
  "ps -a --format {{.Image}}"*) echo agentflow-img-b; exit 0 ;;
  "ps -aq --filter "*) echo id1; exit 0 ;;
  "inspect "*) exit 1 ;;
  "exec "*) echo /workspace; exit 0 ;;
  *) exit 0 ;;
esac
`
	path := filepath.Join(bin, "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CVR_DOCKER_WORKSPACE", workspace)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func cvRErrorsJoin(base error, text string) error {
	return &cvRTextError{base: base, text: text}
}

type cvRTextError struct {
	base error
	text string
}

func (e *cvRTextError) Error() string { return e.text + ": " + e.base.Error() }
func (e *cvRTextError) Unwrap() error { return e.base }
