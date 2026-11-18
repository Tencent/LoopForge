package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/gitwork"
)

const fakeCNBToken = "cnb-fake-token-0000"

func TestGitEnvironmentBindsManagedHostsWithoutGlobalToken(t *testing.T) {
	env, err := GitEnvironment(nil, t.TempDir(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if env["GIT_CONFIG_COUNT"] != "3" || env["GIT_ASKPASS"] != "" {
		t.Fatalf("a run without any token must still fail CNB closed and not install askpass: %v", env)
	}
	if !strings.Contains(env["GIT_CONFIG_VALUE_1"], "quit=1") || env["GIT_CONFIG_VALUE_0"] != "" {
		t.Fatalf("managed helper block missing: %v", env)
	}
}

func TestGitEnvironmentKeepsScopedTokenOutOfConfigAndGlobalToken(t *testing.T) {
	env, err := GitEnvironment(nil, t.TempDir(), "private: ghp_FAKEGLOBALPAT", []gitwork.HTTPSCredential{gitwork.CNBCredential(fakeCNBToken)})
	if err != nil {
		t.Fatal(err)
	}
	if env["AGENTFLOW_SCOPED_GIT_TOKEN_0"] != fakeCNBToken || env["AGENTFLOW_GIT_TOKEN"] != "ghp_FAKEGLOBALPAT" {
		t.Fatalf("tokens not bound: %v", env)
	}
	if env["GIT_CONFIG_COUNT"] != "7" || !strings.HasSuffix(env["GIT_CONFIG_KEY_0"], ".insteadof") {
		t.Fatalf("managed block must follow the Gongfeng rewrites: %v", env)
	}
	// Entries 4..6 are the managed block: neither token may appear in them.
	for i := 4; i < 7; i++ {
		for _, key := range []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"} {
			if v := env[key+strconv.Itoa(i)]; strings.Contains(v, fakeCNBToken) || strings.Contains(v, "ghp_FAKEGLOBALPAT") {
				t.Fatalf("token leaked into %s%d", key, i)
			}
		}
	}
}

func TestControlledBaseEnvironmentDropsScopedTokens(t *testing.T) {
	got := controlledBaseEnvironment([]string{"PATH=/bin", "AGENTFLOW_SCOPED_GIT_TOKEN_0=" + fakeCNBToken})
	if len(got) != 1 || got[0] != "PATH=/bin" {
		t.Fatalf("scoped token inherited by a child: %v", got)
	}
}

func TestGitAskPassRefusesManagedHost(t *testing.T) {
	run := func(prompt string) (string, error) {
		cmd := exec.Command("sh", "-c", gitAskPassScript, "askpass", prompt)
		cmd.Env = []string{"AGENTFLOW_GIT_TOKEN=ghp_FAKEGLOBALPAT", "AGENTFLOW_GIT_USER=private"}
		out, err := cmd.Output()
		return string(out), err
	}
	for _, prompt := range []string{"Username for 'https://cnb.cool': ", "Password for 'https://cnb@cnb.cool': "} {
		if out, err := run(prompt); err == nil || out != "" {
			t.Fatalf("askpass answered a CNB prompt %q: %q %v", prompt, out, err)
		}
	}
	if out, err := run("Password for 'https://git.example.com': "); err != nil || strings.TrimSpace(out) != "ghp_FAKEGLOBALPAT" {
		t.Fatalf("askpass must still serve other hosts: %q %v", out, err)
	}
}

func TestBindWorkspaceGitEnvironmentBindsCNBWithoutGitHubToken(t *testing.T) {
	env := map[string]string{}
	credentials := RunCredentials{GitHTTPS: []gitwork.HTTPSCredential{gitwork.CNBCredential(fakeCNBToken)}}
	if err := bindWorkspaceGitEnvironment(nil, t.TempDir(), env, credentials); err != nil {
		t.Fatal(err)
	}
	if env["AGENTFLOW_SCOPED_GIT_TOKEN_0"] != fakeCNBToken || env["GIT_CONFIG_COUNT"] != "3" || env["GIT_ASKPASS"] != "" {
		t.Fatalf("a CNB-only requester must get the managed block and no GitHub askpass: %v", env)
	}
}

func TestRunCredentialsKeepGitHTTPSOverTheControlConnection(t *testing.T) {
	raw, err := json.Marshal(RunCredentials{GitHTTPS: []gitwork.HTTPSCredential{gitwork.CNBCredential(fakeCNBToken)}})
	if err != nil {
		t.Fatal(err)
	}
	var got RunCredentials
	if err := json.Unmarshal(raw, &got); err != nil || len(got.GitHTTPS) != 1 || got.GitHTTPS[0].Token != fakeCNBToken {
		t.Fatalf("GitHTTPS lost in transit: %s %v", raw, err)
	}
}

// Under the conversation askpass the GitHub token is never offered to cnb.cool.
func TestBindGitWorkspaceOffersCNBOnlyItsOwnToken(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	fill := func(credentials RunCredentials) (string, error) {
		return ExecuteLocalCommand(context.Background(), t.TempDir(), CommandSpec{
			Argv:               []string{"sh", "-c", "printf 'protocol=https\nhost=cnb.cool\n\n' | git credential fill"},
			CredentialBindings: []CredentialBinding{BindGitWorkspace},
		}, credentials, nil)
	}
	out, err := fill(RunCredentials{GitToken: "ghp_FAKEGLOBALPAT", GitHTTPS: []gitwork.HTTPSCredential{gitwork.CNBCredential(fakeCNBToken)}})
	if err != nil || !strings.Contains(out, "password="+fakeCNBToken) || strings.Contains(out, "ghp_FAKEGLOBALPAT") {
		t.Fatalf("CNB must get only its own token: %q %v", out, err)
	}
	out, err = fill(RunCredentials{GitToken: "ghp_FAKEGLOBALPAT"})
	if err == nil || strings.Contains(out, "ghp_FAKEGLOBALPAT") || !strings.Contains(out, "told us to quit") {
		t.Fatalf("without a CNB token nothing may be offered to cnb.cool: %q", out)
	}
}
