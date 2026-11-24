package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

func TestSOPGitCredentialsUseAnEphemeralHostScopedRelay(t *testing.T) {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITLAB_TOKEN", "GLAB_TOKEN", "CNB_TOKEN"} {
		t.Setenv(key, "inherited-must-not-leak")
	}
	credentials := RunCredentials{GitHTTPCredentials: map[string]domain.GitHTTPExecutionCredential{
		"cnb.cool":           {GitHTTPCredential: domain.GitHTTPCredential{Host: "cnb.cool", Username: "cnb", Platform: "other"}, Token: "fake-cnb-sop-secret"},
		"hidden.example.org": {GitHTTPCredential: domain.GitHTTPCredential{Host: "hidden.example.org"}, Token: "hidden-sop-secret"},
	}}
	workspace := t.TempDir()
	script := `import os,subprocess
def fill(protocol,host):
 p=subprocess.run(['git','credential','fill'],input='protocol='+protocol+'\nhost='+host+'\n\n',text=True,capture_output=True)
 return dict(line.split('=',1) for line in p.stdout.splitlines() if '=' in line)
for key in ['GH_TOKEN','GITHUB_TOKEN','GH_ENTERPRISE_TOKEN','GITLAB_TOKEN','GLAB_TOKEN','CNB_TOKEN','AGENTFLOW_GIT_TOKEN']:
 assert not os.getenv(key),key
c=fill('https','cnb.cool')
assert c['username']=='cnb' and c['password']=='fake-cnb-sop-secret'
assert fill('https','hidden.example.org')['password']=='hidden-sop-secret'
for protocol,host in [('http','cnb.cool'),('https','cnb.cool:444'),('https','other.invalid')]:
 assert not fill(protocol,host).get('password')
print('BOUND_OK')
print(c['password'])
print('GATEWAY='+os.environ['AGENTFLOW_SESSION_GATEWAY'])
`
	command := CommandSpec{Argv: []string{"python3", "-c", script}, Provider: "script", CredentialBindings: []CredentialBinding{BindGit}}
	out, err := ExecuteLocalCommand(context.Background(), workspace, command, credentials, nil)
	if err != nil || !strings.Contains(out, "BOUND_OK") || !strings.Contains(out, "[redacted]") || strings.Contains(out, "fake-cnb-sop-secret") {
		t.Fatalf("SOP binding: %v %s", err, out)
	}
	var gateway string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "GATEWAY=") {
			gateway = strings.TrimPrefix(line, "GATEWAY=")
		}
	}
	client := http.Client{Timeout: time.Second}
	if res, err := client.Post(gateway, "application/json", strings.NewReader(`{}`)); err == nil {
		res.Body.Close()
		t.Fatal("credential relay survived the command")
	}
	filepath.WalkDir(workspace, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() != "agentflow-helper" {
			if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), "fake-cnb-sop-secret") {
				t.Errorf("credential persisted in %s", path)
			}
		}
		return err
	})
}

func testHTTPSCredentials(token string) map[string]domain.GitHTTPExecutionCredential {
	username, token := gitwork.SplitToken(token)
	result := map[string]domain.GitHTTPExecutionCredential{}
	for _, host := range []string{"github.com", "git.example.com"} {
		result[host] = domain.GitHTTPExecutionCredential{GitHTTPCredential: domain.GitHTTPCredential{Host: host, Username: username, Platform: domain.GitPlatformGitHub}, Token: token}
	}
	return result
}

func TestCodexSOPGitBindingKeepsHelperInToolShell(t *testing.T) {
	workspace := t.TempDir()
	sandbox := &localSandbox{workspace: workspace}
	const token = "fixture-sop-token"
	credentials := RunCredentials{GitHTTPCredentials: testHTTPSCredentials(token), GitSSHKeys: terminalSSHTestKeys(t)}
	credentials.GitSSHKeys["git@git.example.org:team/other.git"] = ""
	command := CommandSpec{
		Provider: "codex", Argv: withCodexIsolationEnv([]string{"codex", "exec", "prompt"}, map[string]string{
			"ZDOTDIR": "/fixture/zdot", SessionGatewayEnv: "stale-profile-gateway", GitHostsEnv: "stale-profile-hosts",
		}),
		CredentialBindings: []CredentialBinding{BindGit},
	}
	bind := func(cmd CommandSpec, creds RunCredentials) CommandSpec {
		t.Helper()
		bound, err := bindCommand(t.Context(), sandbox, workspace, cmd, creds)
		if err != nil {
			t.Fatal(err)
		}
		stop, err := bindCommandGitTransport(t.Context(), sandbox, workspace, &bound, creds)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(stop)
		return bound
	}
	sshOnly := bind(command, RunCredentials{GitSSHKeys: credentials.GitSSHKeys})
	if !slices.Equal(sshOnly.Argv, command.Argv) {
		t.Fatal("Codex policy changed without HTTPS credentials")
	}
	prepared, closeRelay, err := prepareRunGitBinding(t.Context(), sandbox, command, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRelay()
	credentials.GitHTTPHosts = runGitHosts(credentials)
	bound := bind(prepared, credentials)
	conversation := prepared
	conversation.CredentialBindings = []CredentialBinding{BindGitWorkspace}
	workspaceCommand := bind(conversation, credentials)
	if !slices.Equal(workspaceCommand.Argv, command.Argv) {
		t.Fatal("conversation Git binding changed Codex arguments")
	}
	var names []string
	for _, arg := range bound.Argv {
		if raw, ok := strings.CutPrefix(arg, "shell_environment_policy.include_only="); ok {
			if err := json.Unmarshal([]byte(raw), &names); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(arg, token) || strings.Contains(arg, "stale-profile-") || strings.Contains(arg, bound.environment[SessionGatewayEnv]) {
			t.Fatal("credential or relay URL in Codex arguments")
		}
	}
	if !slices.Contains(names, "ZDOTDIR") || !slices.Contains(names, "PATH") || !containsArgPair(bound.Argv, "-c", `shell_environment_policy.inherit="all"`) {
		t.Fatalf("missing Codex shell configuration: %v", names)
	}
	for _, key := range names {
		if strings.ContainsAny(key, "*?[]") || key == "CODEX_API_KEY" || key == "GITHUB_TOKEN" {
			t.Fatalf("unexpected inherited environment key %q", key)
		}
	}
	// Use only the names Codex will pass to Git, including the empty helper
	// reset. A pre-existing repository helper must not answer for another host.
	git := func(input string, args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
		for _, key := range names {
			if value, ok := bound.environment[key]; ok {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
		cmd.Stdin = strings.NewReader(input)
		return cmd.CombinedOutput()
	}
	if out, err := git("", "init", "-q"); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if out, err := git("", "status", "--short"); err != nil {
		t.Fatalf("git status: %v: %s", err, out)
	}
	url, err := git("", "ls-remote", "--get-url", "git@git.example.org:team/app.git")
	if err != nil || !strings.HasPrefix(string(url), "git@lf-") {
		t.Fatalf("missing SSH remote rewrite: %q, %v", url, err)
	}
	for _, key := range []string{"GIT_SSH_COMMAND", "GIT_SSH_VARIANT", "GIT_ALLOW_PROTOCOL"} {
		if !slices.Contains(names, key) {
			t.Fatalf("missing SSH binding %s", key)
		}
	}
	if _, err := git("", "config", "credential.helper", "!echo username=stale; echo password=stale"); err != nil {
		t.Fatal(err)
	}
	got, err := git("protocol=https\nhost=git.example.com\n\n", "credential", "fill")
	if err != nil || !strings.Contains(string(got), "password="+token) {
		t.Fatalf("configured host did not get its SOP credential: %v", err)
	}
	got, err = git("protocol=https\nhost=unconfigured.invalid\n\n", "credential", "fill")
	if err == nil || strings.Contains(string(got), "password=") {
		t.Fatal("unconfigured host obtained a Git credential")
	}
}

func TestCodexSOPGitIdentityPreservesSystemConfig(t *testing.T) {
	system := filepath.Join(t.TempDir(), "company defaults")
	if err := os.WriteFile(system, []byte("[http]\n\tsslCAInfo = /fixture/company.pem\n[user]\n\tname = Image User\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_SYSTEM", system)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "global"))

	workspace := t.TempDir()
	creds := RunCredentials{GitHTTPCredentials: testHTTPSCredentials("fixture"), GitUserName: "SOP User"}
	env, stop, err := RunGitEnvironment(t.Context(), nil, workspace, creds, "codex")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	t.Setenv("GIT_CONFIG_SYSTEM", env["GIT_CONFIG_SYSTEM"])
	testRuntimeGit(t, workspace, "init", "-q")
	if got := testRuntimeGit(t, workspace, "config", "--get", "http.sslCAInfo"); got != "/fixture/company.pem" {
		t.Fatalf("system CA = %q", got)
	}
	if got := testRuntimeGit(t, workspace, "config", "--get", "user.name"); got != "SOP User" {
		t.Fatalf("default identity = %q", got)
	}
	for _, scope := range []string{"--global", "--local"} {
		testRuntimeGit(t, workspace, "config", scope, "user.name", scope+" user")
		if got := testRuntimeGit(t, workspace, "config", "--get", "user.name"); got != scope+" user" {
			t.Fatalf("%s identity = %q", scope, got)
		}
	}
}

func TestCodexSOPGitPreservesNativeRunnerShellSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Base(home))
	const config = `[shell_environment_policy.set]
BUILD_PROFILE = "integration-fixture"
"CMAKE_FLAGS" = "-DOPTION=ON"
"BUILD_*" = "wildcard"
CODEX_API_KEY = "private-fixture"
[shell_environment_policy.Set]
NOT_EXPLICIT = "ignored-by-codex"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{SessionGatewayEnv: "fixture-relay", GitHostsEnv: "git.example.com=other"}
	for _, tc := range []struct {
		workspace string
		args      []string
		wantBuild bool
	}{
		{filepath.Dir(home), []string{"codex", "exec", "prompt"}, true},
		{"", []string{"codex", "exec", "prompt"}, false},
		{filepath.Dir(home), []string{"codex", "exec", "--ignore-user-config", "prompt"}, false},
	} {
		args := CodexRunGitArgv(tc.args, env, tc.workspace)
		var names []string
		for _, arg := range args {
			if raw, ok := strings.CutPrefix(arg, "shell_environment_policy.include_only="); ok {
				if err := json.Unmarshal([]byte(raw), &names); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(arg, "integration-fixture") || strings.Contains(arg, "private-fixture") {
				t.Fatal("Codex config value reached argv")
			}
		}
		for _, key := range []string{"BUILD_PROFILE", "CMAKE_FLAGS"} {
			if slices.Contains(names, key) != tc.wantBuild {
				t.Errorf("workspace=%q args=%v: unexpected inheritance of %s", tc.workspace, tc.args, key)
			}
		}
		if slices.Contains(names, "CODEX_API_KEY") || slices.Contains(names, "BUILD_*") || slices.Contains(names, "NOT_EXPLICIT") {
			t.Fatal("unexpected inherited secret or wildcard")
		}
	}
}
