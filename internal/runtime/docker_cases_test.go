package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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
	const extraValue = "test-extra-credential"
	command, err := bindCommand(t.Context(), sb, workspace, CommandSpec{
		Argv:               []string{"sh", "-lc", "echo ok"},
		CredentialBindings: []CredentialBinding{BindExtra},
		ExtraCredentialEnv: map[string]string{"DEPLOYMENT_VALUE": "registry"},
		environment:        map[string]string{"B": "2"},
	}, RunCredentials{Extra: map[string]string{"registry": extraValue}})
	if err != nil {
		t.Fatal(err)
	}
	var started string
	out, err := sb.Exec(context.Background(), command, func(event domain.TelemetryEvent) {
		if event.Name == "process.started" {
			started = event.Message
		}
	})
	if err != nil || !strings.Contains(out, "/workspace") {
		t.Fatalf("exec=%q err=%v", out, err)
	}
	if strings.Contains(started, extraValue) || !strings.Contains(started, "DEPLOYMENT_VALUE=<redacted>") || !strings.Contains(started, "B=2") {
		t.Fatalf("process.started must redact the extra credential and keep ordinary environment variables: %q", started)
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

func TestDockerSandboxPrivilegedRequiresExecutionMachineSetting(t *testing.T) {
	const legacyVariable = "AGENTFLOW_ALLOW_PRIVILEGED_SANDBOX"
	t.Setenv(legacyVariable, "true")
	profile := domain.SandboxProfile{
		Driver:      domain.SandboxDriverDocker,
		Privileged:  true,
		Environment: map[string]string{legacyVariable: "true"},
	}
	resources := SandboxResources{Dockerfile: domain.DockerfileDefinition{Content: "FROM scratch"}}
	manager := NewManager(t.TempDir())
	if _, err := manager.AcquireRun(t.Context(), "privileged", profile, SandboxRequirements{}, RunCredentials{}, resources); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("platform worker accepted privileged sandbox without the machine setting: %v", err)
	}
	workspace := t.TempDir()
	if _, err := OpenRemoteSandbox(t.Context(), "privileged", workspace, profile, SandboxRequirements{}, resources); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("runner accepted privileged sandbox without the machine setting: %v", err)
	}
	t.Setenv(legacyVariable, "false")
	ctx := WithPrivilegedSandboxSetting(t.Context(), func(context.Context) (bool, error) { return true, nil })
	if _, err := OpenRemoteSandbox(ctx, "privileged", workspace, profile, SandboxRequirements{}, resources); err != nil {
		t.Fatalf("runner ignored the machine setting: %v", err)
	}
}

func TestDockerSandboxHonorsPrivilegedSandboxSettingChanges(t *testing.T) {
	workspace := t.TempDir()
	cvRInstallFakeDocker(t, workspace)
	profile := domain.SandboxProfile{Driver: domain.SandboxDriverDocker, Privileged: true}
	resources := SandboxResources{Dockerfile: domain.DockerfileDefinition{Content: "FROM scratch"}}
	enabled := false
	ctx := WithPrivilegedSandboxSetting(t.Context(), func(context.Context) (bool, error) { return enabled, nil })
	open := func() (Sandbox, error) {
		return OpenRemoteSandbox(ctx, "privileged", workspace, profile, SandboxRequirements{}, resources)
	}
	if _, err := open(); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("Docker sandbox opened with the setting off: %v", err)
	}
	enabled = true
	sb, err := open()
	if err != nil {
		t.Fatalf("Docker sandbox did not open with the setting on: %v", err)
	}
	defer sb.Close(t.Context())
	enabled = false
	if err := sb.(*dockerSandbox).ensureStarted(t.Context()); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("new Docker start ignored the newer setting: %v", err)
	}
	enabled = true
	if err := sb.(*dockerSandbox).ensureStarted(t.Context()); err != nil {
		t.Fatalf("new Docker start rejected the enabled setting: %v", err)
	}
	enabled = false
	if err := sb.(*dockerSandbox).Recreate(t.Context()); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("Docker recreation ignored the newer setting: %v", err)
	}
}

func TestDockerSandboxReplacesContainerWhenPrivilegeChanges(t *testing.T) {
	for _, tc := range []struct {
		name       string
		privileged bool
	}{
		{name: "enable", privileged: true},
		{name: "disable", privileged: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			cvRInstallFakeDocker(t, workspace)
			argsFile := filepath.Join(t.TempDir(), "create-args")
			t.Setenv("CVR_DOCKER_CREATE_ARGS", argsFile)
			t.Setenv("CVR_DOCKER_EXISTING_PRIVILEGED", strconv.FormatBool(!tc.privileged))
			sb := &dockerSandbox{
				name:              "box",
				workspace:         workspace,
				profile:           domain.SandboxProfile{Driver: "docker", Privileged: tc.privileged, ReadOnlyRootFS: true},
				dockerfile:        domain.DockerfileDefinition{Content: "FROM scratch"},
				privilegedSetting: func(context.Context) (bool, error) { return true, nil },
			}
			if err := sb.BindWorkspace(t.Context()); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatalf("expected docker create when privilege changes: %v", err)
			}
			args := strings.Split(strings.TrimSpace(string(data)), "\n")
			if slices.Contains(args, "--privileged") != tc.privileged ||
				slices.Contains(args, "--cgroupns=private") != tc.privileged ||
				slices.Contains(args, "--cap-drop=ALL") == tc.privileged ||
				slices.Contains(args, "no-new-privileges") == tc.privileged ||
				!slices.Contains(args, "--read-only") {
				t.Fatalf("unexpected docker create args: %q", args)
			}
			if os.Geteuid() != 0 {
				wantUser := strconv.Itoa(os.Geteuid()) + ":" + strconv.Itoa(os.Getegid())
				if !strings.Contains(string(data), "\n--user\n"+wantUser+"\n") {
					t.Fatalf("docker create must map the default image user to %s: %q", wantUser, args)
				}
			}
		})
	}
}

func TestDockerSandboxPreparesPrivilegedCgroup(t *testing.T) {
	t.Run("create and reuse run root cgroup setup only when privileged", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			privileged bool
			reuse      bool
			cgroupns   string
			wantCreate bool
		}{
			{name: "privileged create", privileged: true, wantCreate: true},
			{name: "privileged reuse", privileged: true, reuse: true, cgroupns: "private", wantCreate: false},
			{name: "privileged host cgroupns recreated", privileged: true, reuse: true, cgroupns: "host", wantCreate: true},
			{name: "unprivileged create", privileged: false, wantCreate: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				workspace := t.TempDir()
				bin := t.TempDir()
				logFile := filepath.Join(t.TempDir(), "docker.log")
				script := `#!/bin/sh
set -eu
log="${CVR_DOCKER_LOG}"
printf '%s\n' "$*" >> "$log"
case "$*" in
  "inspect --type container --format "*" box")
    if [ "${CVR_DOCKER_REUSE:-0}" = "1" ]; then
      printf '{"mounts":[{"Destination":"/workspace","Source":"%s"}],"privileged":true,"cgroupnsMode":"%s"}\n' "$CVR_DOCKER_WORKSPACE" "${CVR_DOCKER_CGROUPNS:-}"
      exit 0
    fi
    echo 'Error: No such container' >&2; exit 1 ;;
  "image inspect "*) exit 0 ;;
  "exec box "*) echo /workspace; exit 0 ;;
  *) exit 0 ;;
esac
`
				if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("CVR_DOCKER_LOG", logFile)
				t.Setenv("CVR_DOCKER_WORKSPACE", workspace)
				if tc.reuse {
					t.Setenv("CVR_DOCKER_REUSE", "1")
					t.Setenv("CVR_DOCKER_CGROUPNS", tc.cgroupns)
				}
				sb := &dockerSandbox{
					name:              "box",
					workspace:         workspace,
					profile:           domain.SandboxProfile{Driver: "docker", Privileged: tc.privileged, ReadOnlyRootFS: true},
					dockerfile:        domain.DockerfileDefinition{Content: "FROM scratch"},
					privilegedSetting: func(context.Context) (bool, error) { return true, nil },
				}
				if err := sb.BindWorkspace(t.Context()); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(logFile)
				if err != nil {
					t.Fatal(err)
				}
				calls := string(data)
				hasCreate := strings.Contains(calls, "create --name box ")
				if hasCreate != tc.wantCreate {
					t.Fatalf("create=%v want=%v calls:\n%s", hasCreate, tc.wantCreate, calls)
				}
				if tc.wantCreate && tc.privileged && !strings.Contains(calls, " --privileged --cgroupns=private ") {
					t.Fatalf("expected --privileged --cgroupns=private in calls:\n%s", calls)
				}
				hasCgroupSetup := strings.Contains(calls, "exec --user 0:0 box sh -c ") &&
					strings.Contains(calls, "/sys/fs/cgroup/init") &&
					strings.Contains(calls, "cgroup.subtree_control")
				if hasCgroupSetup != tc.privileged {
					t.Fatalf("cgroup setup=%v want=%v calls:\n%s", hasCgroupSetup, tc.privileged, calls)
				}
			})
		}
	})

	t.Run("cgroup setup failure cleans up new container", func(t *testing.T) {
		workspace := t.TempDir()
		bin := t.TempDir()
		logFile := filepath.Join(t.TempDir(), "docker.log")
		script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "${CVR_DOCKER_LOG}"
case "$*" in
  "inspect --type container --format "*" box") echo 'Error: No such container' >&2; exit 1 ;;
  "image inspect "*) exit 0 ;;
  "exec --user 0:0 box "*) echo "write error: device or resource busy" >&2; exit 1 ;;
  "exec box "*) echo /workspace; exit 0 ;;
  *) exit 0 ;;
esac
`
		if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("CVR_DOCKER_LOG", logFile)
		sb := &dockerSandbox{
			name:              "box",
			workspace:         workspace,
			profile:           domain.SandboxProfile{Driver: "docker", Privileged: true},
			dockerfile:        domain.DockerfileDefinition{Content: "FROM scratch"},
			privilegedSetting: func(context.Context) (bool, error) { return true, nil },
		}
		err := sb.BindWorkspace(t.Context())
		if err == nil || !strings.Contains(err.Error(), "prepare privileged sandbox cgroup") || sb.started {
			t.Fatalf("expected cgroup setup error and started=false, got started=%v err=%v", sb.started, err)
		}
		data, readErr := os.ReadFile(logFile)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(data), "rm -f box") {
			t.Fatalf("expected failed container cleanup, calls:\n%s", data)
		}
	})
}

func TestDockerSandboxUsesRelayNetworkOnlyWhenRequested(t *testing.T) {
	for _, tc := range []struct {
		name               string
		privateHome        bool
		conversation       bool
		runnerLoginRelay   bool
		invalidRelayConfig bool
		wantNetwork        string
	}{
		{name: "LOOP with private home", privateHome: true, wantNetwork: "sop-network"},
		{name: "LOOP ignores invalid conversation settings", privateHome: true, invalidRelayConfig: true, wantNetwork: "sop-network"},
		{name: "conversation", conversation: true, wantNetwork: "chat-network"},
		{name: "Codex login relay", runnerLoginRelay: true, wantNetwork: "chat-network"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			cvRInstallFakeDocker(t, workspace)
			argsFile := filepath.Join(t.TempDir(), "create-args")
			t.Setenv("CVR_DOCKER_CREATE_ARGS", argsFile)
			t.Setenv("AGENTFLOW_DOCKER_NETWORK", "sop-network")
			t.Setenv("AGENTFLOW_CONVERSATION_DOCKER_NETWORK", "chat-network")
			host := "host.docker.internal"
			if tc.invalidRelayConfig {
				host = "invalid/relay"
			}
			t.Setenv("AGENTFLOW_CONVERSATION_RELAY_HOST", host)
			requirements := SandboxRequirements{PrivateHome: tc.privateHome, RunnerLoginRelay: tc.runnerLoginRelay}
			if tc.conversation {
				requirements.ConversationRoot = t.TempDir()
			}
			ctx := context.Background()
			sandbox, err := createDocker(ctx, "network-policy", workspace, domain.SandboxProfile{Driver: "docker"}, requirements, domain.DockerfileDefinition{Content: "FROM scratch"})
			if err != nil {
				t.Fatal(err)
			}
			defer sandbox.Close(ctx)
			if err := sandbox.(*dockerSandbox).BindWorkspace(ctx); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			args := string(data)
			if strings.Count(args, "\n--network\n") != 1 || !strings.Contains(args, "\n--network\n"+tc.wantNetwork+"\n") {
				t.Fatalf("docker create used the wrong network: %s", args)
			}
			usesGateway := strings.Contains(args, "\n--add-host\nhost.docker.internal:host-gateway\n")
			if usesGateway != (tc.conversation || tc.runnerLoginRelay) {
				t.Fatalf("docker create host gateway = %v for %s", usesGateway, tc.name)
			}
		})
	}
}

func TestDockerDiscoveryImagesAndBuildHelpers(t *testing.T) {
	workspace := t.TempDir()
	cvRInstallFakeDocker(t, workspace)
	ctx := context.Background()
	info, err := inspectDockerWorkspace(ctx, "box")
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
  printf '{"mounts":[{"Destination":"/workspace","Source":"%s"}],"privileged":false}\n' "$workspace"
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

func TestSandboxEnvironmentIdentityRecoversExternallyStoppedOrRemovedContainer(t *testing.T) {
	bin, state := t.TempDir(), t.TempDir()
	// A stateful docker stand-in: "status" holds running/exited, "generation"
	// counts docker create and "starts" counts docker start.
	script := `#!/bin/sh
state="${CVR_DOCKER_STATE}"
status() { cat "$state/status" 2>/dev/null; }
count() { n=$(cat "$state/$1" 2>/dev/null || echo 0); echo $((n + 1)) > "$state/$1"; }
case "$*" in
  "inspect --format {{.State.Running}}|{{.Id}}|{{.State.StartedAt}} box")
    case "$(status)" in
      running) echo "true|id-$(cat "$state/generation")|start-$(cat "$state/starts")"; exit 0 ;;
      exited) echo "false|id-$(cat "$state/generation")|start-$(cat "$state/starts")"; exit 0 ;;
    esac
    echo 'Error: No such object: box' >&2; exit 1 ;;
  "inspect --type container --format "*" box")
    [ -n "$(status)" ] || { echo 'Error: No such container: box' >&2; exit 1; }
    printf '{"mounts":[{"Destination":"/workspace","Source":"%s"}],"privileged":false}\n' "$CVR_DOCKER_WORKSPACE"; exit 0 ;;
  "start box") [ -n "$(status)" ] || exit 1; echo running > "$state/status"; count starts; exit 0 ;;
  "create --name box "*) echo exited > "$state/status"; count generation; exit 0 ;;
  "rm -f box") rm -f "$state/status"; exit 0 ;;
  "exec box "*) [ "$(status)" = running ] || { echo 'Error response from daemon: container box is not running' >&2; exit 1; }
    case "$*" in *"cd /workspace && pwd"*) echo /workspace ;; esac; exit 0 ;;
  "image inspect --format {{.Config.User}} "*) exit 0 ;;
  "image inspect "*) exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CVR_DOCKER_STATE", state)
	t.Setenv("CVR_DOCKER_WORKSPACE", workspace)
	for name, value := range map[string]string{"status": "running", "generation": "1", "starts": "1"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	sb := &dockerSandbox{id: "box", name: "box", workspace: workspace, started: true,
		dockerfile: domain.DockerfileDefinition{Content: "FROM alpine:3.20\n"}}
	identity, err := SandboxEnvironmentIdentity(ctx, sb)
	if err != nil {
		t.Fatal(err)
	}
	for _, external := range []string{"stopped", "removed"} {
		if external == "stopped" {
			err = os.WriteFile(filepath.Join(state, "status"), []byte("exited\n"), 0o644)
		} else {
			err = os.Remove(filepath.Join(state, "status"))
		}
		if err != nil {
			t.Fatal(err)
		}
		// The first call after an external stop/removal recovers; it must not
		// keep failing on the cached started flag.
		next, err := SandboxEnvironmentIdentity(ctx, sb)
		if err != nil || next == identity || !sb.started {
			t.Fatalf("container %s: identity %q (was %q) started=%v err=%v", external, next, identity, sb.started, err)
		}
		if again, err := SandboxEnvironmentIdentity(ctx, sb); err != nil || again != next {
			t.Fatalf("container %s: identity unstable %q != %q %v", external, again, next, err)
		}
		identity = next
	}
	if data, err := os.ReadFile(filepath.Join(state, "generation")); err != nil || strings.TrimSpace(string(data)) != "2" {
		t.Fatalf("removed container was not recreated exactly once: %q %v", data, err)
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
  "inspect --type container --format "*" box")
    printf '{"mounts":[{"Destination":"/workspace","Source":"%s"}],"privileged":%s}\n' "$workspace" "${CVR_DOCKER_EXISTING_PRIVILEGED:-false}"; exit 0 ;;
  "inspect --type container "*) echo 'Error: No such container' >&2; exit 1 ;;
  "inspect --format {{.State.Running}}|{{.Id}}|{{.State.StartedAt}} "*) echo 'true|container-id|now'; exit 0 ;;
  "image inspect --format {{.Config.User}} "*) exit 0 ;;
  "image inspect "*) exit 1 ;;
  "images --format {{.Repository}} --filter reference=agentflow-img-*"*) printf 'agentflow-img-a:latest\nagentflow-img-b\nagentflow-img-a\n'; exit 0 ;;
  "ps -a --format {{.Image}}"*) echo agentflow-img-b; exit 0 ;;
  "ps -aq --filter "*) echo id1; exit 0 ;;
  "inspect "*) exit 1 ;;
  "exec "*) echo /workspace; exit 0 ;;
  "create "*)
    if [ -n "${CVR_DOCKER_CREATE_ARGS:-}" ]; then printf '%s\n' "$@" > "$CVR_DOCKER_CREATE_ARGS"; fi
    exit 0 ;;
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
