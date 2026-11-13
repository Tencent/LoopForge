package gitwork

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestBindSSHRemotesRewritesSortedURLsAfterExistingConfig(t *testing.T) {
	keyed := "ssh://git@git.example.org:2222/group/b.git"
	plain := "git@gitlab.com:group/a.git"
	env := map[string]string{"GIT_CONFIG_COUNT": "1"}
	bindings, remotes, err := bindSSHRemotes(env, map[string]string{keyed: "KEY", plain: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || len(remotes) != 1 {
		t.Fatalf("bindings=%d remotes=%d, want 1/1", len(bindings), len(remotes))
	}
	remote, err := ParseSSHRemote(keyed)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(remote.URL))
	alias := fmt.Sprintf("lf-%x", sum[:16])
	if got := bindings[0]; got.alias != alias || got.key != "KEY" || got.remote != remote || remotes[0] != remote {
		t.Fatalf("binding = %+v, want alias %s for %+v", got, alias, remote)
	}
	want := map[string]string{
		"GIT_CONFIG_COUNT":   "3",
		"GIT_CONFIG_KEY_1":   "url." + plain + ".insteadOf",
		"GIT_CONFIG_VALUE_1": plain,
		"GIT_CONFIG_KEY_2":   "url." + remote.aliasURL(alias) + ".insteadOf",
		"GIT_CONFIG_VALUE_2": keyed,
	}
	if len(env) != len(want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
	for k, v := range want {
		if env[k] != v {
			t.Fatalf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
}

func TestBindSSHRemotesRejectsInvalidRemote(t *testing.T) {
	env := map[string]string{}
	if _, _, err := bindSSHRemotes(env, map[string]string{"https://example.com/a.git": "KEY"}); err == nil {
		t.Fatal("expected invalid SSH remote error")
	}
}

func TestStartSSHWithoutAssignedKeyOnlyRewritesURLs(t *testing.T) {
	raw := "git@gitlab.com:group/a.git"
	env := map[string]string{}
	stop, err := StartSSH(env, map[string]string{raw: ""}, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if env["GIT_CONFIG_COUNT"] != "1" || env["GIT_CONFIG_VALUE_0"] != raw || env["GIT_SSH_VARIANT"] != "ssh" {
		t.Fatalf("env = %v", env)
	}
	if env["GIT_SSH_COMMAND"] == "" {
		t.Fatal("expected failing GIT_SSH_COMMAND without assigned key")
	}
}
