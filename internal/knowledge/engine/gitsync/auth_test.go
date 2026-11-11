package gitsync

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agentflow/internal/knowledge"
)

func TestPrivateHTTPSCredentialSync(t *testing.T) {
	source := fixtureRepo(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// Record what Git children actually receive, including remote helpers.
	audit := filepath.Join(t.TempDir(), "processes")
	wrapperDir := t.TempDir()
	script := "#!/bin/sh\n/usr/bin/env >> " + shellQuote(audit) + "\nprintf '%s\\n' \"$@\" >> " + shellQuote(audit) + "\nexec " + shellQuote(gitPath) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapperDir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperDir+":"+os.Getenv("PATH"))
	t.Setenv("PRIVATE_SOURCE_TOKEN", "private-https-token-123")
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(source), "GIT_HTTP_EXPORT_ALL=1"}}
	token := "private-https-token-123"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "deploy" || password != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="Git"`)
			http.Error(w, "Authentication failed "+token, 401)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	remote := server.URL + "/" + filepath.Base(source)
	opts := []string{"protocol.allow=never", "protocol.https.allow=always", "http.followRedirects=false", "http.proxy=", "http.sslCAInfo=" + ca}
	cache := filepath.Join(t.TempDir(), "cache")
	cred := &knowledge.GitCredential{Kind: "https", Username: "deploy", Token: token}
	for i := 0; i < 2; i++ {
		_, head, files, _, _, err := syncAuthenticated(context.Background(), cache, remote, "main", nil, opts, cred, "")
		if err != nil || head == "" || len(files) != 2 {
			t.Fatalf("private HTTPS sync: %s %d %v", head, len(files), err)
		}
	}
	config, err := os.ReadFile(filepath.Join(cache, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), token) || strings.Contains(string(config), "deploy") {
		t.Fatal(".git/config 泄露凭据")
	}
	childData, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{token, "private-user", "PRIVATE_SOURCE_TOKEN", "AGENTFLOW_SESSION_SECRET"} {
		if strings.Contains(string(childData), secret) {
			t.Fatalf("child argv/environment leaks %s", secret)
		}
	}
	cred.Token = "wrong-token"
	_, _, _, _, _, err = syncAuthenticated(context.Background(), cache, remote, "main", nil, opts, cred, "")
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("应返回鉴权失败: %v", err)
	}
	for _, secret := range []string{token, cred.Token, "deploy"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("错误泄露凭据")
		}
	}
}

func TestPrivateSSHCredentialSync(t *testing.T) {
	source := fixtureRepo(t)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(block))
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	audit := filepath.Join(t.TempDir(), "ssh-processes")
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	wrapperDir := t.TempDir()
	script := "#!/bin/sh\n/usr/bin/env >> " + shellQuote(audit) + "\nprintf '%s\\n' \"$@\" >> " + shellQuote(audit) + "\nexec " + shellQuote(sshPath) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapperDir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperDir+":"+os.Getenv("PATH"))
	t.Setenv("SSH_PRIVATE_KEY", privateKey)
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, public ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(public.Marshal(), signer.PublicKey().Marshal()) {
			return nil, nil
		}
		return nil, errors.New("permission denied")
	}}
	config.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var connections sync.WaitGroup
	defer connections.Wait()
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer connections.Done()
				defer conn.Close()
				transport, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer transport.Close()
				go ssh.DiscardRequests(requests)
				for ch := range channels {
					if ch.ChannelType() != "session" {
						ch.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					channel, requests, err := ch.Accept()
					if err != nil {
						return
					}
					go func() {
						defer channel.Close()
						for req := range requests {
							if req.Type != "exec" {
								req.Reply(false, nil)
								continue
							}
							var command struct{ Command string }
							ssh.Unmarshal(req.Payload, &command)
							if !strings.HasPrefix(command.Command, "git-upload-pack ") {
								req.Reply(false, nil)
								return
							}
							req.Reply(true, nil)
							cmd := exec.Command("git-upload-pack", source)
							cmd.Stdin = channel
							cmd.Stdout = channel
							cmd.Stderr = channel.Stderr()
							status := uint32(0)
							if cmd.Run() != nil {
								status = 1
							}
							channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
							return
						}
					}()
				}
			}()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	remote := fmt.Sprintf("ssh://git@git.example.com:%d/repo.git", port)
	cred := &knowledge.GitCredential{Kind: "ssh", PrivateKey: privateKey, KnownHosts: "git.example.com " + string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))}
	options := []string{"protocol.allow=never", "protocol.ssh.allow=always"}
	cache := filepath.Join(t.TempDir(), "cache")
	for i := 0; i < 2; i++ {
		_, head, files, _, _, err := syncAuthenticated(context.Background(), cache, remote, "main", nil, options, cred, "127.0.0.1")
		if err != nil || head == "" || len(files) != 2 {
			t.Fatalf("private SSH sync: %s %d %v", head, len(files), err)
		}
	}
	data, err := os.ReadFile(filepath.Join(cache, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PRIVATE KEY") || strings.Contains(string(data), privateKey) {
		t.Fatal("SSH 私钥泄露至 .git/config")
	}
	childData, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(childData), privateKey) || strings.Contains(string(childData), "SSH_PRIVATE_KEY") {
		t.Fatal("SSH 子进程环境或命令行泄露私钥")
	}
	// Trusted host keys must be enforced before sending a deployment key.
	cred.KnownHosts = "git.example.com " + string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	_, _, _, _, _, err = syncAuthenticated(context.Background(), cache, remote, "main", nil, options, cred, "127.0.0.1")
	if err == nil || !strings.Contains(err.Error(), "host key verification failed") {
		t.Fatalf("host key mismatch: %v", err)
	}
}

func TestGitFailureCategoriesDiscardRemoteSecrets(t *testing.T) {
	for _, tc := range []struct{ stderr, want string }{
		{"fatal: Authentication failed secret-token", "authentication failed"},
		{"fatal: repository not found secret-token", "repository not found"},
		{"fatal: Remote branch absent not found in upstream origin secret-token", "remote branch missing"},
		{"fatal: secret-token", "Git operation failed"},
	} {
		if got := safeGitError(tc.stderr).Error(); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}
