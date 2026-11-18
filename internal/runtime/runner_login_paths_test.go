package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// Exercise the same preparation, process-pool and Sandbox.Exec boundaries as
// real tasks. The CLI fixture forces renewal and then plants an untrusted token
// in its HOME; only the manager's issuer response may reach the host.
func TestManagedLoginRuntimePaths(t *testing.T) {
	if os.Getenv("LOOPFORGE_PROBE_AUTH_SANDBOXES") != "1" {
		t.Skip("opt in to real native/Docker sandboxes with fake credentials")
	}
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "python:3.13-alpine")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	for _, provider := range []string{"codex", "claude-code"} {
		for _, driver := range []string{"native", "docker"} {
			for _, mode := range []string{"conversation", "sop"} {
				t.Run(provider+"/"+driver+"/"+mode, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
					defer cancel()
					cache, err := os.UserCacheDir()
					if err != nil {
						t.Fatal(err)
					}
					root, err := os.MkdirTemp(cache, "lf-auth-sandbox-")
					if err != nil {
						t.Fatal(err)
					}
					defer os.RemoveAll(root)
					workspace := filepath.Join(root, "ws")
					if err := os.MkdirAll(workspace, 0700); err != nil {
						t.Fatal(err)
					}
					fixturePath := filepath.Join(workspace, "fake-cli")
					if err := os.WriteFile(fixturePath, []byte("#!/usr/bin/env python3\n"+managedAuthCLIFixture), 0755); err != nil {
						t.Fatal(err)
					}
					override := "AGENTFLOW_CODEX_BIN"
					if provider == "claude-code" {
						override = "AGENTFLOW_CLAUDE_BIN"
					}
					t.Setenv(override, fixturePath)
					source, _, renewed := managedLoginFixture(t, provider)
					login, _ := runnerLoginSpec(provider)
					t.Setenv(login.configEnv, filepath.Dir(source.path))
					commandEnv := map[string]string{"LOOPFORGE_FAKE_SOURCE": source.path}
					var exchanges atomic.Int32
					previous := nativeRunnerLogins
					nativeRunnerLogins = &runnerLoginManager{stateDir: t.TempDir(), refresh: func(context.Context, string, []byte) ([]byte, error) { exchanges.Add(1); return renewed, nil }}
					defer func() { nativeRunnerLogins = previous }()
					profile := domain.SandboxProfile{Driver: driver}
					dockerfile := domain.DockerfileDefinition{Content: IsolatedDockerfile}
					binary := fixturePath
					if driver == "docker" {
						binary = "/workspace/fake-cli"
					}
					command := CommandSpec{Provider: provider, Argv: []string{binary, "task prompt"}, environment: commandEnv}
					var out string
					if mode == "conversation" {
						pool := &ConversationProcessPool{}
						spec := &domain.RunnerSandboxSpec{Profile: profile, Dockerfile: &dockerfile}
						factory := func(ctx context.Context, _ string) (Sandbox, func(), error) {
							sb, err := OpenRemoteSandbox(ctx, fmt.Sprintf("auth-conversation-%d", time.Now().UnixNano()), workspace, profile, SandboxRequirements{ConversationRoot: root}, SandboxResources{Dockerfile: dockerfile})
							if err != nil {
								return nil, nil, err
							}
							release := func() { _ = sb.Close(context.Background()) }
							if docker, ok := sb.(*dockerSandbox); ok {
								if err := docker.BindWorkspace(ctx); err != nil {
									release()
									return nil, nil, err
								}
								// Install only this harmless fixture in the disposable container.
								out, err := exec.CommandContext(ctx, "docker", "exec", "--user", "0:0", docker.name, "cp", "/workspace/fake-cli", "/usr/local/bin/"+conversationBinary(provider)).CombinedOutput()
								if err != nil {
									release()
									return nil, nil, fmt.Errorf("fixture install: %w: %s", err, out)
								}
							}
							return sb, release, nil
						}
						started, err := pool.Handle(ctx, root, workspace, spec, ConversationProcessRequest{Operation: "start", Command: &ConversationCommand{
							Argv: command.Argv, Provider: provider, Environment: commandEnv, Bindings: []CredentialBinding{login.binding}, RunnerLogin: true}}, nil, factory)
						if err != nil {
							t.Fatal(err)
						}
						defer pool.Handle(context.Background(), root, workspace, spec, ConversationProcessRequest{Operation: "stop", ID: started.ID}, nil)
						var after int64
						for {
							result, err := pool.Handle(ctx, root, workspace, spec, ConversationProcessRequest{Operation: "poll", ID: started.ID, After: after}, nil)
							if err != nil {
								t.Fatal(err)
							}
							for _, event := range result.Events {
								out += event.Message + "\n"
							}
							after = result.Sequence
							if result.Done {
								if result.Error != "" {
									t.Fatalf("conversation: %s; fake output %.1500s", result.Error, out)
								}
								break
							}
						}
					} else {
						requirements := ProviderRequirements(provider)
						requirements.RunnerLoginRelay = driver == "docker" && provider == "codex"
						sb, err := OpenRemoteSandbox(ctx, fmt.Sprintf("auth-%d", time.Now().UnixNano()), workspace, profile, requirements, SandboxResources{Dockerfile: dockerfile})
						if err != nil {
							t.Fatal(err)
						}
						defer sb.Close(context.Background())
						if docker, ok := sb.(*dockerSandbox); ok {
							// SOP workspace preparation may start the container before the CLI has a login lease.
							if err := docker.BindWorkspace(ctx); err != nil {
								t.Fatal(err)
							}
						}
						env, cleanup, err := PrepareRunnerSandboxLogin(sb, provider, "run", "node")
						if err != nil {
							t.Fatal(err)
						}
						defer cleanup()
						command = WithCommandEnvironment(command, env)
						out, err = sb.Exec(ctx, command, nil)
						if err != nil {
							t.Fatalf("sandbox command: %v; fake output %.1200s", err, out)
						}
					}
					if !strings.Contains(out, "managed-auth-ok") || strings.Contains(out, "oauth_token_refresh") {
						t.Fatalf("missing result or leaked control frame: %.1000s", out)
					}
					host, err := os.ReadFile(source.path)
					if err != nil || !bytes.Equal(host, renewed) || exchanges.Load() != 1 {
						t.Fatalf("host refresh/preservation failed; exchanges=%d", exchanges.Load())
					}
				})
			}
		}
	}
}

const managedAuthCLIFixture = `import json,os,sys,urllib.request
try:
 open(os.environ['LOOPFORGE_FAKE_SOURCE']).read()
except OSError:
 pass
else:
 raise AssertionError('native sandbox can read host refresh token')
if os.environ.get('CODEX_HOME'):
 path=os.path.join(os.environ['CODEX_HOME'],'auth.json')
 data=json.load(open(path)); nonce=data['tokens']['refresh_token']
 assert 'host-refresh' not in nonce
 request=urllib.request.Request(os.environ['CODEX_REFRESH_TOKEN_URL_OVERRIDE'],data=json.dumps({'grant_type':'refresh_token','refresh_token':nonce}).encode(),headers={'Content-Type':'application/json'})
 response=json.load(urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=15))
 assert response.get('access_token') and 'host-refresh' not in response['refresh_token']
 data['tokens']['refresh_token']='sandbox-account-replacement'
 json.dump(data,open(path,'w'))
 print(json.dumps({'type':'result','result':'managed-auth-ok'}),flush=True)
else:
 path=os.path.join(os.environ['CLAUDE_CONFIG_DIR'],'.credentials.json')
 data=json.load(open(path)); assert 'refreshToken' not in data['claudeAiOauth']
 for line in sys.stdin:
  event=json.loads(line)
  if event.get('type')=='control_request':
   print(json.dumps({'type':'control_response','response':{'subtype':'success','request_id':event['request_id']}}),flush=True)
  elif event.get('type')=='user':
   print(json.dumps({'type':'control_request','request_id':'refresh','request':{'subtype':'oauth_token_refresh'}}),flush=True)
  elif event.get('type')=='control_response':
   assert event['response']['response']['accessToken']=='claude-access-1'
   data['claudeAiOauth']['refreshToken']='sandbox-account-replacement'
   json.dump(data,open(path,'w'))
   print(json.dumps({'type':'result','result':'managed-auth-ok'}),flush=True)
   break
`
