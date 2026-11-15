package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// This test is deliberately separate from the fake Docker argv tests. It must
// run on the Windows host and bind real Windows files into real Linux containers.
func TestWindowsLiveDockerConversationFilesMasksAndCancel(t *testing.T) {
	if os.Getenv("LOOPFORGE_WINDOWS_DOCKER_LIVE") != "1" {
		t.Skip("set LOOPFORGE_WINDOWS_DOCKER_LIVE=1 to exercise the actual local Linux Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.OSType}}").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "linux" {
		t.Fatalf("a working local Linux Docker daemon is required: %s: %v", output, err)
	}
	if image := os.Getenv("AGENTFLOW_ISOLATED_IMAGE"); image == "" {
		t.Fatal("AGENTFLOW_ISOLATED_IMAGE must name the locally prepared Linux acceptance image")
	}
	root := filepath.Join(t.TempDir(), "real conversation with spaces ' quote")
	workspace := filepath.Join(root, "ws")
	for _, directory := range []string{workspace, filepath.Join(root, "home", "tmp")} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	instructions := filepath.Join(workspace, "CLAUDE.md")
	if err := os.WriteFile(instructions, []byte("repository instruction must remain host-owned"), 0600); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("agentflow-windows-conversation-qa-%d", time.Now().UnixNano())
	sandbox := &dockerSandbox{name: name, workspace: workspace, profile: domain.SandboxProfile{Driver: "docker"},
		requirements: SandboxRequirements{ConversationRoot: root}, dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_ = sandbox.Close(cleanupCtx)
	})
	pool := &ConversationProcessPool{}
	const source = "/platform/conversation/session"
	_, err := pool.Handle(ctx, root, workspace, &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{Driver: "docker"}}, ConversationProcessRequest{
		Operation: "sync", SourceRoot: source, Files: []ConversationFile{{Path: "current-settings.json", Mode: 0600, Rewrite: true,
			Data: []byte(`{"cwd":"` + source + `/ws"}`)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	configRoot := filepath.Join(workspace, ".agentflow", "config", "real-gate")
	if err := os.MkdirAll(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	gate, err := materializeGateMCP(workspace, configRoot, ".agentflow/config/real-gate/gate-mcp", "live-gate-result.json", "docker")
	if err != nil {
		t.Fatal(err)
	}
	gateInput := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"submit_gate_result","arguments":{"decision":"pass","reason":"real Windows Docker materialized Gate"}}}` + "\n"
	gateCommand := WithCommandEnvironment(CommandSpec{Argv: append([]string{gate.Command}, gate.Args...), Stdin: strings.NewReader(gateInput)}, gate.Environment)
	if output, err := sandbox.Exec(ctx, gateCommand, nil); err != nil {
		t.Fatalf("materialized Linux Gate could not execute from the Windows bind mount: %s: %v", output, err)
	}
	gateResult, err := os.ReadFile(filepath.Join(workspace, "live-gate-result.json"))
	var result domain.GateResult
	if err != nil || json.Unmarshal(gateResult, &result) != nil || result.Decision != "pass" {
		t.Fatalf("materialized Gate result did not reach Windows: %s: %v", gateResult, err)
	}
	probe := `import json,os
assert os.getcwd()=="/loopforge/conversation/ws",os.getcwd()
assert os.environ["HOME"]=="/loopforge/conversation/home",os.environ.get("HOME")
assert json.load(open("/loopforge/conversation/current-settings.json"))["cwd"]==os.getcwd()
for file in ["/workspace/CLAUDE.md","/loopforge/conversation/ws/CLAUDE.md"]:
    assert open(file).read()=="",file
try:
    open("/loopforge/conversation/home/.cursor/hooks.json","w").write("replaced")
except OSError:
    pass
else:
    raise RuntimeError("approval guard was writable")
open("/tmp/loopforge-conversation-warm","w").write("retained")
json.dump({"cwd":os.getcwd(),"home":os.environ["HOME"]},open("conversation-receipt.json","w"))
print("REAL_DOCKER_CONVERSATION_PATHS_VERIFIED")
`
	command := CommandSpec{Argv: []string{"python3", "-c", probe}, Provider: "codex", WorkingDir: workspace, OwnProcessGroup: true}
	command = command.WithPrivateHomeDir(filepath.Join(root, "home"))
	if output, err := sandbox.Exec(ctx, command, nil); err != nil {
		t.Fatalf("real Docker conversation path/guard probe failed: %s: %v", output, err)
	}
	var receipt map[string]string
	data, err := os.ReadFile(filepath.Join(workspace, "conversation-receipt.json"))
	if err != nil || json.Unmarshal(data, &receipt) != nil || receipt["cwd"] != "/loopforge/conversation/ws" {
		t.Fatalf("container artifact did not reach the Windows bind: %s: %v", data, err)
	}
	if data, err := os.ReadFile(instructions); err != nil || string(data) != "repository instruction must remain host-owned" {
		t.Fatalf("mask altered host instructions: %s: %v", data, err)
	}
	stdinFrame := "{\"message\":\"exact JSON stdin\",\"windowsPath\":\"C:\\\\fixture\\\\file.txt\"}\n"
	stdinCommand := command
	stdinCommand.Argv = []string{"python3", "-I", "-c", `import json,sys
frame=sys.stdin.buffer.read()
json.loads(frame)
open("stdin-receipt.json","wb").write(frame)
`}
	stdinCommand.Stdin = strings.NewReader(stdinFrame)
	if output, err := sandbox.Exec(ctx, stdinCommand, nil); err != nil {
		t.Fatalf("owner lease corrupted conversation stdin: %s: %v", output, err)
	}
	if actual, err := os.ReadFile(filepath.Join(workspace, "stdin-receipt.json")); err != nil || string(actual) != stdinFrame {
		t.Fatalf("conversation stdin was not byte-exact: %q: %v", actual, err)
	}
	if output, err := sandbox.Exec(ctx, CommandSpec{Provider: "script", Argv: []string{"sh", "-ec", `test "$(cat /tmp/loopforge-conversation-warm)" = retained; test "$(cat CLAUDE.md)" = 'repository instruction must remain host-owned'`}}, nil); err != nil {
		t.Fatalf("real script view lost warm state or could not unmask host instructions: %s: %v", output, err)
	}

	// The subprocess double-forks and changes session, so stopping a docker
	// client or the original CLI process group cannot accidentally pass this test.
	child := `import os,time
if os.fork(): os._exit(0)
os.setsid()
if os.fork(): os._exit(0)
import json
start=open("/proc/self/stat").read().rpartition(") ")[2].split()[19]
json.dump({"pid":os.getpid(),"start":start},open("/loopforge/conversation/ws/detached-ready.tmp","w"))
os.replace("/loopforge/conversation/ws/detached-ready.tmp","/loopforge/conversation/ws/detached-ready")
time.sleep(2)
open("/loopforge/conversation/ws/detached-escaped","w").write("escaped")
`
	parent := `import subprocess,sys,time
subprocess.run([sys.executable,"-c",sys.argv[1]],check=True)
time.sleep(30)
`
	cancelCtx, cancelProcess := context.WithCancel(ctx)
	defer cancelProcess()
	command.Argv = []string{"python3", "-c", parent, child}
	finished := make(chan error, 1)
	go func() { _, err := sandbox.Exec(cancelCtx, command, nil); finished <- err }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(workspace, "detached-ready")); err == nil {
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("container supervisor stopped before starting its detached child: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("container detached child did not reach the Windows bind marker")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var detached struct {
		PID   int    `json:"pid"`
		Start string `json:"start"`
	}
	ready, err := os.ReadFile(filepath.Join(workspace, "detached-ready"))
	if err != nil || json.Unmarshal(ready, &detached) != nil || detached.PID <= 0 || detached.Start == "" {
		t.Fatal("detached child did not record its exact kernel PID/start identity")
	}
	cancelProcess()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled container conversation returned success")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Docker conversation cancellation did not return")
	}
	probeGone := dockerSSHProcessIdentity + `test "$(process_start "$1" 2>/dev/null || :)" != "$2"`
	if output, err := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-ec", probeGone, "verify-child-reaped", fmt.Sprint(detached.PID), detached.Start).CombinedOutput(); err != nil {
		t.Fatalf("detached conversation child retained its exact kernel identity after cancellation: %s: %v", output, err)
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(workspace, "detached-escaped")); !os.IsNotExist(err) {
		t.Fatalf("detached container child survived cancellation: %v", err)
	}
	if output, err := sandbox.Exec(ctx, CommandSpec{Argv: []string{"sh", "-ec", "test -f conversation-receipt.json"}}, nil); err != nil {
		t.Fatalf("cancel destroyed the reusable workspace sandbox: %s: %v", output, err)
	}
	if err := sandbox.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for _, container := range []string{name, dockerScriptContainerName(name)} {
		if output, err := exec.CommandContext(ctx, "docker", "inspect", container).CombinedOutput(); err == nil || !dockerObjectGone(string(output), err) {
			t.Fatalf("real Docker test leaked %s: %s: %v", container, output, err)
		}
	}
}

func TestWindowsLiveDockerConversationForcedOwner(t *testing.T) {
	if os.Getenv("LOOPFORGE_WINDOWS_DOCKER_LIVE") != "1" {
		t.Skip("requires the actual local Linux Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	for _, directory := range []string{workspace, filepath.Join(root, "home", "tmp")} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	name := fmt.Sprintf("agentflow-conversation-owner-qa-%d", time.Now().UnixNano())
	sandbox := &dockerSandbox{name: name, workspace: workspace, profile: domain.SandboxProfile{Driver: "docker"},
		requirements: SandboxRequirements{ConversationRoot: root}, dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	t.Cleanup(func() { _ = sandbox.Close(context.Background()) })
	if err := sandbox.ensureStarted(ctx); err != nil {
		t.Fatal(err)
	}
	frame := "{\"owner\":\"stdin must not contain heartbeat frames\"}\n"
	owner := exec.Command(os.Args[0], "-test.run=^TestWindowsDockerConversationLeaseOwnerProcess$")
	owner.Env = append(os.Environ(), "LOOPFORGE_CONVERSATION_OWNER_HELPER=1", "LOOPFORGE_CONVERSATION_OWNER_CONTAINER="+name,
		"LOOPFORGE_CONVERSATION_OWNER_ROOT="+root, "LOOPFORGE_CONVERSATION_OWNER_WORKSPACE="+workspace)
	owner.Stdin = strings.NewReader(frame)
	owner.Stdout, owner.Stderr = io.Discard, io.Discard
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if owner.ProcessState == nil {
			_ = owner.Process.Kill()
			_ = owner.Wait()
		}
	})
	ready := filepath.Join(workspace, "owner-ready.json")
	deadline := time.Now().Add(15 * time.Second)
	var identities []map[string]any
	for {
		if data, err := os.ReadFile(ready); err == nil && json.Unmarshal(data, &identities) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real conversation did not record its active process identities")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// This is TerminateProcess, not graceful cancellation: all owner defers die.
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	encoded, _ := json.Marshal(identities)
	probe := `import json,os,sys
alive=[]
for identity in json.loads(sys.argv[1]):
    try:
        fields=open('/proc/'+str(identity['pid'])+'/stat').read().rsplit(')',1)[1].split()
        if fields[19]==identity['start']: alive.append(dict(identity,state=fields[0]))
    except FileNotFoundError: pass
print(json.dumps(alive))
sys.exit(1 if alive else 0)
`
	deadline = time.Now().Add(12 * time.Second)
	for {
		output, err := exec.CommandContext(ctx, "docker", "exec", name, "/usr/bin/python3", "-I", "-c", probe, string(encoded)).CombinedOutput()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("forced owner death retained exact remote process identities: %s: %v", output, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(11 * time.Second)
	if _, err := os.Stat(filepath.Join(workspace, "owner-escaped")); !os.IsNotExist(err) {
		t.Fatalf("remote conversation wrote after its owner was terminated: %v", err)
	}
	if actual, err := os.ReadFile(filepath.Join(workspace, "owner-stdin.json")); err != nil || string(actual) != frame {
		t.Fatalf("owner control channel changed task stdin: %q: %v", actual, err)
	}
}

func TestWindowsDockerConversationLeaseOwnerProcess(t *testing.T) {
	if os.Getenv("LOOPFORGE_CONVERSATION_OWNER_HELPER") != "1" {
		t.Skip("forced owner subprocess fixture")
	}
	root, workspace := os.Getenv("LOOPFORGE_CONVERSATION_OWNER_ROOT"), os.Getenv("LOOPFORGE_CONVERSATION_OWNER_WORKSPACE")
	sandbox := &dockerSandbox{name: os.Getenv("LOOPFORGE_CONVERSATION_OWNER_CONTAINER"), workspace: workspace,
		profile: domain.SandboxProfile{Driver: "docker"}, requirements: SandboxRequirements{ConversationRoot: root},
		dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	program := `import json,os,subprocess,sys,time
frame=sys.stdin.buffer.readline()
json.loads(frame)
open('owner-stdin.json','wb').write(frame)
def identity(pid):
    return {'pid':pid,'start':open('/proc/'+str(pid)+'/stat').read().rsplit(')',1)[1].split()[19]}
child="""import json,os,time
if os.fork(): os._exit(0)
os.setsid()
if os.fork(): os._exit(0)
identity={'pid':os.getpid(),'start':open('/proc/self/stat').read().rsplit(')',1)[1].split()[19]}
json.dump(identity,open('owner-child.tmp','w'))
os.replace('owner-child.tmp','owner-child.json')
time.sleep(10)
open('owner-escaped','w').write('escaped')
time.sleep(30)
"""
subprocess.run([sys.executable,'-I','-c',child],check=True)
while not os.path.exists('owner-child.json'): time.sleep(0.01)
json.dump([identity(os.getpid()),identity(os.getppid()),json.load(open('owner-child.json'))],open('owner-ready.tmp','w'))
os.replace('owner-ready.tmp','owner-ready.json')
time.sleep(30)
`
	// A generic provider with OwnProcessGroup=false must have the same owner
	// lifetime as Codex; instruction masking is an independent classification.
	command := CommandSpec{Provider: "opencode", Argv: []string{"python3", "-I", "-c", program}, WorkingDir: workspace, Stdin: os.Stdin}
	if output, err := sandbox.Exec(context.Background(), command, nil); err != nil {
		t.Fatalf("owner fixture stopped before forced termination: %s: %v", output, err)
	}
}
