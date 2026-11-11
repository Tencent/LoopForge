package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestGitLabDockerLaunchRedactsTelemetryAndOutput(t *testing.T) {
	for _, provider := range []string{"command", "script"} {
		for _, fail := range []bool{false, true} {
			t.Run(provider+map[bool]string{false: "/success", true: "/failure"}[fail], func(t *testing.T) {
				workspace := t.TempDir()
				cvRInstallFakeDocker(t, workspace)
				docker, err := exec.LookPath("docker")
				if err != nil {
					t.Fatal(err)
				}
				stub, err := os.ReadFile(docker)
				if err != nil {
					t.Fatal(err)
				}
				// Run the actual docker launch paths against a fake CLI. Verify the
				// process receives the PAT, then echo it in both output streams.
				replacement := `"exec "*)
  case " $* " in *" AGENTFLOW_GITLAB_TOKEN=gitlab-launch-test-secret "*) ;; *) exit 77 ;; esac
  printf 'stdout: gitlab-launch-test-secret\n'
  printf 'stderr: gitlab-launch-test-secret' >&2
  exit "${GITLAB_TEST_EXIT:-0}" ;;
`
				if err := os.WriteFile(docker, []byte(strings.Replace(string(stub), `"exec "*) echo /workspace; exit 0 ;;`, replacement, 1)), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GITLAB_TEST_EXIT", map[bool]string{false: "0", true: "3"}[fail])
				gate := filepath.Join(t.TempDir(), "gate")
				if err := os.WriteFile(gate, []byte("#!/bin/sh\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("AGENTFLOW_GATE_MCP_BINARY", gate)
				sb := &dockerSandbox{name: "box", workspace: workspace, started: true}
				command, err := bindCommand(sb, workspace, CommandSpec{Provider: provider, Argv: []string{"sh", "-c", "echo ok"}, CredentialBindings: []CredentialBinding{BindGit}}, RunCredentials{GitLabBaseURL: "https://gitlab.example.test", GitLabToken: "gitlab-launch-test-secret"})
				if err != nil {
					t.Fatal(err)
				}
				var events []domain.TelemetryEvent
				out, err := sb.Exec(context.Background(), command, func(event domain.TelemetryEvent) { events = append(events, event) })
				if fail {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 3 {
						t.Fatalf("error = %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(out, "gitlab-launch-test-secret") || !strings.Contains(out, "<redacted>") {
					t.Fatalf("unsafe or missing output: %q", out)
				}
				seen := map[string]bool{}
				for _, event := range events {
					seen[event.Name] = true
					if strings.Contains(event.Message, "gitlab-launch-test-secret") {
						t.Fatalf("credential in %s: %s", event.Name, event.Message)
					}
				}
				for _, name := range []string{"process.started", "process.stdout", "process.stderr", "process.finished"} {
					if !seen[name] {
						t.Fatalf("missing %s", name)
					}
				}
				if err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if entry.IsDir() {
						return nil
					}
					raw, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if strings.Contains(string(raw), "gitlab-launch-test-secret") {
						t.Errorf("credential persisted to %s", path)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestCompactCommandRedactsGitLabAndHostPassword(t *testing.T) {
	for _, key := range []string{"AGENTFLOW_GITLAB_TOKEN", "AGENTFLOW_GIT_HTTP_PASSWORD"} {
		args := []string{"docker", "exec", "-e", key + "=test-secret", "box", "true"}
		if got := compactCommand(args); strings.Contains(got, "test-secret") || !strings.Contains(got, key+"=<redacted>") {
			t.Fatalf("unsafe command: %s", got)
		}
		if args[3] != key+"=test-secret" {
			t.Fatal("telemetry mutated launch arguments")
		}
	}
}

func TestGitLabProcessStartFailureRedactsError(t *testing.T) {
	cmd := exec.Command(filepath.Join(t.TempDir(), "missing-gitlab-error-secret"))
	cmd.Env = []string{"AGENTFLOW_GITLAB_TOKEN=gitlab-error-secret"}
	var events []domain.TelemetryEvent
	_, err := runCommand(cmd, func(event domain.TelemetryEvent) { events = append(events, event) })
	var pathError *os.PathError
	if err == nil || !errors.As(err, &pathError) || strings.Contains(err.Error(), "gitlab-error-secret") {
		t.Fatalf("unsafe error or lost cause: %v", err)
	}
	for _, event := range events {
		if strings.Contains(event.Message, "gitlab-error-secret") {
			t.Fatalf("unsafe event: %+v", event)
		}
	}
}
