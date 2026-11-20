package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

const runnerAuthFixture = `#!/bin/sh
set -eu
if [ "${1:-}" = --version ]; then
  printf '1.0.0\n'
  exit
fi
if [ "${1:-}" = workspace ]; then
  test "$(cat runner-workspace-marker)" = attached
fi
case "${0##*/}" in
  codex)
    test -z "${CODEX_API_KEY:-}${OPENAI_API_KEY:-}"
    grep -q '"fixture":"codex-login"' "$CODEX_HOME/auth.json"
    printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"fixture-authenticated"}}'
    ;;
  claude)
    test -z "${ANTHROPIC_API_KEY:-}"
    grep -q '"fixture":"claude-login"' "$CLAUDE_CONFIG_DIR/.credentials.json"
    printf '%s\n' '{"type":"result","subtype":"success","result":"fixture-authenticated"}'
    ;;
  cursor-agent)
    test "$CURSOR_API_KEY" = fixture-cursor-key
    printf '%s\n' '{"type":"result","subtype":"success","result":"fixture-authenticated"}'
    ;;
  agy)
    test "$GEMINI_API_KEY" = fixture-gemini-key
    grep -q '"modelProvider":"gemini"' "$HOME/.gemini/antigravity-cli/settings.json"
    printf '%s\n' '{"type":"result","subtype":"success","result":"fixture-authenticated"}'
    ;;
  *) exit 125 ;;
esac
if [ -d .agentflow/config ]; then
  gate="$(grep -oE '\.agentflow/gate-results/[^"]+\.json' .agentflow/config/*/mcp.json | head -1)"
  test -n "$gate"
  mkdir -p "$(dirname "$gate")"
  printf '%s\n' '{"decision":"pass","reason":"fixture-authenticated"}' > "$gate"
fi
`

func runnerSandboxAuthFixtures(t *testing.T, driver string) (string, domain.DockerfileDefinition) {
	t.Helper()
	if driver == domain.SandboxDriverDocker {
		if os.Getenv("AGENTFLOW_TEST_DOCKER") != "1" {
			t.Skip("set AGENTFLOW_TEST_DOCKER=1 for local Docker sandbox tests")
		}
		// Local Docker VMs such as Colima share the checkout but may not share
		// the OS temporary directory. Create bind sources in the shared root.
		useTempDirOutsideTmp(t, os.Getenv("AGENTFLOW_DOCKER_TEST_ROOT"))
	} else {
		switch goruntime.GOOS {
		case "darwin":
			if _, err := exec.LookPath("sandbox-exec"); err != nil {
				t.Skip(err)
			}
		case "linux":
			if out, err := exec.Command("bwrap", "--ro-bind", "/", "/", "--", "/bin/true").CombinedOutput(); err != nil {
				t.Skipf("bwrap is unavailable: %v %s", err, out)
			}
			// bwrap mounts a private tmpfs over /tmp after binding the
			// workspace, so fixture CLIs and run workspaces under the default
			// t.TempDir() would vanish inside the sandbox. A runner keeps its
			// workspaces under ~/.agentflow-runner; keep the fixtures out of
			// /tmp the same way.
			useTempDirOutsideTmp(t, "")
		default:
			t.Skip("the native sandbox requires macOS or Linux")
		}
	}
	root := t.TempDir()
	for _, login := range []struct{ config, file, data string }{
		{"CODEX_HOME", "auth.json", `{"fixture":"codex-login"}`},
		{"CLAUDE_CONFIG_DIR", ".credentials.json", `{"fixture":"claude-login"}`},
	} {
		dir := filepath.Join(root, login.config)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, login.file), []byte(login.data), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(login.config, dir)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("AGENTFLOW_CONVERSATION_RELAY_HOST", "host.docker.internal")
	t.Setenv("AGENTFLOW_CONVERSATION_DOCKER_NETWORK", "none")
	t.Setenv("AGENTFLOW_DOCKER_NETWORK", "none")
	for _, name := range []string{"codex", "claude", "cursor-agent", "agy"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(runnerAuthFixture), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	image := domain.DockerfileDefinition{
		ID: "runner-cli-auth-fixture", Name: "Runner CLI auth fixture",
		Content: "FROM python:3.13-alpine\nCOPY --chmod=755 cli-fixture /usr/local/bin/codex\nCOPY --chmod=755 cli-fixture /usr/local/bin/claude\nCOPY --chmod=755 cli-fixture /usr/local/bin/cursor-agent\nCOPY --chmod=755 cli-fixture /usr/local/bin/agy\n",
		Files:   map[string]string{"cli-fixture": runnerAuthFixture},
	}
	return root, image
}

func TestRunnerConversationWorkspaceCLIAuthentication(t *testing.T) {
	for _, driver := range []string{domain.SandboxDriverNative, domain.SandboxDriverDocker} {
		t.Run(driver, func(t *testing.T) {
			bin, image := runnerSandboxAuthFixtures(t, driver)
			d, target, workspace := runnerPreparationFixture(t)
			defer d.closeAllRunSandboxes(context.Background())
			target.Sandbox.Profile.Driver = driver
			target.Sandbox.Dockerfile = &image
			target.Workflow = &domain.WorkflowDefinition{}
			d.conversationForward = func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
				return nil, nil
			}
			if err := os.WriteFile(filepath.Join(workspace, "runner-workspace-marker"), []byte("attached"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			for i, cli := range []struct {
				provider, binary string
				binding          runtime.CredentialBinding
			}{
				{"codex", "codex", runtime.BindCodex},
				{"claude-code", "claude", runtime.BindClaudeCode},
				{"cursor", "cursor-agent", runtime.BindAgentCLI},
				{"agy", "agy", runtime.BindAgentCLI},
			} {
				t.Run(cli.provider, func(t *testing.T) {
					path := filepath.Join(bin, cli.binary)
					if driver == domain.SandboxDriverDocker {
						path = "/usr/local/bin/" + cli.binary
					}
					key := strings.Repeat(fmt.Sprint(i+1), 64)
					invoke := func(req runtime.ConversationProcessRequest) runtime.ConversationProcessResult {
						t.Helper()
						req.SessionKey = key
						target.Conversation = conversationRequest(t, req)
						response := d.conversationProcess(ctx, target)
						var result runtime.ConversationProcessResult
						if response.Status != 200 || json.Unmarshal(response.Payload, &result) != nil {
							t.Fatalf("conversation %s: %d %s", req.Operation, response.Status, response.Error)
						}
						return result
					}
					invoke(runtime.ConversationProcessRequest{Operation: "prepare"})
					result := invoke(runtime.ConversationProcessRequest{Operation: "start", Command: &runtime.ConversationCommand{
						Provider: cli.provider, Argv: []string{path, "workspace"},
						RunnerLogin: cli.binding != runtime.BindAgentCLI, Bindings: []runtime.CredentialBinding{cli.binding},
						Credentials: runtime.RunCredentials{AgentCLI: map[string]runtime.ProviderCredential{
							"cursor": {APIKey: "fixture-cursor-key"}, "agy": {APIKey: "fixture-gemini-key"},
						}},
						AgentCLI: runtime.AgentCLIOptions{Model: "fixture-model"},
					}})
					var output strings.Builder
					for !result.Done && ctx.Err() == nil {
						result = invoke(runtime.ConversationProcessRequest{Operation: "poll", ID: result.ID, After: result.Sequence})
						for _, event := range result.Events {
							output.WriteString(event.Message + "\n")
						}
						time.Sleep(10 * time.Millisecond)
					}
					if result.Error != "" || !strings.Contains(output.String(), "fixture-authenticated") {
						t.Fatalf("conversation CLI did not authenticate: %+v %s", result, output.String())
					}
				})
			}
		})
	}
}

func TestRunnerSOPCLIAuthentication(t *testing.T) {
	for _, driver := range []string{domain.SandboxDriverNative, domain.SandboxDriverDocker} {
		t.Run(driver, func(t *testing.T) {
			bin, image := runnerSandboxAuthFixtures(t, driver)
			server, st, en := newDaemonTestServer(t)
			workspaceRoot := t.TempDir()
			row, _ := startPoolRunnerWithConfig(t, st, Config{
				ServerURL: server.URL, BootstrapToken: "boot-secret", StatePath: filepath.Join(workspaceRoot, "identity.json"),
				Name: "cli-auth-runner", DefaultWorkspaceRoot: workspaceRoot, HeartbeatInterval: time.Second, LongPollTimeout: time.Second,
			})
			if err := st.PutUser(domain.User{LoginName: "cli-auth-test"}); err != nil {
				t.Fatal(err)
			}
			user, _ := st.UserByLoginName("cli-auth-test")
			row.OwnerUserID = user.ID
			for i := range row.Runtimes {
				if runtime.IsAgentCLI(row.Runtimes[i].Provider) {
					row.Runtimes[i].AuthMode = domain.RuntimeAuthPersonal
				}
			}
			if err := st.PutRunner(row); err != nil {
				t.Fatal(err)
			}
			if err := st.SetDefaultRunner(user.ID, row.ID, time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := st.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{
				"cursor": {APIKey: "fixture-cursor-key"}, "agy": {APIKey: "fixture-gemini-key"},
			}}); err != nil {
				t.Fatal(err)
			}
			if err := st.PutDockerfile(image); err != nil {
				t.Fatal(err)
			}
			profile := domain.SandboxProfile{ID: "runner-cli-auth-sandbox", Name: "CLI auth sandbox", Driver: driver}
			if driver == domain.SandboxDriverDocker {
				profile.DockerfileID = image.ID
			}
			if err := st.PutSandbox(profile); err != nil {
				t.Fatal(err)
			}
			for _, cli := range []struct{ kind, binary string }{
				{"codex", "codex"}, {"claude-code", "claude"}, {"cursor", "cursor-agent"}, {"agy", "agy"},
			} {
				t.Run(cli.kind, func(t *testing.T) {
					binary := filepath.Join(bin, cli.binary)
					if driver == domain.SandboxDriverDocker {
						binary = "/usr/local/bin/" + cli.binary
					}
					id := "runner-cli-auth-" + cli.kind
					if err := st.PutExecutor(domain.ExecutorDefinition{ID: id, Name: id, Kind: cli.kind, Binary: binary, Model: "fixture-model"}); err != nil {
						t.Fatal(err)
					}
					if err := st.PutAgent(domain.AgentDefinition{ID: id, Name: id, ExecutorID: id, Prompt: "{{input}}"}); err != nil {
						t.Fatal(err)
					}
					wf := domain.WorkflowDefinition{ID: id, Name: id, SandboxProfileID: profile.ID,
						Nodes: []domain.WorkflowNode{{ID: "agent", Name: "Authenticate", Type: "agent", AgentID: id}},
					}
					if err := st.PutWorkflow(wf); err != nil {
						t.Fatal(err)
					}
					run, err := en.StartWithExecutionContext(wf.ID, "test", domain.RepositoryContext{}, domain.TelemetryContext{ResponsibleUserID: user.ID})
					if err != nil {
						t.Fatal(err)
					}
					var result domain.Run
					waitFor(t, 20*time.Second, "SOP completion", func() bool {
						result, _ = st.RunState(run.ID)
						return domain.TerminalRunStatus(result.Status) || result.Status == "waiting"
					})
					node := result.Nodes["agent"]
					if result.Status != "succeeded" || result.ExecutionContext.ExecutionRunnerID != row.ID || !strings.Contains(node.Output, "fixture-authenticated") {
						for _, task := range st.RunnerTasks() {
							if task.RunID == run.ID && task.Provider == cli.kind {
								for _, event := range st.RunnerTaskEvents(task.ID) {
									if event.Type == "stderr" {
										t.Log(event.Data)
									}
								}
							}
						}
						t.Fatalf("SOP %s, node %s, error = %q, output = %q", result.Status, node.Status, node.Error, node.Output)
					}
					home := filepath.Join(workspaceRoot, "runs", run.ID, "workspace", ".agentflow", "private-home")
					for _, pattern := range []string{"codex-login-*/.codex/auth.json", "claude-code-login-*/.claude/.credentials.json"} {
						files, err := filepath.Glob(filepath.Join(home, filepath.FromSlash(pattern)))
						if err != nil || len(files) != 0 {
							t.Fatalf("SOP retained a temporary login file: %v, %v", files, err)
						}
					}
				})
			}
		})
	}
}

// useTempDirOutsideTmp points t.TempDir() at a fresh directory under root (the
// package directory when empty) for the rest of the test.
func useTempDirOutsideTmp(t *testing.T, root string) {
	t.Helper()
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	temp, err := os.MkdirTemp(root, ".runner-cli-auth-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temp) })
	t.Setenv("TMPDIR", temp)
}
