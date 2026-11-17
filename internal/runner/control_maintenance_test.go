package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"

	"github.com/gorilla/websocket"
)

func TestControlMaintenanceUpdateOutlivesControlConnection(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "cursor-agent")
	started, release, version := filepath.Join(dir, "started"), filepath.Join(dir, "release"), filepath.Join(dir, "version")
	script := fmt.Sprintf(`#!%s
case "$1" in
  --version) IFS= read -r v < %q; printf '%%s\n' "$v" ;;
  --help) printf 'Usage: agent\n\nCommands:\n  update [options]  Update Cursor\n' ;;
  update)
    printf started > %q
    while ! test -f %q; do %q 0.01; done
    printf '2.0.0\n' > %q ;;
  *) exit 3 ;;
esac
`, shell, version, started, release, sleep, version)
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(version, []byte("1.0.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Keep discovery confined to the fixture, including the refresh after
	// updating. Do not inspect or run the developer's installed CLIs.
	binaries := make(map[string]string)
	for _, spec := range defaultProviders {
		binaries[spec.Kind] = filepath.Join(dir, "missing-"+spec.Kind)
	}
	binaries["cursor"] = binary
	d := &Daemon{cfg: Config{ExtraBinaries: binaries}, heartbeatWake: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d.refreshRuntimes(ctx)
	peers := make(chan *websocket.Conn, 1)
	releasePeer := make(chan struct{})
	conn := controlTestServer(t, func(peer *websocket.Conn) { peers <- peer; <-releasePeer })
	defer close(releasePeer)
	peer := <-peers
	loopDone := make(chan struct{})
	go func() { _ = d.readControls(ctx, conn); close(loopDone) }()
	defer func() {
		cancel()
		_ = peer.Close()
		<-loopDone
		d.maintenance.jobs.Wait()
	}()
	if err := peer.WriteJSON(controlMessage{
		Type: "workspace.request", RequestID: "cli-update",
		WorkspaceRequest: &domain.RunnerWorkspaceRequest{
			Operation:   domain.RunnerWorkspaceOperationMaintenance,
			Maintenance: &domain.RunnerMaintenanceRequest{Provider: "cursor", Apply: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	var response controlMessage
	if err := peer.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "workspace.response" || response.RequestID != "cli-update" || response.WorkspaceResponse == nil || response.WorkspaceResponse.Status != 200 {
		t.Fatalf("maintenance was not dispatched: %+v", response)
	}
	var accepted domain.RunnerMaintenanceResult
	if err := json.Unmarshal(response.WorkspaceResponse.Payload, &accepted); err != nil || accepted.Status != "updating" {
		t.Fatalf("maintenance acceptance: %+v, %v", accepted, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted update did not start")
		}
		time.Sleep(time.Millisecond)
	}
	// The update owns daemonCtx, unlike workspace preparation. Ending the
	// control RPC/connection must not kill the already accepted subprocess.
	_ = peer.Close()
	select {
	case <-loopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("control connection did not stop")
	}
	updateDone := make(chan struct{})
	go func() { d.maintenance.jobs.Wait(); close(updateDone) }()
	select {
	case <-updateDone:
		t.Fatal("control disconnect interrupted the accepted update")
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updateDone:
	case <-time.After(3 * time.Second):
		t.Fatal("update did not finish after the control connection closed")
	}
	d.maintenance.mu.Lock()
	result := d.maintenance.results["cursor"]
	d.maintenance.mu.Unlock()
	if result.Status != "updated" || result.Version != "2.0.0" {
		t.Fatalf("maintenance result after disconnect: %+v", result)
	}
}
