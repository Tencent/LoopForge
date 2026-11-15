package gitwork

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func sshFactoryFixtureKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "factory-fixture")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), signer.PublicKey()
}

func TestSSHAgentFactoryReceivesOnlySelectedUniqueIdentities(t *testing.T) {
	first, host := sshFactoryFixtureKey(t)
	second, _ := sshFactoryFixtureKey(t)
	pinned := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(pinned, append([]byte("git.example.test "), ssh.MarshalAuthorizedKey(host)...), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SSHKnownHostsEnv, pinned)
	root := t.TempDir()
	environment := map[string]string{}
	calls, cleanups := 0, 0
	directory := ""
	stop, err := StartSSHWithAgentFactory(environment, map[string]string{
		"git@git.example.test:a.git": first, "git@git.example.test:b.git": first, "git@git.example.test:c.git": second,
	}, root, "/workspace/runtime", func(dir string, identities []string) (string, func(), error) {
		calls++
		directory = dir
		if len(identities) != 2 || identities[0] != first || identities[1] != second {
			t.Fatal("factory identity selection/deduplication changed")
		}
		return "/tmp/selected-operation/agent", func() { cleanups++ }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if calls != 1 || cleanups != 0 {
		t.Fatal("factory lifecycle changed")
	}
	config, err := os.ReadFile(filepath.Join(directory, "config"))
	if err != nil || !strings.Contains(string(config), `IdentityAgent "/tmp/selected-operation/agent"`) {
		t.Fatal("custom agent socket was not installed")
	}
	if !strings.Contains(string(config), "StrictHostKeyChecking yes") || !strings.Contains(string(config), "IdentitiesOnly yes") {
		t.Fatal("custom agent removed host/identity restrictions")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "PRIVATE KEY") || strings.Contains(string(data), first) || strings.Contains(string(data), second) {
			t.Fatal("private identity reached the filesystem")
		}
	}
	for _, value := range environment {
		if strings.Contains(value, first) || strings.Contains(value, second) {
			t.Fatal("private identity reached the environment")
		}
	}
	stop()
	stop()
	if cleanups != 1 {
		t.Fatal("factory cleanup is not idempotent")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("public session directory was retained")
	}
}

func TestSSHAgentFactoryFailureCleansItsPartialSession(t *testing.T) {
	identity, host := sshFactoryFixtureKey(t)
	pinned := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(pinned, append([]byte("git.example.test "), ssh.MarshalAuthorizedKey(host)...), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SSHKnownHostsEnv, pinned)
	root := t.TempDir()
	cleaned := false
	_, err := StartSSHWithAgentFactory(map[string]string{}, map[string]string{"git@git.example.test:a.git": identity}, root, "/workspace/runtime",
		func(string, []string) (string, func(), error) {
			return "", func() { cleaned = true }, errors.New("fixture startup failure")
		})
	if err == nil || !cleaned {
		t.Fatal("partial factory startup was not cleaned")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("partial public session directory was retained")
	}
}
