package runtime

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agentflow/internal/gitwork"

	"golang.org/x/crypto/ssh"
)

func terminalSSHTestKeys(t *testing.T) map[string]string {
	t.Helper()
	_, identity, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(identity, "terminal test")
	if err != nil {
		t.Fatal(err)
	}
	hostPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewPublicKey(hostPublic)
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHosts, []byte("git.example.org "+string(ssh.MarshalAuthorizedKey(hostKey))), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(gitwork.SSHKnownHostsEnv, knownHosts)
	return map[string]string{"git@git.example.org:team/app.git": string(pem.EncodeToMemory(block))}
}

// The requester's agent must never be reachable through the shared workspace.
func TestTerminalGitSSHAgentStaysOutOfWorkspace(t *testing.T) {
	keys := terminalSSHTestKeys(t)
	for _, driver := range []string{"local", "bwrap"} {
		t.Run(driver, func(t *testing.T) {
			workspace := t.TempDir()
			var sb Sandbox = &localSandbox{workspace: workspace, environment: map[string]string{}}
			if driver == "bwrap" {
				sb = &processSandbox{workspace: workspace, driver: "bwrap"}
			}
			command := CommandSpec{environment: map[string]string{}}
			stop, err := BindTerminalGitSSH(sb, &command, keys)
			if err != nil {
				t.Fatal(err)
			}
			sockets, _ := filepath.Glob(filepath.Join("/tmp", "lf-term-ssh-*", "lf-ssh-*", "agent"))
			var session string
			for _, socket := range sockets {
				if strings.Contains(command.environment["GIT_SSH_COMMAND"], filepath.Dir(socket)) {
					session = filepath.Dir(filepath.Dir(socket))
				}
			}
			if session == "" {
				t.Fatalf("terminal agent socket not found for %q", command.environment["GIT_SSH_COMMAND"])
			}
			if info, err := os.Stat(session); err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("session directory mode: %v %v", info, err)
			}
			if strings.HasPrefix(session, workspace) || strings.Contains(command.environment["GIT_SSH_COMMAND"], workspace) {
				t.Fatalf("terminal agent lives in the workspace: %s", command.environment["GIT_SSH_COMMAND"])
			}
			var found []string
			_ = filepath.Walk(workspace, func(path string, info os.FileInfo, err error) error {
				if err == nil && info.Mode()&os.ModeSocket != 0 {
					found = append(found, path)
				}
				return nil
			})
			if len(found) != 0 {
				t.Fatalf("sockets in the workspace: %v", found)
			}
			wantBinds := []string(nil)
			if driver == "bwrap" {
				wantBinds = []string{session}
			}
			if !slices.Equal(command.privateBinds, wantBinds) {
				t.Fatalf("private binds = %v, want %v", command.privateBinds, wantBinds)
			}
			stop()
			if _, err := os.Stat(session); !os.IsNotExist(err) {
				t.Fatalf("session directory left behind: %v", err)
			}
		})
	}
}

// Only the terminal that owns the agent binds it back over its private /tmp;
// every other bwrap process of the workspace sees an empty tmpfs there.
func TestBwrapPrivateBindsReachOnlyTheirProcess(t *testing.T) {
	workspace := t.TempDir()
	base, err := bwrapArgsCommand(workspace, SandboxRequirements{}, false)
	if err != nil {
		t.Fatal(err)
	}
	owner := bwrapPrivateBinds(base, []string{"/tmp/lf-term-ssh-1"})
	if owner[len(owner)-1] != "--" || !slices.Equal(owner[len(owner)-4:len(owner)-1], []string{"--bind", "/tmp/lf-term-ssh-1", "/tmp/lf-term-ssh-1"}) {
		t.Fatalf("owner args: %q", owner)
	}
	if tmpfs, bind := slices.Index(owner, "/tmp"), slices.Index(owner, "/tmp/lf-term-ssh-1"); tmpfs < 0 || bind < tmpfs {
		t.Fatalf("private bind must follow --tmpfs /tmp: %q", owner)
	}
	other := bwrapPrivateBinds(base, nil)
	if slices.Contains(other, "/tmp/lf-term-ssh-1") {
		t.Fatalf("another process got the terminal's agent: %q", other)
	}
}

// Docker and seatbelt share one filesystem view among every process of the
// workspace, so the requester's key is not offered at all.
func TestTerminalGitSSHWithoutPrivateFilesystemOffersNoKey(t *testing.T) {
	keys := terminalSSHTestKeys(t)
	for _, sb := range []Sandbox{&dockerSandbox{workspace: t.TempDir()}, &processSandbox{workspace: t.TempDir(), driver: "seatbelt"}} {
		before, _ := filepath.Glob("/tmp/lf-term-ssh-*")
		command := CommandSpec{environment: map[string]string{}}
		stop, err := BindTerminalGitSSH(sb, &command, keys)
		if err != nil {
			t.Fatal(err)
		}
		stop()
		after, _ := filepath.Glob("/tmp/lf-term-ssh-*")
		if command.environment["GIT_SSH_COMMAND"] != terminalGitSSHUnavailable || len(command.privateBinds) != 0 || len(after) != len(before) {
			t.Fatalf("%s offered a key: env=%v binds=%v", sandboxDriver(sb), command.environment, command.privateBinds)
		}
		for key, value := range command.environment {
			if strings.HasPrefix(key, "GIT_CONFIG_") || strings.Contains(value, "BEGIN") {
				t.Fatalf("%s leaked SSH configuration %s=%s", sandboxDriver(sb), key, value)
			}
		}
	}
}
