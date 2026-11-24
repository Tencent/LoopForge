package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"agentflow/internal/domain"
)

func TestDockerWorkspacePreviewServiceOutlivesConversation(t *testing.T) {
	ctx, sb := dockerScriptTestSandbox(t)
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "python:3.13-alpine")
	sb.profile.ReadOnlyRootFS = true
	root := dockerScriptTestConversationRoot(t, sb)
	if err := os.MkdirAll(filepath.Join(root, "home", "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureConversationSandbox(ctx, sb, root); err != nil {
		t.Fatal(err)
	}

	turn, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		// The native tool leaves a detached server behind when the CLI exits.
		command := WithCommandEnvironment(CommandSpec{OwnProcessGroup: true, Argv: []string{"python3", "-c", `
import os, subprocess, sys, time
subprocess.Popen([sys.executable, '-m', 'http.server', '5174', '--bind', '127.0.0.1'],
    stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    start_new_session=True)
while not os.path.exists('finish-turn'):
    time.sleep(0.02)
`}}, map[string]string{"PREVIEW_TEST_TURN_SECRET": "private-to-turn"})
		out, err := sb.Exec(turn, command, nil)
		if err != nil {
			err = fmt.Errorf("conversation CLI: %w: %s", err, out)
		}
		finished <- err
	}()
	checkDockerPreviewPort(t, ctx, sb, 5174, true)

	control := func(operation, name, command, want string) {
		t.Helper()
		got, err := WorkspacePreviewService(ctx, sb, domain.WorkspacePreviewServiceRequest{Operation: operation, Name: name, Command: command})
		if err != nil || got.Name != name || got.Status != want {
			t.Fatalf("%s %s = %+v, %v, want %s", operation, name, got, err, want)
		}
	}
	serverCommand := func(port int) string {
		return fmt.Sprintf(`test -z "$PREVIEW_TEST_TURN_SECRET" && exec python3 -c '
import subprocess, sys
command = [sys.executable, "-m", "http.server", sys.argv[1], "--bind", "127.0.0.1"]
sys.exit(subprocess.call(command, start_new_session=True))
' %d`, port)
	}
	for name, port := range map[string]int{"frontend": 5173, "backend": 8080} {
		control("start", name, serverCommand(port), "running")
		checkDockerPreviewPort(t, ctx, sb, port, true)
	}
	control("start", "frontend", "touch duplicate-start", "running")
	if _, err := os.Stat(filepath.Join(sb.workspace, "duplicate-start")); !os.IsNotExist(err) {
		t.Fatalf("duplicate start was executed: %v", err)
	}

	if err := os.WriteFile(filepath.Join(sb.workspace, "finish-turn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	checkDockerPreviewPort(t, ctx, sb, 5174, false)
	checkDockerPreviewPort(t, ctx, sb, 5173, true)
	checkDockerPreviewPort(t, ctx, sb, 8080, true)
	control("status", "frontend", "", "running")

	killSupervisor := func(record string) {
		t.Helper()
		out, err := sb.Exec(ctx, CommandSpec{Argv: []string{"python3", "-c", `
import json, os, signal, sys
with open("/dev/shm/loopforge-preview/" + sys.argv[1]) as file:
    os.kill(json.load(file)["pid"], signal.SIGKILL)
`, record}}, nil)
		if err != nil {
			t.Fatalf("kill preview supervisor %s: %v %s", record, err, out)
		}
	}
	// The outer subreaper cleans up even the server in a separate session.
	killSupervisor("frontend.json")
	checkDockerPreviewPort(t, ctx, sb, 5173, false)
	control("start", "frontend", serverCommand(5173), "running")
	checkDockerPreviewPort(t, ctx, sb, 5173, true)
	checkDockerPreviewPort(t, ctx, sb, 8080, true)

	// If the outer process dies first, the inner supervisor still owns the tree.
	killSupervisor("frontend.json.guard")
	control("status", "frontend", "", "running")
	control("stop", "frontend", "", "stopped")
	control("stop", "frontend", "", "stopped")
	checkDockerPreviewPort(t, ctx, sb, 5173, false)
	checkDockerPreviewPort(t, ctx, sb, 8080, true)
	control("stop", "backend", "", "stopped")
	checkDockerPreviewPort(t, ctx, sb, 8080, false)
}

func checkDockerPreviewPort(t *testing.T, ctx context.Context, sb *dockerSandbox, port int, available bool) {
	t.Helper()
	out, err := sb.Exec(ctx, CommandSpec{Argv: []string{"python3", "-c", `
from http.client import HTTPConnection
import sys, time

port, expected = int(sys.argv[1]), sys.argv[2] == 'true'
deadline = time.monotonic() + 5
while time.monotonic() < deadline:
    connection = HTTPConnection('127.0.0.1', port, timeout=0.25)
    try:
        connection.request('GET', '/')
        available = connection.getresponse().status == 200
    except ConnectionRefusedError:
        available = False
    finally:
        connection.close()
    if available == expected:
        sys.exit(0)
    time.sleep(0.05)
sys.exit(1)
`, strconv.Itoa(port), strconv.FormatBool(available)}}, nil)
	if err != nil {
		t.Fatalf("port %d did not reach available=%t: %v %s", port, available, err, out)
	}
}
