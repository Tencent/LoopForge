//go:build sandboxlive

package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runner"
	"agentflow/internal/runtime"
)

func TestDesktopSSHReachesSameDockerWorkspace(t *testing.T) {
	binary := os.Getenv("LOOPFORGE_TEST_DESKTOP_SSH_BINARY")
	if binary == "" || exec.Command("docker", "info").Run() != nil {
		t.Skip("set LOOPFORGE_TEST_DESKTOP_SSH_BINARY to the built desktop companion and start Docker")
	}
	for _, execution := range []string{domain.ExecutionModeCloud, domain.ExecutionModeRunner} {
		t.Run(execution, func(t *testing.T) {
			testDesktopSSHWorkspace(t, binary, execution)
		})
	}
}

func testDesktopSSHWorkspace(t *testing.T, binary, execution string) {
	t.Helper()
	t.Setenv("AGENTFLOW_SESSION_SECRET", rand.Text())
	t.Setenv("AGENTFLOW_WORKSPACE_SSH_ENABLED", "true")
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	bootstrap := rand.Text()
	t.Setenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN", bootstrap)
	t.Setenv("LOOPFORGE_DESKTOP_SSH_CONFIG_DIR", t.TempDir())

	// The Docker VM must see the same bind source as this process.
	root, err := os.MkdirTemp(".", ".desktop-ssh-live-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	a, owner, _, _, _ := sharedWorkbenchFixture(t)
	manager := runtime.NewManager(filepath.Join(root, "platform"))
	e := engine.New(a.repo, manager)
	server := httptest.NewServer(New(a.repo, e, nil, "", ""))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	run, _ := a.repo.RunState("shared-run")
	run.ID = "desktop-ssh-" + strings.ToLower(rand.Text()[:10])
	run.ExecutionContext.ExecutionMode = execution
	if execution == domain.ExecutionModeRunner {
		runnerRoot := filepath.Join(root, "runner")
		stateFile := filepath.Join(root, "runner-state.json")
		d, err := runner.New(runner.Config{
			ServerURL: server.URL, BootstrapToken: bootstrap, Name: run.ID,
			StatePath: stateFile, DefaultWorkspaceRoot: runnerRoot, DisableAutoUpdate: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		runnerCtx, stop := context.WithCancel(ctx)
		stopped := make(chan error, 1)
		go func() { stopped <- d.Run(runnerCtx) }()
		defer func() {
			stop()
			select {
			case <-stopped:
			case <-time.After(35 * time.Second):
				t.Error("test runner did not stop")
			}
		}()
		for run.ExecutionContext.ExecutionRunnerID == "" {
			data, err := os.ReadFile(stateFile)
			var state struct{ RunnerID string }
			if err == nil && json.Unmarshal(data, &state) == nil {
				run.ExecutionContext.ExecutionRunnerID = state.RunnerID
			}
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			time.Sleep(50 * time.Millisecond)
		}
		run.ExecutionContext.RunnerWorkspaceLayout = domain.RunnerWorkspaceLayoutShared
		if err := os.MkdirAll(filepath.Join(runnerRoot, "runs", run.ID, "workspace"), 0700); err != nil {
			t.Fatal(err)
		}
	} else {
		defer manager.DiscardRunContainer(context.Background(), run.ID)
		if err := os.MkdirAll(manager.RunWorkspace(run.ID), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	token := issueWorkspaceSSHTestToken(t, a.repo, owner.ID)
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if out, err := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	configure := exec.CommandContext(ctx, binary, "desktop-ssh", "configure", "-server", server.URL, "-identity", keyPath)
	configure.Stdin = strings.NewReader(token)
	config, err := configure.CombinedOutput()
	if err != nil {
		t.Fatalf("configure: %v: %s", err, config)
	}
	configFile := filepath.Join(t.TempDir(), "ssh_config")
	if err := os.WriteFile(configFile, config, 0600); err != nil {
		t.Fatal(err)
	}
	alias := "loopforge-" + hex.EncodeToString([]byte(server.URL)) + "-" + hex.EncodeToString([]byte(run.ID))
	sshCommand := func(command string) *exec.Cmd {
		return exec.CommandContext(ctx, "ssh", "-F", configFile, "-o", "BatchMode=yes", "-o", "ConnectTimeout=30", alias, command)
	}

	// Keep one SSH connection open while a second edits the same checkout.
	first := sshCommand("printf ready; sleep 15")
	stdout, err := first.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	first.Stderr = os.Stderr
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Process.Kill(); _ = first.Wait() }()
	var ready [5]byte
	if _, err := io.ReadFull(stdout, ready[:]); err != nil || string(ready[:]) != "ready" {
		t.Fatalf("first workspace SSH connection: %q %v", ready, err)
	}
	out, err := sshCommand("printf 'desktop-edit' > ssh-marker.txt && test \"$PWD\" = /workspace && hostname").CombinedOutput()
	if err != nil {
		t.Fatalf("SSH edit: %v: %s", err, out)
	}
	// Docker uses the container's short ID as its hostname. No port is mapped.
	container := strings.TrimSpace(string(out))
	if _, err := hex.DecodeString(container); err != nil || len(container) != 12 {
		t.Fatalf("unexpected sandbox hostname: %q", container)
	}
	mapped, err := exec.CommandContext(ctx, "docker", "port", container).CombinedOutput()
	if err != nil || len(mapped) != 0 {
		t.Fatalf("sandbox published a host port: %v: %s", err, mapped)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/runs/"+run.ID+"/workspace/file?path=ssh-marker.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var file struct{ Content string }
	if err := json.NewDecoder(response.Body).Decode(&file); err != nil || response.StatusCode != http.StatusOK || file.Content != "desktop-edit" {
		t.Fatalf("workspace file after SSH edit: %d, %q, %v", response.StatusCode, file.Content, err)
	}
}
