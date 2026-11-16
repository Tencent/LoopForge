package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/scriptview"
)

// A copy of this test executable records Docker argv without a daemon or a
// shell wrapper. This exercises the same Windows process boundary as Docker.exe.
func init() {
	if os.Getenv("LOOPFORGE_WINDOWS_DOCKER_HELPER") != "1" {
		return
	}
	file, err := os.OpenFile(os.Getenv("LOOPFORGE_WINDOWS_DOCKER_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(2)
	}
	_ = json.NewEncoder(file).Encode(os.Args[1:])
	_ = file.Close()
	joined := strings.Join(os.Args[1:], "\n")
	switch {
	case len(os.Args) > 1 && os.Args[1] == "inspect":
		switch {
		case strings.Contains(joined, "{{json .}}"):
			fmt.Println(`{"Id":"fixture-container","Image":"fixture-image","State":{"Running":true,"StartedAt":"now"}}`)
		case strings.Contains(joined, "{{json .Mounts}}"):
			fmt.Println("[]")
		default:
			fmt.Fprintln(os.Stderr, "Error: No such container")
			os.Exit(1)
		}
	case strings.Contains(joined, "{{.Architecture}}"):
		fmt.Println("amd64")
	case containsArg(os.Args, scriptview.ProbeArgument):
		fmt.Println(scriptview.Version)
	case strings.Contains(joined, "id -u; id -g"):
		fmt.Print("0\n0\n")
	case strings.Contains(joined, "loopforge-cli-setup"):
		fmt.Println("/usr/bin/codebuddy")
	case strings.Contains(joined, dockerOwnerLeaseStart):
		fmt.Println("LOOPFORGE_OWNER_LEASE_READY")
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
		}
	case containsArg(os.Args, "-i") && strings.Contains(joined, dockerConversationProcessSupervisor):
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Print(line)
	default:
		fmt.Println("/workspace")
	}
	os.Exit(0)
}

func installWindowsDockerRecorder(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker.exe"), self, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "docker.jsonl")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LOOPFORGE_WINDOWS_DOCKER_HELPER", "1")
	t.Setenv("LOOPFORGE_WINDOWS_DOCKER_LOG", log)
	t.Setenv("AGENTFLOW_CONVERSATION_RELAY_HOST", "host.docker.internal")
	t.Setenv("AGENTFLOW_CONVERSATION_DOCKER_NETWORK", "fixture-network")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", "")
	t.Setenv("AGENTFLOW_RUNTIME_TOOLCHAIN_DIR", "")
	gate := filepath.Join(bin, "agentflow-gate-mcp-linux-amd64")
	if err := os.WriteFile(gate, []byte("fake Docker fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", gate)
	return log
}

func readWindowsDockerCalls(t *testing.T, log string) [][]string {
	t.Helper()
	file, err := os.Open(log)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var calls [][]string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var call []string
		if err := json.Unmarshal(scanner.Bytes(), &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return calls
}

func TestWindowsDockerConversationSeparatesHostAndContainerPaths(t *testing.T) {
	log := installWindowsDockerRecorder(t)
	root := filepath.Join(t.TempDir(), "conversation")
	workspace := filepath.Join(root, "ws")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("repository policy"), 0600); err != nil {
		t.Fatal(err)
	}
	sandbox := &dockerSandbox{name: "windows-conversation", workspace: workspace,
		requirements: SandboxRequirements{ConversationRoot: root}, dockerfile: domain.DockerfileDefinition{Content: "FROM fixture"}}
	masks, err := sandbox.currentAgentMasks()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(masks, []string{"/workspace/AGENTS.md", "/loopforge/conversation/ws/AGENTS.md"}) {
		t.Fatalf("conversation masks must use Linux execution paths: %v", masks)
	}
	if err := sandbox.ensureStarted(context.Background()); err != nil {
		t.Fatal(err)
	}
	command := WithCommandEnvironment(CommandSpec{Argv: []string{"codex", "--settings", filepath.Join(root, "current-settings.json")}, WorkingDir: workspace, OwnProcessGroup: true},
		map[string]string{"HOME": filepath.Join(root, "home"), "TMPDIR": filepath.Join(root, "home", "tmp")})
	if _, err := sandbox.Exec(context.Background(), command, nil); err != nil {
		t.Fatal(err)
	}
	var create, execute, stop []string
	for _, call := range readWindowsDockerCalls(t, log) {
		if call[0] == "create" {
			create = call
		}
		joined := strings.Join(call, "\n")
		if strings.Contains(joined, dockerConversationProcessSupervisor) {
			execute = call
		}
		if strings.Contains(joined, dockerConversationProcessStop) {
			stop = call
		}
	}
	if create == nil || execute == nil || stop == nil {
		t.Fatalf("missing create/start/stop calls: create=%v execute=%v stop=%v", create, execute, stop)
	}
	joinedCreate := strings.Join(create, "\n")
	for _, mount := range []string{root + ":/loopforge/conversation:ro", filepath.Join(root, "home") + ":/loopforge/conversation/home", workspace + ":/loopforge/conversation/ws",
		filepath.Join(root, "home", ".cursor", "hooks.json") + ":/loopforge/conversation/home/.cursor/hooks.json:ro"} {
		if !strings.Contains(joinedCreate, mount) {
			t.Errorf("missing mount %q in %v", mount, create)
		}
	}
	if containsArg(create, "--user") || strings.Contains(joinedCreate, "dst=C:") || strings.Contains(joinedCreate, "dst=\\") {
		t.Fatalf("Windows host identity/path leaked into container options: %v", create)
	}
	for _, value := range []string{"HOME=/loopforge/conversation/home", "TMPDIR=/loopforge/conversation/home/tmp", "/loopforge/conversation/ws", "/loopforge/conversation/current-settings.json"} {
		if !containsArg(execute, value) {
			t.Errorf("missing execution path %q in %v", value, execute)
		}
	}
	if strings.Contains(strings.Join(execute, "\n"), root) {
		t.Fatalf("native host root reached Linux process: %v", execute)
	}
	for _, call := range [][]string{execute, stop} {
		found := false
		for _, arg := range call {
			if strings.HasPrefix(arg, "/loopforge/conversation/home/tmp/process-") && strings.HasSuffix(arg, ".pid") {
				found = true
			}
		}
		if !found {
			t.Fatalf("supervisor PID file is not a container path: %v", call)
		}
	}
	if command.WorkingDir != workspace || command.environment["HOME"] != filepath.Join(root, "home") {
		t.Fatal("container translation mutated the caller's host command")
	}
}

func TestWindowsDockerConversationAssetsAndApprovalUseExecutionRoot(t *testing.T) {
	root := t.TempDir()
	const source = "/platform/conversation"
	pool := &ConversationProcessPool{}
	spec := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{Driver: "docker"}}
	_, err := pool.Handle(context.Background(), root, "", spec, ConversationProcessRequest{Operation: "sync", SourceRoot: source,
		Files: []ConversationFile{{Path: "current-settings.json", Rewrite: true, Mode: 0600, Data: []byte(`{"cwd":"` + source + `/ws"}`)}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "current-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]string
	if err := json.Unmarshal(data, &config); err != nil || config["cwd"] != "/loopforge/conversation/ws" {
		t.Fatalf("container JSON settings = %s: %v", data, err)
	}
	command, err := installConversationApproval(root, "/loopforge/conversation", "codebuddy", CommandSpec{Argv: []string{"codebuddy", "prompt"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(command.Argv, []string{"codebuddy", "--settings", "/loopforge/conversation/native-settings.json", "prompt"}) {
		t.Fatalf("approval argv = %v", command.Argv)
	}
	settings, err := os.ReadFile(filepath.Join(root, "native-settings.json"))
	if err != nil || !strings.Contains(string(settings), "/loopforge/conversation/native-approval.py") || strings.Contains(string(settings), filepath.ToSlash(root)) {
		t.Fatalf("approval settings reference host paths: %s: %v", settings, err)
	}
}

func TestWindowsDockerScriptHelperSetsContainerExecuteMode(t *testing.T) {
	log := installWindowsDockerRecorder(t)
	sandbox := &dockerSandbox{name: "windows-script-mode", workspace: t.TempDir(), started: true}
	if err := sandbox.ensureScriptContainer(context.Background()); err != nil {
		t.Fatal(err)
	}
	copyIndex, startIndex, modeIndex, probeIndex := -1, -1, -1, -1
	name := dockerScriptContainerName(sandbox.name)
	for index, call := range readWindowsDockerCalls(t, log) {
		switch {
		case call[0] == "cp":
			copyIndex = index
		case reflect.DeepEqual(call, []string{"start", name}):
			startIndex = index
		case reflect.DeepEqual(call, []string{"exec", "--user", "0:0", name, "chmod", "0755", scriptViewBinary}):
			modeIndex = index
		case containsArg(call, scriptview.ProbeArgument):
			probeIndex = index
		}
	}
	if copyIndex < 0 || startIndex <= copyIndex || modeIndex <= startIndex || probeIndex <= modeIndex {
		t.Fatalf("Windows helper must be copied, started, chmodded as root, then probed: cp=%d start=%d chmod=%d probe=%d", copyIndex, startIndex, modeIndex, probeIndex)
	}
}

func TestWindowsDockerConversationStdinUsesExecutionRoot(t *testing.T) {
	installWindowsDockerRecorder(t)
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "fixture-image")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	sandbox := &dockerSandbox{name: "conversation-stdin", workspace: workspace, started: true, requirements: SandboxRequirements{ConversationRoot: root}, dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	spec := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{Driver: "docker"}}
	pool := &ConversationProcessPool{}
	const source = "/platform/conversation"
	started, err := pool.Handle(ctx, root, workspace, spec, ConversationProcessRequest{Operation: "start", SourceRoot: source,
		Command: &ConversationCommand{Provider: "codebuddy", Argv: []string{"codebuddy", "prompt"}, Interactive: true}}, nil,
		func(context.Context, string) (Sandbox, func(), error) { return sandbox, func() {}, nil })
	if err != nil {
		t.Fatal(err)
	}
	input := `{"cwd":"` + source + `/ws"}` + "\n"
	if _, err := pool.Handle(ctx, root, workspace, spec, ConversationProcessRequest{Operation: "write", ID: started.ID, SourceRoot: source, Data: input}, nil); err != nil {
		t.Fatal(err)
	}
	output := conversationPoll(t, pool, ctx, root, started.ID)
	var frame map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &frame); err != nil || frame["cwd"] != "/loopforge/conversation/ws" {
		t.Fatalf("container stdin frame = %q: %v", output, err)
	}
}
