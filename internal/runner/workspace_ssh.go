package runner

import (
	"context"
	"fmt"
	"net/http"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"agentflow/internal/workspacessh"

	"github.com/gorilla/websocket"
)

func (d *Daemon) openRunnerWorkspaceSSH(ctx context.Context, root string, request domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse {
	link := request.DesktopSSH
	if link == nil || len(link.Ticket) < 16 || len(link.Ticket) > 128 || len(link.HostSeed) != 32 || len(link.ClientKey) == 0 || len(link.ClientKey) > 4096 {
		return runnerWorkspaceError(fmt.Errorf("invalid desktop SSH request"))
	}
	task, err := d.workspaceSandboxTask(root, request)
	if err != nil {
		return runnerWorkspaceError(err)
	}
	// A desktop SSH connection participates in the same workspace fence as
	// active tasks. Other desktop connections can attach to this generation.
	gate := d.requestGate(request)
	if !gate.TryRLock() {
		return workspaceBusyResponse("workspace is being prepared, restored or saved")
	}
	sandbox, release := d.acquireExistingTaskSandbox(task)
	if sandbox == nil {
		gate.RUnlock()
		return workspaceBusyResponse("prepare the workspace before opening desktop SSH")
	}
	release = onceRelease(release, gate.RUnlock)
	stream, err := runtime.OpenWorkspaceSSH(d.withDockerRunnerHelperWait(ctx), sandbox, workspacessh.Keys{
		HostSeed: link.HostSeed, ClientKey: link.ClientKey,
	})
	if err != nil {
		release()
		return runnerWorkspaceError(err)
	}
	socket, err := d.client.openWorkspaceSSHRelay(ctx, link.Ticket)
	if err != nil {
		_ = stream.Close()
		release()
		return runnerWorkspaceError(err)
	}
	go func() {
		defer release()
		workspacessh.Relay(ctx, workspacessh.NewSocket(socket), stream)
	}()
	return runnerWorkspacePayload(map[string]bool{"connected": true})
}

func (c *Client) openWorkspaceSSHRelay(ctx context.Context, ticket string) (*websocket.Conn, error) {
	address, err := workspacessh.WebSocketURL(c.base, workspacessh.RelayPath)
	if err != nil {
		return nil, err
	}
	header := http.Header{"Authorization": {"Bearer " + c.sessionToken()}}
	header.Set(workspacessh.RelayTicketHeader, ticket)
	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: workspacessh.HandshakeTimeout}
	conn, response, err := dialer.DialContext(ctx, address, header)
	if response != nil && err != nil {
		_ = response.Body.Close()
		return nil, fmt.Errorf("platform SSH relay returned HTTP %d", response.StatusCode)
	}
	return conn, err
}
