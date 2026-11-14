package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

type cvRProtocolSandbox struct {
	workspace string
	driver    string
	lines     []domain.TelemetryEvent
	raw       string
	err       error
	command   CommandSpec
}

func (s *cvRProtocolSandbox) ID() string                  { return "cvR-protocol" }
func (s *cvRProtocolSandbox) Workspace() string           { return s.workspace }
func (s *cvRProtocolSandbox) Driver() string              { return s.driver }
func (s *cvRProtocolSandbox) Close(context.Context) error { return nil }
func (s *cvRProtocolSandbox) Exec(_ context.Context, command CommandSpec, sink EventSink) (string, error) {
	s.command = command
	for _, line := range s.lines {
		sink(line)
	}
	return s.raw, s.err
}

func TestPrepareRemoteProvidersAndBuiltinGate(t *testing.T) {
	runner := NewExecutorRunner()
	for _, kind := range []string{"claude-code", "codebuddy", "codex", "custom"} {
		workspace := t.TempDir()
		prepared, err := runner.PrepareRemote(workspace, RemoteInvocation{
			InvocationID: "inv-" + kind,
			Executor:     domain.ExecutorDefinition{Kind: kind, Binary: kind, Protocol: "jsonl"},
			Agent:        domain.AgentDefinition{ID: "agent", Name: "Agent"},
			Prompt:       "do work",
			Driver:       "local",
		})
		if err != nil || prepared.Builtin || len(prepared.Command.Argv) == 0 {
			t.Fatalf("%s prepared=%+v err=%v", kind, prepared, err)
		}
	}
	workspace := t.TempDir()
	gate := ".agentflow/gate.json"
	prepared, err := runner.PrepareRemote(workspace, RemoteInvocation{
		InvocationID: "mock", Executor: domain.ExecutorDefinition{Kind: "mock"},
		Agent:  domain.AgentDefinition{ID: "gate-agent", Name: "Gate"},
		Prompt: "ok", Options: InvocationOptions{GateResultPath: gate},
	})
	if err != nil || !prepared.Builtin {
		t.Fatalf("mock=%+v err=%v", prepared, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, gate)); err != nil {
		t.Fatal(err)
	}
	lost := domain.AgentDefinition{ID: "lost", Prompt: "[[mock:session=lost]]"}
	options := InvocationOptions{GateResultPath: gate, ResumeSessionID: "session"}
	if mockShouldSubmitGate(workspace, lost, options) {
		t.Fatal("first lost session submitted")
	}
	if mockShouldSubmitGate(workspace, lost, options) {
		t.Fatal("resumed lost session submitted")
	}
	options.ResumeSessionID = ""
	if !mockShouldSubmitGate(workspace, lost, options) {
		t.Fatal("fresh retry did not submit")
	}
}

func TestExecutorRunProtocolOutcomes(t *testing.T) {
	runner := NewExecutorRunner()
	t.Run("generic success without result", func(t *testing.T) {
		sb := &cvRProtocolSandbox{workspace: t.TempDir(), driver: "runner", raw: "plain output",
			lines: []domain.TelemetryEvent{{Name: "process.stdout", Message: `{"type":"message","text":"hello"}`}}}
		got, err := runner.Run(context.Background(), sb, "inv", domain.ExecutorDefinition{Kind: "custom", Binary: "custom", Protocol: "jsonl"},
			domain.AgentDefinition{ID: "a"}, domain.AgentAssetBundle{}, "prompt", InvocationOptions{}, nil)
		if err != nil || got != "plain output" {
			t.Fatalf("got=%q err=%v", got, err)
		}
	})
	t.Run("claude reported failure", func(t *testing.T) {
		sb := &cvRProtocolSandbox{workspace: t.TempDir(), driver: "runner",
			lines: []domain.TelemetryEvent{{Name: "process.stdout", Message: `{"type":"result","result":"401 Unauthorized","is_error":true}`}}}
		got, err := runner.Run(context.Background(), sb, "inv", domain.ExecutorDefinition{Kind: "claude-code", Binary: "claude", Protocol: "stream-json"},
			domain.AgentDefinition{ID: "a"}, domain.AgentAssetBundle{}, "prompt", InvocationOptions{}, nil)
		if err == nil || !strings.Contains(err.Error(), "401") || got != "401 Unauthorized" {
			t.Fatalf("got=%q err=%v", got, err)
		}
	})
	t.Run("max turns", func(t *testing.T) {
		sb := &cvRProtocolSandbox{workspace: t.TempDir(), driver: "runner",
			lines: []domain.TelemetryEvent{{Name: "process.stdout", Message: `{"type":"result","result":"maximum turns reached","is_error":true}`}}}
		_, err := runner.Run(context.Background(), sb, "inv", domain.ExecutorDefinition{Kind: "claude-code", Binary: "claude", Protocol: "stream-json"},
			domain.AgentDefinition{ID: "a"}, domain.AgentAssetBundle{}, "prompt", InvocationOptions{}, nil)
		if !errors.Is(err, ErrMaxTurns) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("process error before terminal result", func(t *testing.T) {
		sb := &cvRProtocolSandbox{workspace: t.TempDir(), driver: "runner", raw: "partial", err: errors.New("exit")}
		got, err := runner.Run(context.Background(), sb, "inv", domain.ExecutorDefinition{Kind: "custom", Binary: "custom", Protocol: "jsonl"},
			domain.AgentDefinition{ID: "a"}, domain.AgentAssetBundle{}, "prompt", InvocationOptions{}, nil)
		if err == nil || got != "partial" {
			t.Fatalf("got=%q err=%v", got, err)
		}
	})
	t.Run("mock lost resumed session", func(t *testing.T) {
		sb := &cvRProtocolSandbox{workspace: t.TempDir(), driver: "runner"}
		got, err := runner.Run(context.Background(), sb, "inv", domain.ExecutorDefinition{Kind: "mock"},
			domain.AgentDefinition{ID: "a", Prompt: "[[mock:session=lost]]"}, domain.AgentAssetBundle{}, "prompt",
			InvocationOptions{ResumeSessionID: "missing"}, nil)
		if err != nil || !strings.Contains(got, "No conversation found") {
			t.Fatalf("got=%q err=%v", got, err)
		}
	})
	t.Run("mock gate observation", func(t *testing.T) {
		sb := &cvRProtocolSandbox{workspace: t.TempDir(), driver: "runner"}
		var events []domain.TelemetryEvent
		got, err := runner.Run(context.Background(), sb, "inv", domain.ExecutorDefinition{Kind: "mock"},
			domain.AgentDefinition{ID: "a"}, domain.AgentAssetBundle{}, "prompt",
			InvocationOptions{GateResultPath: ".agentflow/gate.json"}, func(e domain.TelemetryEvent) { events = append(events, e) })
		if err != nil || got == "" || len(events) == 0 {
			t.Fatalf("got=%q events=%v err=%v", got, events, err)
		}
	})
}

func TestProviderRequirementAndCommandHelpers(t *testing.T) {
	base := providerForKind("custom")
	if base.Kind() != "custom" {
		t.Fatal("generic kind")
	}
	if err := base.Materialize(&providerMaterializeContext{}); err != nil {
		t.Fatal(err)
	}
	command, err := base.Prepare(providerInvocation{Executor: domain.ExecutorDefinition{Kind: "custom", Binary: "tool", Model: "m"}, Prompt: "p"})
	if err != nil || command.Argv[len(command.Argv)-1] != "p" {
		t.Fatalf("command=%+v err=%v", command, err)
	}
	merged := MergeSandboxRequirements(
		SandboxRequirements{BunFS: true, CLIStates: []CLIStateRequirement{{Directory: ".a"}, {Directory: ""}}},
		SandboxRequirements{CLIStates: []CLIStateRequirement{{Directory: ".a"}, {Directory: ".b"}}},
	)
	if !merged.BunFS || len(merged.CLIStates) != 2 {
		t.Fatalf("merged=%+v", merged)
	}
	for _, kind := range []string{"claude-code", "codebuddy", "codex", "custom"} {
		command, err := PrepareProviderCommand(domain.ExecutorDefinition{Kind: kind, Binary: kind}, "prompt", "runner", InvocationOptions{})
		if err != nil || len(command.Argv) == 0 {
			t.Fatalf("%s command=%+v err=%v", kind, command, err)
		}
	}
}

func TestCheckpointValidationStatusAndBounds(t *testing.T) {
	valid := EmptyWorkspaceCheckpoint()
	if err := ValidateWorkspaceCheckpoint(valid); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkspaceCheckpoint(nil); err == nil {
		t.Fatal("empty checkpoint accepted")
	}
	if err := ValidateWorkspaceCheckpoint([]byte("bad")); err == nil {
		t.Fatal("invalid checkpoint accepted")
	}
	root := t.TempDir()
	if response := WorkspaceCheckpointStatus(root); response.Status != 200 {
		t.Fatalf("status=%+v", response)
	}
	missing := filepath.Join(root, "missing")
	if response := WorkspaceCheckpointStatus(missing); response.Status != 404 {
		t.Fatalf("missing=%+v", response)
	}
	file := filepath.Join(root, "file")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	if response := WorkspaceCheckpointStatus(file); response.Status != 409 {
		t.Fatalf("file=%+v", response)
	}
	var target strings.Builder
	writer := &checkpointBoundedWriter{writer: &target, left: 2}
	if _, err := writer.Write([]byte("abc")); err == nil {
		t.Fatal("oversized write accepted")
	}
	writer.left = 3
	if n, err := writer.Write([]byte("abc")); err != nil || n != 3 || writer.left != 0 {
		t.Fatalf("n=%d left=%d err=%v", n, writer.left, err)
	}
}

func TestEnvironmentIdentityGitAndMCPHeaders(t *testing.T) {
	workspace := t.TempDir()
	local := &localSandbox{id: "local", workspace: workspace, environment: map[string]string{"A": "1"}}
	identity, err := SandboxEnvironmentIdentity(context.Background(), local)
	if err != nil || !strings.HasPrefix(identity, "local:") {
		t.Fatalf("identity=%q err=%v", identity, err)
	}
	process := &processSandbox{id: "process", workspace: workspace, driver: "bwrap", profile: domain.SandboxProfile{Environment: map[string]string{"B": "2"}}}
	identity, err = ReadSandboxEnvironmentIdentity(context.Background(), process)
	if err != nil || !strings.HasPrefix(identity, "bwrap:") {
		t.Fatalf("process identity=%q err=%v", identity, err)
	}
	managed := &managedSandbox{Sandbox: local}
	if identity, err = ReadSandboxEnvironmentIdentity(context.Background(), managed); err != nil || identity == "" {
		t.Fatalf("managed identity=%q err=%v", identity, err)
	}
	if _, err := ReadSandboxEnvironmentIdentity(context.Background(), &capturingSandbox{workspace: workspace}); err == nil {
		t.Fatal("unsupported identity accepted")
	}
	env, err := GitEnvironment(local, workspace, "oauth2: secret")
	if err != nil || env["AGENTFLOW_GIT_USER"] != "oauth2" || env["AGENTFLOW_GIT_TOKEN"] != "secret" {
		t.Fatalf("git env=%v err=%v", env, err)
	}
	env, err = GitEnvironment(&dockerSandbox{}, workspace, "private: token")
	if err != nil || !strings.HasPrefix(env["GIT_ASKPASS"], "/workspace/") {
		t.Fatalf("docker git env=%v err=%v", env, err)
	}
	env, err = GitEnvironment(nil, workspace, "")
	if err != nil || env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("empty git env=%v err=%v", env, err)
	}
	t.Setenv("CVR_SECRET", "secret")
	headers, err := ResolveMCPHeaders(domain.MCPServerDefinition{ID: "m", Headers: map[string]string{"Authorization": "Bearer ${secret:CVR_SECRET}"}})
	if err != nil || headers["Authorization"] != "Bearer secret" {
		t.Fatalf("headers=%v err=%v", headers, err)
	}
	if _, err := ResolveMCPHeaders(domain.MCPServerDefinition{ID: "m", Headers: map[string]string{"Authorization": "${secret:MISSING_CVR}"}}); err == nil {
		t.Fatal("missing header secret accepted")
	}
}

func TestProtocolToolHelperBranches(t *testing.T) {
	for _, tc := range []struct {
		value any
		ready bool
	}{
		{map[string]any{}, false}, {map[string]any{"x": 1}, true},
		{" ", false}, {"x", true}, {nil, false}, {42, true},
	} {
		if got := toolInputReady(tc.value); got != tc.ready {
			t.Fatalf("ready(%#v)=%v", tc.value, got)
		}
	}
	values := []any{
		nil, " text ", []any{"a", map[string]any{"stdout": "b"}},
		map[string]any{"content": "c"}, map[string]any{"output": "d"}, 42,
	}
	for _, value := range values {
		_ = toolResultText(value)
		_ = slimToolInput(value)
	}
	for _, tc := range []struct {
		name    string
		failed  bool
		content any
	}{
		{"", true, ""}, {"Bash", true, "boom\nmore"}, {"Read", false, "package main"},
		{"Shell", false, "ok"}, {"Other", false, "a\nb"}, {"Other", false, ""},
	} {
		if got := summarizeToolResult(tc.name, tc.failed, tc.content); got == "" {
			t.Fatalf("empty summary: %+v", tc)
		}
	}
	if !looksLikeSource("a\nb\nc\nd") || !looksLikeSource("func x()") || looksLikeSource("plain") {
		t.Fatal("source detection")
	}
	if firstLogLine(" a\r\nb") != "a" || clipToolMsg(strings.Repeat("x", 200)) == strings.Repeat("x", 200) {
		t.Fatal("line clipping")
	}
}

func TestSmallExecutionAndIsolationBranches(t *testing.T) {
	if commandResult(nil) != "process exited successfully" || commandResult(errors.New("x")) != "x" {
		t.Fatal("command result")
	}
	for kind, want := range map[string]string{"claude-code": "claude-code", "codebuddy": "codebuddy", "codex": "codex", "mock": "mock"} {
		if got := providerForKind(kind).Kind(); got != want {
			t.Fatalf("%s kind=%q", kind, got)
		}
	}
	if got := wrapIsolatedMCPCallExec(nil); got != nil {
		t.Fatalf("nil wrap=%v", got)
	}
	wrapped := wrapIsolatedMCPCallExec([]string{"tool", "arg"})
	if len(wrapped) < 5 || wrapped[len(wrapped)-2] != "tool" {
		t.Fatalf("wrapped=%v", wrapped)
	}
	process := &processSandbox{driver: "bwrap"}
	if !sandboxIsolatesProcess(process) || sandboxIsolatesProcess(&localSandbox{}) {
		t.Fatal("isolation classification")
	}
	if got := BindMCPCallArgv(process, []string{"tool", "arg"}); len(got) <= 2 {
		t.Fatalf("bound=%v", got)
	}
	repo := t.TempDir()
	cmd := exec.Command("git", "-C", repo, "init", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	lock, err := gitIndexLockPath(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	inUse, confirmed, err := gitIndexLockInUse(context.Background(), lock)
	if err != nil || !confirmed || !inUse {
		t.Fatalf("open lock inUse=%v confirmed=%v err=%v", inUse, confirmed, err)
	}
	_ = file.Close()
	inUse, confirmed, err = gitIndexLockInUse(context.Background(), lock)
	if err != nil || !confirmed || inUse {
		t.Fatalf("closed lock inUse=%v confirmed=%v err=%v", inUse, confirmed, err)
	}
}

func TestMaterializePathSafetyBranches(t *testing.T) {
	workspace := t.TempDir()
	if _, err := resetControlDirectory(workspace); err == nil {
		t.Fatal("empty reset accepted")
	}
	for _, name := range []string{"", ".", "..", "a/b"} {
		if _, err := resetControlDirectory(workspace, name); err == nil {
			t.Fatalf("invalid reset segment accepted: %q", name)
		}
	}
	target, err := resetControlDirectory(workspace, "runtime", "replace")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(target, "old"), []byte("x"), 0o600)
	if target2, err := resetControlDirectory(workspace, "runtime", "replace"); err != nil || target2 != target {
		t.Fatalf("target=%q err=%v", target2, err)
	}
	if err := replaceRelativeSymlink(filepath.Join(workspace, "absolute"), "/tmp"); err == nil {
		t.Fatal("absolute symlink target accepted")
	}
	path := filepath.Join(workspace, "link")
	if err := replaceRelativeSymlink(path, "one"); err != nil {
		t.Fatal(err)
	}
	if err := replaceRelativeSymlink(path, "two"); err != nil {
		t.Fatal(err)
	}
	if resolved, err := os.Readlink(path); err != nil || resolved != "two" {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
	regular := filepath.Join(workspace, "regular")
	_ = os.WriteFile(regular, []byte("x"), 0o600)
	if err := replaceRelativeSymlink(regular, "x"); err == nil {
		t.Fatal("regular binding accepted")
	}
}

func TestHookObservation(t *testing.T) {
	if stop := startHookObservation(context.Background(), compiledConfig{}, func(domain.TelemetryEvent) {}); stop == nil {
		t.Fatal("empty hook stop missing")
	} else {
		stop()
	}
	hook := filepath.Join(t.TempDir(), "hooks.jsonl")
	if err := os.WriteFile(hook, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stop := startHookObservation(context.Background(), compiledConfig{HookFile: hook}, func(domain.TelemetryEvent) {})
	stop()
	if _, err := createBwrap("x", t.TempDir(), domain.SandboxProfile{}, SandboxRequirements{}); err == nil {
		t.Log("bwrap is installed; creation branch covered")
	}
}

func TestCheckpointTransferRoundTripsAndErrors(t *testing.T) {
	var pool CheckpointTransfers
	ctx := context.Background()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600)
	export := pool.Handle(ctx, root, domain.RunnerWorkspaceRequest{RunID: "run", Operation: domain.RunnerWorkspaceOperationCheckpointExport})
	var info domain.WorkspaceCheckpointTransfer
	if export.Status != 200 || json.Unmarshal(export.Payload, &info) != nil || info.ID == "" {
		t.Fatalf("export=%+v info=%+v", export, info)
	}
	readInfo := info
	readInfo.Offset = 0
	read := pool.Handle(ctx, root, domain.RunnerWorkspaceRequest{RunID: "run", Operation: domain.RunnerWorkspaceOperationCheckpointRead, CheckpointTransfer: &readInfo})
	if read.Status != 200 {
		t.Fatalf("read=%+v", read)
	}
	closeResponse := pool.Handle(ctx, root, domain.RunnerWorkspaceRequest{RunID: "run", Operation: domain.RunnerWorkspaceOperationCheckpointClose, CheckpointTransfer: &info})
	if closeResponse.Status != 200 {
		t.Fatalf("close=%+v", closeResponse)
	}
	if response := pool.Handle(ctx, root, domain.RunnerWorkspaceRequest{RunID: "run", Operation: domain.RunnerWorkspaceOperationCheckpointRead}); response.Status != 409 {
		t.Fatalf("missing transfer=%+v", response)
	}

	content := EmptyWorkspaceCheckpoint()
	sum := sha256.Sum256(content)
	uploadInfo := domain.WorkspaceCheckpointTransfer{Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
	target := filepath.Join(t.TempDir(), "restored")
	upload := pool.Handle(ctx, target, domain.RunnerWorkspaceRequest{RunID: "run", Operation: domain.RunnerWorkspaceOperationCheckpointUpload, CheckpointTransfer: &uploadInfo})
	if upload.Status != 200 || json.Unmarshal(upload.Payload, &uploadInfo) != nil || uploadInfo.ID == "" {
		t.Fatalf("upload=%+v info=%+v", upload, uploadInfo)
	}
	chunk := domain.WorkspaceCheckpointTransfer{ID: uploadInfo.ID, Offset: 0, Data: content}
	appendResponse := pool.Handle(ctx, target, domain.RunnerWorkspaceRequest{RunID: "run", Operation: domain.RunnerWorkspaceOperationCheckpointAppend, CheckpointTransfer: &chunk})
	if appendResponse.Status != 200 {
		t.Fatalf("append=%+v", appendResponse)
	}
	commit := domain.WorkspaceCheckpointTransfer{ID: uploadInfo.ID}
	commitResponse := pool.Handle(ctx, target, domain.RunnerWorkspaceRequest{RunID: "run", Operation: domain.RunnerWorkspaceOperationCheckpointCommit, CheckpointTransfer: &commit})
	if commitResponse.Status != 200 {
		t.Fatalf("commit=%+v", commitResponse)
	}
	bad := domain.WorkspaceCheckpointTransfer{Size: -1, SHA256: "bad"}
	if response := pool.Handle(ctx, target, domain.RunnerWorkspaceRequest{RunID: "run", Operation: domain.RunnerWorkspaceOperationCheckpointUpload, CheckpointTransfer: &bad}); response.Status != 409 {
		t.Fatalf("bad upload=%+v", response)
	}
	pool.Close()
}
