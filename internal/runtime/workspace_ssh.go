package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"agentflow/internal/workspacessh"
)

const workspaceSSHHome = "/workspace/.agentflow/private-home/desktop-ssh"

// OpenWorkspaceSSH starts the trusted, bind-mounted helper in the Run's
// existing Docker sandbox. Both directions use docker exec's stdio, so each
// sandbox keeps its configured user, capabilities, and networking.
func OpenWorkspaceSSH(ctx context.Context, sandbox Sandbox, keys workspacessh.Keys) (io.ReadWriteCloser, error) {
	docker, ok := unwrapSandbox(sandbox).(*dockerSandbox)
	if !ok {
		return nil, fmt.Errorf("桌面 IDE 连接目前支持 Docker 工作区")
	}
	if _, err := EnsureControlDirectory(docker.workspace, "private-home", "desktop-ssh"); err != nil {
		return nil, err
	}
	if err := docker.ensureStarted(ctx); err != nil {
		return nil, err
	}
	docker.mu.Lock()
	toolsMounted := docker.gitToolsRoot != ""
	docker.mu.Unlock()
	if !toolsMounted {
		return nil, fmt.Errorf("工作区需要重新准备才能启动桌面 IDE")
	}
	if _, _, err := prepareWorkspaceGitTools(ctx, docker, docker.workspace, false); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	args := []string{"exec", "-i"}
	for _, value := range appendEnvironment(nil, docker.profile.Environment, map[string]string{"HOME": workspaceSSHHome}) {
		args = append(args, "-e", value)
	}
	args = append(args, docker.name, dockerGitToolsMount+"/bin/agentflow-helper", workspacessh.ServerCommand)
	cmd := exec.CommandContext(ctx, "docker", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return nil, err
	}
	release := docker.reserveTerminal()
	if err := cmd.Start(); err != nil {
		cancel()
		release()
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	stream := &workspaceSSHProcess{ReadCloser: stdout, WriteCloser: stdin, cmd: cmd, cancel: cancel, release: release}
	timer := time.AfterFunc(workspacessh.HandshakeTimeout, cancel)
	defer timer.Stop()
	if err := json.NewEncoder(stdin).Encode(keys); err == nil {
		var ready [1]byte
		if _, err := io.ReadFull(stdout, ready[:]); err == nil && ready[0] == workspacessh.SandboxReady {
			return stream, nil
		}
	}
	_ = stream.Close()
	return nil, fmt.Errorf("沙箱未能启动 SSH，请确认平台和执行机器使用此版本")
}

type workspaceSSHProcess struct {
	io.ReadCloser
	io.WriteCloser
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	release func()
	once    sync.Once
}

func (p *workspaceSSHProcess) Close() error {
	p.once.Do(func() {
		_ = p.WriteCloser.Close()
		_ = p.ReadCloser.Close()
		fallback := time.AfterFunc(2*time.Second, p.cancel)
		_ = p.cmd.Wait()
		fallback.Stop()
		p.cancel()
		p.release()
	})
	return nil
}
