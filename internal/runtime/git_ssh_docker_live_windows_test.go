package runtime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/processgroup"
	"golang.org/x/crypto/ssh"
)

func liveDockerSSHIdentity(t *testing.T) (string, []byte) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "local-Docker-acceptance")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), ssh.MarshalAuthorizedKey(signer.PublicKey())
}

func TestWindowsLiveDockerPersonalGitSSHIsolationAndCleanup(t *testing.T) {
	if os.Getenv("LOOPFORGE_WINDOWS_DOCKER_LIVE") != "1" {
		t.Skip("requires the actual local Linux Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	image := os.Getenv("AGENTFLOW_ISOLATED_IMAGE")
	if image == "" {
		t.Fatal("AGENTFLOW_ISOLATED_IMAGE must name the prepared acceptance image")
	}
	run := func(input string, arguments ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, "docker", arguments...)
		command.Stdin = strings.NewReader(input)
		return command.CombinedOutput()
	}
	ownerIdentity := func(container, directory string) []string {
		t.Helper()
		output, err := run("", "exec", container, "cat", directory+"/owner")
		identity := strings.Fields(string(output))
		if err != nil || len(identity) != 2 || !regexp.MustCompile(`^[1-9][0-9]* [0-9]+$`).MatchString(strings.Join(identity, " ")) {
			t.Fatal("could not capture exact SSH agent PID/start identity")
		}
		return identity
	}
	assertAgentGone := func(container, directory string, identity []string) {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		for {
			probe := dockerSSHProcessIdentity + `test ! -e "$1"; test "$(process_start "$2" 2>/dev/null || :)" != "$3"`
			_, err := run("", "exec", container, "sh", "-ec", probe, "verify-agent-revoked", directory, identity[0], identity[1])
			if err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("cleanup retained the exact private-key agent process or its socket directory")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	stamp := fmt.Sprintf("%d", time.Now().UnixNano())
	network, server := "loopforge-ssh-qa-"+stamp, "loopforge-sshd-qa-"+stamp
	if output, err := run("", "network", "create", network); err != nil {
		t.Fatalf("create SSH network: %s: %v", output, err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", network).Run() })
	t.Setenv("AGENTFLOW_DOCKER_NETWORK", network)
	startServer := `set -eu; command -v /usr/sbin/sshd >/dev/null; mkdir -p /run/sshd /root/.ssh; chmod 700 /root/.ssh; passwd -d root >/dev/null; ssh-keygen -A >/dev/null; git init --bare /fixture.git >/dev/null; exec /usr/sbin/sshd -D -e -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o UsePAM=no -o PermitRootLogin=prohibit-password`
	if output, err := run("", "run", "-d", "--name", server, "--network", network, "--network-alias", "ssh-fixture", "--entrypoint", "sh", image, "-c", startServer); err != nil {
		t.Fatalf("start local SSH server (fixture image needs openssh-server): %s: %v", output, err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", server).Run() })
	firstKey, firstPublic := liveDockerSSHIdentity(t)
	secondKey, secondPublic := liveDockerSSHIdentity(t)
	if output, err := run(string(firstPublic), "exec", "-i", server, "sh", "-c", "umask 077; mkdir -p /root/.ssh; cat > /root/.ssh/authorized_keys"); err != nil {
		t.Fatalf("install public SSH authorization: %s: %v", output, err)
	}
	var hostPublic []byte
	deadline := time.Now().Add(10 * time.Second)
	for {
		var err error
		hostPublic, err = run("", "exec", server, "cat", "/etc/ssh/ssh_host_ed25519_key.pub")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("SSH host key not ready: %s: %v", hostPublic, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	pinned := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(pinned, append([]byte("ssh-fixture "), hostPublic...), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(gitwork.SSHKnownHostsEnv, pinned)
	t.Setenv("SSH_AUTH_SOCK", "unrelated-host-agent-must-not-be-used")
	workspace := filepath.Join(t.TempDir(), "SSH workspace with spaces ' quote")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	sandbox := &dockerSandbox{name: "agentflow-ssh-client-qa-" + stamp, workspace: workspace, profile: domain.SandboxProfile{Driver: "docker"}, dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	t.Cleanup(func() { _ = sandbox.Close(context.Background()) })
	const remote = "ssh://root@ssh-fixture/fixture.git"
	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	firstEnvironment := map[string]string{}
	stopFirst, err := BindGitSSHContext(firstCtx, sandbox, workspace, firstEnvironment, map[string]string{remote: firstKey})
	if err != nil {
		t.Fatal(err)
	}
	defer stopFirst()
	sockets := func() map[string]bool {
		found := map[string]bool{}
		entries, err := os.ReadDir(filepath.Join(workspace, ".agentflow", "runtime"))
		if err != nil {
			t.Fatal(err)
		}
		pattern := regexp.MustCompile(`(?m)^  IdentityAgent "([^"]+)"$`)
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "lf-ssh-") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(workspace, ".agentflow", "runtime", entry.Name(), "config"))
			if err != nil {
				t.Fatal(err)
			}
			match := pattern.FindSubmatch(data)
			if len(match) != 2 {
				t.Fatal("container SSH session did not receive an isolated agent socket")
			}
			found[string(match[1])] = true
		}
		return found
	}
	firstSockets := sockets()
	if len(firstSockets) != 1 {
		t.Fatal("first binding created an unexpected agent count")
	}
	var firstSocket string
	for value := range firstSockets {
		firstSocket = value
	}
	secondEnvironment := map[string]string{}
	stopSecond, err := BindGitSSHContext(ctx, sandbox, workspace, secondEnvironment, map[string]string{remote: secondKey})
	if err != nil {
		t.Fatal(err)
	}
	defer stopSecond()
	var secondSocket string
	for value := range sockets() {
		if !firstSockets[value] {
			secondSocket = value
		}
	}
	if secondSocket == "" || secondSocket == firstSocket {
		t.Fatal("concurrent commands shared one SSH agent")
	}
	for _, session := range []struct {
		socket string
		public []byte
	}{{firstSocket, firstPublic}, {secondSocket, secondPublic}} {
		listed, err := run("", "exec", "-e", "SSH_AUTH_SOCK="+session.socket, sandbox.name, "ssh-add", "-L")
		fields := bytes.Fields(listed)
		if err != nil || len(fields) < 2 || !bytes.Equal(fields[1], bytes.Fields(session.public)[1]) || len(bytes.Split(bytes.TrimSpace(listed), []byte("\n"))) != 1 {
			t.Fatal("agent contains another command's identity")
		}
	}
	gitCommand := func(environment map[string]string, argv ...string) (string, error) {
		return sandbox.Exec(ctx, WithCommandEnvironment(CommandSpec{Argv: argv}, environment), nil)
	}
	if output, err := gitCommand(firstEnvironment, "git", "ls-remote", remote); err != nil {
		t.Fatalf("selected key could not ls-remote through real SSH: %s: %v", output, err)
	}
	if _, err := gitCommand(secondEnvironment, "git", "ls-remote", remote); err == nil {
		t.Fatal("a concurrent command authenticated with the other command's key")
	}
	push := `set -eu; git init client >/dev/null; git -C client config user.name 'Docker SSH fixture'; git -C client config user.email fixture@example.invalid; printf selected-key-push > client/receipt.txt; git -C client add receipt.txt; git -C client commit -m fixture >/dev/null; git -C client push "$1" HEAD:refs/heads/main`
	if output, err := gitCommand(firstEnvironment, "sh", "-ec", push, "fixture", remote); err != nil {
		t.Fatalf("selected key could not push through real SSH: %s: %v", output, err)
	}
	if output, err := run("", "exec", server, "git", "--git-dir=/fixture.git", "show", "main:receipt.txt"); err != nil || string(output) != "selected-key-push" {
		t.Fatalf("SSH push did not change the real bare repository: %s: %v", output, err)
	}
	// TerminateProcess skips all Runner defers. The container must revoke this
	// owner's agent even though the shared workspace container remains alive.
	beforeCrash := sockets()
	owner := exec.Command(os.Args[0], "-test.run=^TestWindowsDockerSSHAgentOwnerProcess$")
	owner.Env = append(os.Environ(), "LOOPFORGE_SSH_OWNER_HELPER=1", "LOOPFORGE_SSH_OWNER_CONTAINER="+sandbox.name, "LOOPFORGE_SSH_OWNER_WORKSPACE="+workspace)
	owner.Stdin = strings.NewReader(firstKey)
	readyOutput, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	owner.Stderr = io.Discard
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Process.Kill() })
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(readyOutput).ReadString('\n'); ready <- strings.TrimSpace(line) }()
	select {
	case line := <-ready:
		if line != "FORCED_AGENT_READY" {
			_ = owner.Process.Kill()
			_ = owner.Wait()
			t.Fatal("forced-stop fixture could not bind its personal SSH agent")
		}
	case <-time.After(15 * time.Second):
		_ = owner.Process.Kill()
		_ = owner.Wait()
		t.Fatal("forced-stop agent fixture did not become ready")
	}
	var crashedSocket string
	for socket := range sockets() {
		if !beforeCrash[socket] {
			crashedSocket = socket
		}
	}
	if crashedSocket == "" {
		t.Fatal("forced-stop fixture did not own a separate agent")
	}
	crashedDirectory := filepath.ToSlash(filepath.Dir(crashedSocket))
	crashedIdentity := ownerIdentity(sandbox.name, crashedDirectory)
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	assertAgentGone(sandbox.name, crashedDirectory, crashedIdentity)
	// Keep the attachment and stdin open but stop renewing its lease. This
	// exercises the daemon-side watchdog rather than relying on EOF delivery.
	leaseDirectory := "/tmp/loopforge-git-ssh-lease-qa-" + stamp
	leaseOwner := exec.CommandContext(ctx, "docker", "exec", "-i", sandbox.name, "sh", "-c", dockerSSHAgentStart, "lease-fixture", leaseDirectory)
	leaseInput, err := leaseOwner.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	leaseOutput, err := leaseOwner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	leaseOwner.Stderr = io.Discard
	closeLeaseJob, err := processgroup.Start(leaseOwner)
	if err != nil {
		t.Fatal(err)
	}
	leaseDone := make(chan struct{})
	go func() { _ = leaseOwner.Wait(); closeLeaseJob(); close(leaseDone) }()
	t.Cleanup(func() {
		_ = leaseInput.Close()
		_ = processgroup.Kill(leaseOwner.Process.Pid)
		<-leaseDone
	})
	leaseReady := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(leaseOutput).ReadString('\n')
		leaseReady <- strings.TrimSpace(line)
	}()
	select {
	case line := <-leaseReady:
		if line != "LOOPFORGE_SSH_AGENT_READY" {
			t.Fatal("watchdog fixture did not start its real SSH agent")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watchdog fixture did not become ready")
	}
	if _, err := run(firstKey, "exec", "-i", "-e", "SSH_AUTH_SOCK="+leaseDirectory+"/agent", sandbox.name, "ssh-add", "-"); err != nil {
		t.Fatal("watchdog fixture could not load its selected identity")
	}
	leaseIdentity := ownerIdentity(sandbox.name, leaseDirectory)
	assertAgentGone(sandbox.name, leaseDirectory, leaseIdentity)
	// A stopped ssh-agent ignores TERM until it resumes. Cancellation must
	// escalate to KILL before removing the exact owner record and socket.
	firstDirectory := filepath.ToSlash(filepath.Dir(firstSocket))
	firstIdentity := ownerIdentity(sandbox.name, firstDirectory)
	if output, err := run("", "exec", sandbox.name, "sh", "-ec", `read -r pid start < "$1/owner"; kill -STOP "$pid"`, "pause-fixture", firstDirectory); err != nil {
		t.Fatalf("could not pause cancellation fixture: %s: %v", output, err)
	}
	cancelFirst()
	stopFirst()
	assertAgentGone(sandbox.name, firstDirectory, firstIdentity)
	if _, err := run("", "exec", "-e", "SSH_AUTH_SOCK="+secondSocket, sandbox.name, "ssh-add", "-L"); err != nil {
		t.Fatal("cancelling first operation killed the concurrent agent")
	}
	secondDirectory := filepath.ToSlash(filepath.Dir(secondSocket))
	secondIdentity := ownerIdentity(sandbox.name, secondDirectory)
	stopSecond()
	stopSecond()
	assertAgentGone(sandbox.name, secondDirectory, secondIdentity)
	if err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte("PRIVATE KEY")) || bytes.Contains(data, []byte(firstKey)) || bytes.Contains(data, []byte(secondKey)) {
			return fmt.Errorf("private key reached a workspace file")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Launched only by the real Docker test, with the synthetic key on stdin.
// The parent forcibly terminates this process without cleanup or cancellation.
func TestWindowsDockerSSHAgentOwnerProcess(t *testing.T) {
	if os.Getenv("LOOPFORGE_SSH_OWNER_HELPER") != "1" {
		t.Skip("subprocess fixture for forced owner termination")
	}
	identity, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	workspace := os.Getenv("LOOPFORGE_SSH_OWNER_WORKSPACE")
	sandbox := &dockerSandbox{name: os.Getenv("LOOPFORGE_SSH_OWNER_CONTAINER"), workspace: workspace,
		profile: domain.SandboxProfile{Driver: "docker"}, dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	stop, err := BindGitSSHContext(context.Background(), sandbox, workspace, map[string]string{}, map[string]string{"ssh://root@ssh-fixture/fixture.git": string(identity)})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	fmt.Println("FORCED_AGENT_READY")
	for {
		time.Sleep(time.Second)
	}
}
