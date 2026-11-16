package httpapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/runtime"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestSSHRepositoryCheckoutAndDelivery(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("OpenSSH client is not installed")
	}
	remote, privateKey, bare, knownHosts := localSSHGit(t, nil)
	f := cvBNewHTTPFixture(t)
	credentialURL := fmt.Sprintf("/api/users/%d/credentials", f.user.ID)
	keyURL := fmt.Sprintf("/api/users/%d/git-ssh-keys", f.user.ID)
	if resp := cvBRequest(t, f, http.MethodPut, keyURL, map[string]string{"host": remote, "name": "SSH host", "privateKey": "invalid"}, true); resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid SSH key: HTTP %d", resp.Code)
	}
	response := cvBRequest(t, f, http.MethodPut, keyURL, map[string]string{"host": remote, "name": "SSH host", "privateKey": privateKey}, true)
	var keys []domain.GitSSHKey
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &keys) != nil || len(keys) != 1 || strings.Contains(response.Body.String(), "PRIVATE KEY") {
		t.Fatalf("credential response must show metadata only (HTTP %d)", response.Code)
	}
	if user, _ := f.s.User(f.user.ID); !user.GitSSHKeyConfigured {
		t.Fatal("saved host key is missing its configured flag")
	}
	// An unrelated credential write must not overwrite the SSH key.
	if resp := cvBRequest(t, f, http.MethodPut, credentialURL, map[string]string{"codexBaseUrl": "https://example.org"}, true); resp.Code != http.StatusOK || f.s.GitSSHKeysForRemotes(f.user.ID, []string{remote})[remote] != privateKey {
		t.Fatalf("other credential update changed the SSH key (HTTP %d)", resp.Code)
	}

	input := map[string]string{"projectId": "platform", "repoUrl": remote, "webUrl": "https://forge.example.org/team/app"}
	t.Setenv(gitwork.SSHKnownHostsEnv, "")
	if resp := cvBRequest(t, f, http.MethodDelete, keyURL+"/"+keys[0].ID, nil, true); resp.Code != http.StatusOK {
		t.Fatalf("remove personal key before saving repository: HTTP %d", resp.Code)
	}
	resp := cvBRequest(t, f, http.MethodPost, "/api/repositories", input, true)
	var repository domain.Repository
	if err := json.Unmarshal(resp.Body.Bytes(), &repository); err != nil || resp.Code != http.StatusOK || repository.Provider != domain.RepositoryProviderSSH || repository.CloneURL != remote || repository.GongfengProjectID != "" {
		t.Fatalf("save repository: HTTP %d, %s", resp.Code, resp.Body.String())
	}
	// The missing-key warning is advisory; saving repository configuration does
	// not require a personal key. Add it later before attempting Git operations.
	if resp := cvBRequest(t, f, http.MethodPut, keyURL, map[string]string{"host": remote, "name": "SSH host", "privateKey": privateKey}, true); resp.Code != http.StatusOK {
		t.Fatalf("add personal key after saving repository: HTTP %d", resp.Code)
	}
	gitCtx := gitwork.WithSSHRemote(gitwork.WithSSHKeys(context.Background(), map[string]string{remote: privateKey}), remote)
	if _, err := gitwork.Run(gitCtx, t.TempDir(), "ls-remote", "--", remote); err == nil || !strings.Contains(err.Error(), gitwork.SSHKnownHostsEnv) {
		t.Fatalf("Git execution must still require host pinning: %v", err)
	}
	t.Setenv(gitwork.SSHKnownHostsEnv, knownHosts)
	rc := domain.RepositoryContext{RepositoryID: repository.ID, Name: repository.Name, Provider: repository.Provider, CloneURL: remote, WebURL: repository.WebURL, BaseBranch: "main", DefaultBranch: "main", WorkBranch: "feature/ssh-test", CreateNewWorkBranch: true, WorkspaceDir: "app"}
	run := domain.Run{
		ID: "run-git-ssh", Kind: domain.RunKindWorkspace, Status: "waiting",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID, SandboxProfileID: "local"},
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform", ResponsibleUserID: f.user.ID},
		Repositories:     []domain.RepositoryContext{rc}, Nodes: map[string]*domain.NodeRun{},
	}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	// This integration spans multiple Git processes; allow slower CI hosts.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	correctPins, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ssh.ParsePrivateKey([]byte(privateKey))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := gitwork.ParseSSHRemote(remote)
	if err != nil {
		t.Fatal(err)
	}
	wrongPin := knownhosts.Line([]string{net.JoinHostPort(endpoint.Host, endpoint.Port)}, identity.PublicKey()) + "\n"
	if err := os.WriteFile(knownHosts, []byte(wrongPin), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCtx = gitwork.WithSSHRemote(gitwork.WithSSHKeys(ctx, map[string]string{remote: privateKey}), remote)
	if _, err := gitwork.Run(gitCtx, t.TempDir(), "ls-remote", "--", remote); err == nil || !strings.Contains(err.Error(), "Host key verification failed") {
		t.Fatalf("an unexpected host key was not rejected: %v", err)
	}
	if err := os.WriteFile(knownHosts, correctPins, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.e.MaterializeConversationWorkspace(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	root, reason := f.e.LocalRunWorkspace(run)
	if reason != "" {
		t.Fatal(reason)
	}
	checkout := filepath.Join(root, "app")
	if origin := sshTestGit(t, checkout, "remote", "get-url", "origin"); origin != remote {
		t.Fatalf("origin = %q", origin)
	}
	if got, err := runtime.ExecuteLocalCommand(ctx, root, runtime.CommandSpec{
		Argv: []string{"git", "-C", "app", "fetch", "origin", "main"}, CredentialBindings: []runtime.CredentialBinding{runtime.BindGit},
	}, runtime.RunCredentials{GitSSHKeys: map[string]string{remote: privateKey}}, nil); err != nil {
		t.Fatalf("sandbox Git fetch: %v (%s)", err, got)
	}
	if entries, err := filepath.Glob(filepath.Join(root, ".agentflow", "runtime", "lf-ssh-*")); err != nil || len(entries) != 0 {
		t.Fatalf("SSH command left a signing agent behind: %v %v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("ssh delivery\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &agentToolGateway{deps: &deps{repo: f.s, engine: f.e}}
	session := &AgentToolSession{RunID: run.ID, ProjectID: "platform", UserID: f.user.ID, User: f.user, Mode: AgentToolAssistant}
	raw, err := h.workspaceSubmitMR(ctx, session, map[string]any{"title": "SSH delivery"})
	if err != nil {
		t.Fatal(err)
	}
	var output struct{ Results []workspaceMRResult }
	if err := json.Unmarshal([]byte(raw), &output); err != nil || len(output.Results) != 1 {
		t.Fatalf("invalid delivery result: %s", raw)
	}
	result := output.Results[0]
	if !result.Pushed || !result.PushOnly || result.MRURL != "" || result.RepositoryURL != repository.WebURL || result.Commit != sshTestGit(t, bare, "rev-parse", "refs/heads/feature/ssh-test") {
		t.Fatalf("SSH delivery = %+v", result)
	}
	if resp := cvBRequest(t, f, http.MethodDelete, keyURL+"/"+keys[0].ID, nil, true); resp.Code != http.StatusOK || f.s.GitSSHKeysForRemotes(f.user.ID, []string{remote})[remote] != "" {
		t.Fatalf("remove key: HTTP %d", resp.Code)
	}
	if _, err := h.workspaceSubmitMR(ctx, session, map[string]any{"title": "No key"}); err == nil || !strings.Contains(err.Error(), "未配置 Git SSH 密钥") {
		t.Fatalf("missing SSH key must explain the fix: %v", err)
	}
}

func TestGitSSHKeysMatchHostAndPort(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("OpenSSH client is not installed")
	}
	const otherPath = "/team/other.git"
	app, firstKey, _, knownHosts := localSSHGit(t, map[string]ssh.PublicKey{otherPath: nil})
	second, secondKey, _, secondPins := localSSHGit(t, nil)
	pins, _ := os.ReadFile(knownHosts)
	extraPins, _ := os.ReadFile(secondPins)
	if err := os.WriteFile(knownHosts, append(pins, extraPins...), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(gitwork.SSHKnownHostsEnv, knownHosts)
	other := strings.TrimSuffix(app, "/team/app.git") + otherPath
	firstHost, _ := gitwork.SSHHostScope(app)
	secondHost, _ := gitwork.SSHHostScope(second)
	f := cvBNewHTTPFixture(t)
	endpoint := fmt.Sprintf("/api/users/%d/git-ssh-keys", f.user.ID)
	if resp := cvBRequest(t, f, http.MethodGet, "/api/users/999/git-ssh-keys", nil, true); resp.Code != http.StatusForbidden {
		t.Fatalf("another account's credentials returned HTTP %d", resp.Code)
	}
	putKey := func(input map[string]string) []domain.GitSSHKey {
		t.Helper()
		resp := cvBRequest(t, f, http.MethodPut, endpoint, input, true)
		var keys []domain.GitSSHKey
		if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &keys) != nil || strings.Contains(resp.Body.String(), "PRIVATE KEY") || strings.Contains(resp.Body.String(), `"scope"`) {
			t.Fatalf("save SSH host key metadata: HTTP %d", resp.Code)
		}
		return keys
	}
	keys := putKey(map[string]string{"host": app, "name": "First host", "privateKey": firstKey})
	firstID := keys[0].ID
	keys = putKey(map[string]string{"host": second, "name": "Second host", "privateKey": secondKey})
	if len(keys) != 2 || keys[0].Host != firstHost || keys[1].Host != secondHost {
		t.Fatalf("saved host metadata = %+v", keys)
	}
	duplicate := map[string]string{"host": other, "name": "Duplicate", "privateKey": secondKey}
	if resp := cvBRequest(t, f, http.MethodPut, endpoint, duplicate, true); resp.Code != http.StatusConflict {
		t.Fatalf("a second key for the same host returned HTTP %d", resp.Code)
	}
	if resp := cvBRequest(t, f, http.MethodPut, endpoint, map[string]string{"scope": "default", "privateKey": secondKey}, true); resp.Code != http.StatusBadRequest {
		t.Fatalf("an unbound default key returned HTTP %d", resp.Code)
	}
	keys = putKey(map[string]string{"id": firstID, "host": firstHost, "name": "First host renamed"})
	if keys[0].Name != "First host renamed" {
		t.Fatalf("renamed key metadata = %+v", keys)
	}
	const unrelated = "git@unconfigured.example.org:team/app.git"
	selected := f.s.GitSSHKeysForRemotes(f.user.ID, []string{app, other, second, unrelated})
	if selected[app] != firstKey || selected[other] != firstKey || selected[second] != secondKey || selected[unrelated] != "" {
		t.Fatal("keys must match the host and port, with no fallback for unconfigured hosts")
	}
	if f.s.GitSSHKeysForRemotes(f.user.ID+1, []string{app})[app] != "" {
		t.Fatal("a different user borrowed the host key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := runtime.ExecuteLocalCommand(ctx, t.TempDir(), runtime.CommandSpec{
		Argv:               []string{"sh", "-c", `git ls-remote --exit-code -- "$1" refs/heads/main && git ls-remote --exit-code -- "$2" refs/heads/main && git ls-remote --exit-code -- "$3" refs/heads/main`, "git-ssh-test", app, other, second},
		CredentialBindings: []runtime.CredentialBinding{runtime.BindGit},
	}, runtime.RunCredentials{GitSSHKeys: map[string]string{app: selected[app], other: selected[other], second: selected[second]}}, nil)
	if err != nil || strings.Count(out, "refs/heads/main") != 3 {
		t.Fatalf("one sandbox must authenticate to both hosts and share the first host key: %v (%s)", err, out)
	}
	if resp := cvBRequest(t, f, http.MethodDelete, endpoint+"/"+firstID, nil, true); resp.Code != http.StatusOK {
		t.Fatalf("delete host key: HTTP %d", resp.Code)
	}
	selected = f.s.GitSSHKeysForRemotes(f.user.ID, []string{app, other, second})
	if selected[app] != "" || selected[other] != "" || selected[second] != secondKey {
		t.Fatal("deleting a host key must affect that host's repositories only")
	}
}

func TestGitSSHRunnerClaimUsesTaskActorCredential(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	actor := domain.User{LoginName: "ssh-delivery-actor"}
	if err := f.s.PutUser(actor); err != nil {
		t.Fatal(err)
	}
	actor, _ = f.s.UserByLoginName(actor.LoginName)
	const key = "transient-other-host-identity"
	const gitlabKey = "transient-gitlab-host-identity"
	const gitlabRepo = "git@gitlab.example.org:team/app.git"
	const otherHostRepo = "git@git.example.org:team/app.git"
	if err := f.s.SaveGitSSHKey(actor.ID, "", otherHostRepo, "Other Git host", key); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SaveGitSSHKey(actor.ID, "", gitlabRepo, "GitLab", gitlabKey); err != nil {
		t.Fatal(err)
	}
	runnerID, session := registerTestRunner(t, f.h)
	registered, _ := f.s.Runner(runnerID)
	now := time.Now().UTC()
	if err := f.s.RunnerHeartbeat(runnerID, registered.Runtimes, domain.RunnerVersionGitSSH, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	task := domain.RunnerTask{ID: "ssh-task", RunnerID: runnerID, Provider: "mock", Status: domain.RunnerTaskQueued, MaxAttempts: 1, Invocation: &domain.RunnerInvocation{
		GitSSHUserID: actor.ID,
		Repositories: []domain.RunnerRepository{{CloneURL: gitlabRepo, UseSSH: true}, {CloneURL: otherHostRepo, UseSSH: true}},
	}}
	if err := f.s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	resp := requestWithHeaders(t, f.h, http.MethodPost, "/api/runners/tasks/claim", map[string]any{"availableRuntimes": []string{"mock"}, "noWait": true}, map[string]string{"Authorization": "Bearer " + session})
	var result struct{ Task domain.RunnerTask }
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil || resp.Code != http.StatusOK || result.Task.ID != task.ID || result.Task.Invocation == nil || result.Task.Invocation.GitSSHKeys[gitlabRepo] != gitlabKey || result.Task.Invocation.GitSSHKeys[otherHostRepo] != key || result.Task.Invocation.GitSSHUserID != 0 {
		t.Fatalf("authenticated claim did not hydrate the task actor's keys (HTTP %d)", resp.Code)
	}
	stored, _ := f.s.RunnerTask(task.ID)
	if stored.Invocation == nil || len(stored.Invocation.GitSSHKeys) != 0 {
		t.Fatal("claimed key was persisted")
	}
	if resp := requestWithHeaders(t, f.h, http.MethodGet, "/api/runners/tasks/"+task.ID, nil, map[string]string{"Authorization": "Bearer " + session}); resp.Code != http.StatusOK || strings.Contains(resp.Body.String(), "gitSsh") || strings.Contains(resp.Body.String(), key) {
		t.Fatalf("runner status response must omit credential fields (HTTP %d)", resp.Code)
	}
}

func sshTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func sshTestIdentity(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	public, identity, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(identity, "loopforge integration test")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), key
}

// A loopback SSH server runs Git on one disposable bare repository, with a
// separate public-key permission for each SSH repository path.
func localSSHGit(t *testing.T, extraKeys map[string]ssh.PublicKey) (remote, privateKey, bare, knownHosts string) {
	t.Helper()
	root := t.TempDir()
	bare = filepath.Join(root, "app.git")
	sshTestGit(t, root, "init", "-b", "main")
	sshTestGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.org", "commit", "--allow-empty", "-m", "base")
	sshTestGit(t, root, "clone", "--bare", ".", bare)
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, user := sshTestIdentity(t)
	expected := map[string]string{"/team/app.git": ssh.FingerprintSHA256(user)}
	for path, key := range extraKeys {
		if key == nil {
			key = user
		}
		expected[path] = ssh.FingerprintSHA256(key)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	config := &ssh.ServerConfig{PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if conn.User() == "git" {
			fingerprint := ssh.FingerprintSHA256(key)
			for _, allowed := range expected {
				if fingerprint == allowed {
					return &ssh.Permissions{Extensions: map[string]string{"key": fingerprint}}, nil
				}
			}
		}
		return nil, fmt.Errorf("unknown key")
	}}
	config.AddHostKey(host)
	knownHosts = filepath.Join(root, "known_hosts")
	if err := os.WriteFile(knownHosts, []byte(knownhosts.Line([]string{listener.Addr().String()}, host.PublicKey())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	go func(repo string) {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSSHGit(conn, config, repo, expected)
		}
	}(bare)
	return "ssh://git@" + listener.Addr().String() + "/team/app.git", privateKey, bare, knownHosts
}

func serveSSHGit(raw net.Conn, config *ssh.ServerConfig, bare string, expected map[string]string) {
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(30 * time.Second))
	conn, sessions, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(requests)
	for session := range sessions {
		if session.ChannelType() != "session" {
			_ = session.Reject(ssh.UnknownChannelType, "session required")
			continue
		}
		channel, commands, err := session.Accept()
		if err != nil {
			return
		}
		defer channel.Close()
		for request := range commands {
			var command struct{ Value string }
			if request.Type != "exec" || ssh.Unmarshal(request.Payload, &command) != nil {
				_ = request.Reply(false, nil)
				continue
			}
			program, path, _ := strings.Cut(command.Value, " ")
			if expected[strings.Trim(path, "'")] != conn.Permissions.Extensions["key"] || (program != "git-upload-pack" && program != "git-receive-pack") {
				_ = request.Reply(false, nil)
				return
			}
			_ = request.Reply(true, nil)
			cmd := exec.Command(program, bare)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = channel, channel, channel.Stderr()
			var exitCode uint32
			if err := cmd.Run(); err != nil {
				exitCode = 1
			}
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{exitCode}))
			return
		}
	}
}

// GitHub SSH-style addresses keep the GitHub provider (HTTPS + token and
// automatic pull requests); only an explicit provider "ssh" switches transport.
func TestGitHubSSHAddressStaysGitHubRepository(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, address := range []string{"git@github.com:octo/widgets.git", "ssh://git@github.com/octo/widgets.git", "git clone git@github.com:octo/widgets.git"} {
		resp := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]string{"projectId": "platform", "repoUrl": address}, true)
		var repository domain.Repository
		if err := json.Unmarshal(resp.Body.Bytes(), &repository); err != nil || resp.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d, %s", address, resp.Code, resp.Body.String())
		}
		if repository.Provider != "github" || repository.GongfengProjectID != "github:octo/widgets" || repository.CloneURL != "https://github.com/octo/widgets.git" {
			t.Fatalf("%s saved as provider=%q id=%q clone=%q", address, repository.Provider, repository.GongfengProjectID, repository.CloneURL)
		}
	}
	resp := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]string{"projectId": "platform", "provider": domain.RepositoryProviderSSH, "repoUrl": "git@github.com:octo/gadgets.git"}, true)
	var explicit domain.Repository
	if err := json.Unmarshal(resp.Body.Bytes(), &explicit); err != nil || resp.Code != http.StatusOK || explicit.Provider != domain.RepositoryProviderSSH || explicit.CloneURL != "git@github.com:octo/gadgets.git" {
		t.Fatalf("explicit SSH provider: HTTP %d, %s", resp.Code, resp.Body.String())
	}
}
