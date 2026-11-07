package gitwork

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRemoteURL(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/Tencent/LoopForge.git",
		"http://git.example.com/a/b.git",
		"ssh://git@git.example.com:22/a/b.git",
		"git://git.example.com/a/b.git",
		"git@git.example.com:a/b.git",
		"git@[::1]:a/b.git",
	} {
		if err := ValidateRemoteURL(raw, false); err != nil {
			t.Errorf("ValidateRemoteURL(%q) = %v", raw, err)
		}
	}
	for _, raw := range []string{
		"",
		"ext::bash -c id>/tmp/pwn_rce",
		"ext::sh -c touch% /tmp/pwn",
		"fd::17",
		"EXT::bash",
		"-u touch /tmp/pwn_ok",
		"--upload-pack=touch /tmp/pwn",
		"ssh://-oProxyCommand=touch%20/tmp/pwn/a",
		"-oProxyCommand=id:a/b",
		"ftp://example.com/a.git",
		"https://git.example.com/a\n.git",
		"/srv/other-project",
		"file:///srv/other-project",
	} {
		if err := ValidateRemoteURL(raw, false); err == nil {
			t.Errorf("ValidateRemoteURL(%q) accepted", raw)
		}
	}
	for _, raw := range []string{"/srv/repo", "./repo", "file:///srv/repo"} {
		if err := ValidateRemoteURL(raw, true); err != nil {
			t.Errorf("local ValidateRemoteURL(%q) = %v", raw, err)
		}
	}
	if err := ValidateRemoteURL("ext::bash -c id", true); err == nil {
		t.Error("allowLocal must not enable remote helpers")
	}
}

func TestEnvPinsAllowedProtocols(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "ext")
	t.Setenv("GIT_PROTOCOL_FROM_USER", "1")
	env := strings.Join(Env(""), "\n")
	if strings.Contains(env, "GIT_ALLOW_PROTOCOL=ext") || strings.Contains(env, "GIT_PROTOCOL_FROM_USER=") {
		t.Fatalf("inherited protocol policy kept: %s", env)
	}
	if !strings.Contains(env, "GIT_ALLOW_PROTOCOL="+AllowedProtocols) {
		t.Fatal("GIT_ALLOW_PROTOCOL not pinned")
	}
}

func TestEnvBlocksExtTransportEvenWhenConfigAllowsIt(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	cmd := exec.Command("git", "-c", "protocol.ext.allow=always", "clone", "--quiet", "--", "ext::sh -c touch% "+marker, filepath.Join(dir, "ws"))
	cmd.Env = Env("")
	_ = cmd.Run()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("ext:: transport executed a command")
	}
}
