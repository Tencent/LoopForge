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

func TestValidateRemoteURLRejectsHostLocalDestinations(t *testing.T) {
	blocked := []string{
		"http://127.0.0.1:9/repo.git",
		"https://127.0.0.1/repo.git",
		"http://127.1/repo.git",
		"http://2130706433/repo.git",
		"http://0x7f.0.0.1/repo.git",
		"http://0177.0.0.1/repo.git",
		"http://localhost:3000/repo.git",
		"http://LOCALHOST./repo.git",
		"http://api.localhost/repo.git",
		"https://169.254.169.254/latest/meta-data",
		"http://[::1]:8080/repo.git",
		"http://[::ffff:127.0.0.1]/repo.git",
		"http://[fe80::1%25eth0]/repo.git",
		"http://0.0.0.0/repo.git",
		"http://[::]/repo.git",
		"http://224.0.0.1/repo.git",
		"ssh://git@127.0.0.1:22/a/b.git",
		"git://localhost/a/b.git",
		"git@127.0.0.1:a/b.git",
		"git@[::1]:a/b.git",
		"git@169.254.169.254:a/b.git",
	}
	for _, raw := range blocked {
		if err := ValidateRemoteURL(raw, false); err == nil {
			t.Errorf("ValidateRemoteURL(%q, false) accepted a host-local destination", raw)
		}
		if err := ValidateRemoteURL(raw, true); err != nil {
			t.Errorf("ValidateRemoteURL(%q, true) = %v; admins keep host-local remotes", raw, err)
		}
	}
	for _, raw := range []string{
		"https://10.0.0.5/team/repo.git",
		"http://192.168.1.20:3000/team/repo.git",
		"git@172.16.0.9:team/repo.git",
		"https://git.internal.example/team/repo.git",
		"https://localhost.example.com/team/repo.git",
		"https://1.2.3.4/team/repo.git",
		"https://0xdeadbeef.example/team/repo.git",
	} {
		if err := ValidateRemoteURL(raw, false); err != nil {
			t.Errorf("ValidateRemoteURL(%q, false) = %v", raw, err)
		}
	}
}
