package gitwork

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/sys/windows"
)

func windowsSSHFixtureKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return signer, string(pem.EncodeToMemory(block))
}

// Exercise the actual Git for Windows SSH executable against a local SSH
// server. No account, remote network, user SSH agent or private-key file is used.
func TestWindowsGitSSHNativeTransportLongPathsAndIsolation(t *testing.T) {
	if _, err := exec.LookPath("git.exe"); err != nil {
		t.Skip("Git for Windows is not installed")
	}
	if _, err := sshClientCommand(""); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace with spaces and ' quote", strings.Repeat("nested", 8))
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(t.TempDir(), "bare.git")
	if output, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("initialize fixture repository: %v %s", err, output)
	}
	firstSigner, firstKey := windowsSSHFixtureKey(t)
	secondSigner, secondKey := windowsSSHFixtureKey(t)
	firstRemote, firstPinned := windowsGitSSHServer(t, bare, firstSigner.PublicKey())
	secondRemote, secondPinned := windowsGitSSHServer(t, bare, secondSigner.PublicKey())
	pinned := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(pinned, []byte(firstPinned+secondPinned), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SSHKnownHostsEnv, pinned)
	// A hostile inherited agent must never take part in either operation.
	t.Setenv("SSH_AUTH_SOCK", "fixture-global-agent-must-not-be-used")
	t.Setenv("GIT_SSH_COMMAND", "fixture-global-ssh-must-not-be-used")
	firstEnvironment := map[string]string{}
	secondEnvironment := map[string]string{}
	stopFirst, err := StartSSH(firstEnvironment, map[string]string{firstRemote: firstKey}, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stopFirst()
	stopSecond, err := StartSSH(secondEnvironment, map[string]string{secondRemote: secondKey}, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stopSecond()
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 2 {
		t.Fatalf("operation roots: entries=%d err=%v", len(entries), err)
	}
	for _, entry := range entries {
		root := filepath.Join(workspace, entry.Name())
		if len(filepath.Join(root, "agent")) < 100 {
			t.Fatal("fixture did not exercise a long socket path")
		}
		windowsSSHAssertPrivateDirectory(t, root)
		files, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			content, err := os.ReadFile(filepath.Join(root, file.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(content, []byte("PRIVATE KEY")) || bytes.Contains(content, []byte(firstKey)) || bytes.Contains(content, []byte(secondKey)) {
				t.Fatalf("private SSH key persisted to %s", file.Name())
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, operation := range []struct {
		remote string
		env    map[string]string
	}{{firstRemote, firstEnvironment}, {secondRemote, secondEnvironment}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := command(ctx, workspace, "", operation.env, []string{"ls-remote", operation.remote}).CombinedOutput()
			if err != nil {
				t.Errorf("native Git SSH task: %v %s", err, out)
			}
		}()
	}
	wg.Wait()
	stopFirst()
	if out, err := command(ctx, workspace, "", secondEnvironment, []string{"ls-remote", secondRemote}).CombinedOutput(); err != nil {
		t.Fatalf("stopping one operation disrupted another: %v %s", err, out)
	}
	stopSecond()
	if entries, err := os.ReadDir(workspace); err != nil || len(entries) != 0 {
		t.Fatalf("Git SSH cleanup left session files: entries=%d err=%v", len(entries), err)
	}
	if os.Getenv("SSH_AUTH_SOCK") != "fixture-global-agent-must-not-be-used" || os.Getenv("GIT_SSH_COMMAND") != "fixture-global-ssh-must-not-be-used" {
		t.Fatal("Git SSH operation changed the account's agent configuration")
	}
}

func windowsSSHAssertPrivateDirectory(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("SSH directory must grant only its owner access: dacl=%v err=%v", dacl, err)
	}
}

func windowsGitSSHServer(t *testing.T, repository string, allowed ssh.PublicKey) (string, string) {
	t.Helper()
	host, _ := windowsSSHFixtureKey(t)
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(key.Marshal(), allowed.Marshal()) {
			return nil, fmt.Errorf("fixture received another operation's key")
		}
		return nil, nil
	}}
	config.AddHostKey(host)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(20 * time.Second))
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						incoming.Reject(ssh.UnknownChannelType, "session required")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					func() {
						defer channel.Close()
						for request := range requests {
							if request.Type != "exec" {
								request.Reply(false, nil)
								continue
							}
							var commandRequest struct{ Command string }
							if err := ssh.Unmarshal(request.Payload, &commandRequest); err != nil || commandRequest.Command != "git-upload-pack '/fixture.git'" {
								request.Reply(false, nil)
								return
							}
							request.Reply(true, nil)
							// An empty repository's real upload-pack advertisement is
							// sufficient for ls-remote and closes without more input.
							cmd := exec.Command("git", "upload-pack", "--advertise-refs", repository)
							cmd.Stdout, cmd.Stderr = channel, channel.Stderr()
							code := uint32(0)
							if err := cmd.Run(); err != nil {
								code = 1
							}
							channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
							return
						}
					}()
				}
			}()
		}
	}()
	address := listener.Addr().String()
	return "ssh://git@" + address + "/fixture.git", knownhosts.Normalize(address) + " " + string(ssh.MarshalAuthorizedKey(host.PublicKey()))
}

func TestWindowsGitSSHRejectsInvalidSocketCookie(t *testing.T) {
	listener, err := listenSSHAgent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() { _, err := listener.Accept(); accepted <- err }()
	conn, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 16)); err == nil {
		t.Fatal("agent accepted an unauthenticated loopback connection")
	}
	listener.Close()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("closing agent did not stop its accept loop")
	}
}
