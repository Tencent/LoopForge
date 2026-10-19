package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/observability"
)

type compiledHook struct {
	Event      string
	Matcher    string
	Command    string
	TimeoutSec int
}

func hookExecutionEnvironment(repositories []domain.RepositoryContext) map[string]string {
	type target struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Dir  string `json:"dir"`
	}
	targets := make([]target, 0, len(repositories))
	for _, repository := range repositories {
		targets = append(targets, target{ID: repository.RepositoryID, Name: repository.Name, Dir: repository.WorkspaceDir})
	}
	raw, _ := json.Marshal(targets)
	return map[string]string{"AGENTFLOW_REPOSITORIES_JSON": string(raw)}
}

// mergeAgentHookDefinitions is the single merge boundary for every provider.
// System hooks always run before role hooks. IDs are unique across both sources
// so a role cannot accidentally replace a provider-owned hook.
func mergeAgentHookDefinitions(systemHooks, roleHooks []domain.AgentHook) ([]domain.AgentHook, error) {
	merged := make([]domain.AgentHook, 0, len(systemHooks)+len(roleHooks))
	seen := make(map[string]string, len(systemHooks)+len(roleHooks))
	appendHooks := func(source string, hooks []domain.AgentHook) error {
		for _, hook := range hooks {
			id := strings.TrimSpace(hook.ID)
			if previous, exists := seen[id]; exists && id != "" {
				return fmt.Errorf("hook id %q conflicts between %s and %s hooks", id, previous, source)
			}
			if id != "" {
				seen[id] = source
			}
			merged = append(merged, hook)
		}
		return nil
	}
	if err := appendHooks("system", systemHooks); err != nil {
		return nil, err
	}
	if err := appendHooks("role", roleHooks); err != nil {
		return nil, err
	}
	return merged, nil
}

func startHookObservation(ctx context.Context, compiled compiledConfig, sink EventSink) context.CancelFunc {
	if compiled.HookFile == "" || sink == nil {
		return func() {}
	}
	hookCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		observability.WatchHookFile(hookCtx, compiled.HookFile, sink)
	}()
	return func() {
		cancel()
		<-done
	}
}

func materializeAgentHooks(configRoot, agentRel, provider string, hooks []domain.AgentHook) ([]compiledHook, error) {
	if len(hooks) == 0 {
		return nil, nil
	}
	root := filepath.Join(configRoot, "hooks")
	if err := os.Mkdir(root, 0o700); err != nil {
		return nil, err
	}
	compiled := make([]compiledHook, 0, len(hooks))
	for _, hook := range hooks {
		ext := ".sh"
		interpreter := "sh"
		if hook.Language == "python" {
			ext = ".py"
			interpreter = "python3"
		}
		name := safeID(hook.ID)
		if name == "" {
			return nil, fmt.Errorf("invalid hook id %q", hook.ID)
		}
		file := filepath.Join(root, name+ext)
		body := hook.Script
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		if err := writeManagedFile(file, []byte(body), 0o700); err != nil {
			return nil, err
		}
		wrapperName := name + "-agentflow.py"
		wrapper := hookWrapperPython(name+ext, interpreter, provider, hook)
		if err := writeManagedFile(filepath.Join(root, wrapperName), []byte(wrapper), 0o700); err != nil {
			return nil, err
		}
		rel := filepath.ToSlash(filepath.Join(agentRel, "..", "config", filepath.Base(configRoot), "hooks", wrapperName))
		compiled = append(compiled, compiledHook{
			Event:      hook.Event,
			Matcher:    hook.Matcher,
			Command:    "python3 " + shellSingleQuote(rel),
			TimeoutSec: hook.TimeoutSec,
		})
	}
	return compiled, nil
}

func hookWrapperPython(scriptName, interpreter, provider string, hook domain.AgentHook) string {
	return `#!/usr/bin/env python3
import json, os, shutil, subprocess, sys, tempfile, time
raw = sys.stdin.buffer.read()
try:
    payload = json.loads(raw.decode("utf-8")) if raw.strip() else {}
except Exception:
    payload = {"raw": raw[:4000].decode("utf-8", errors="replace")}
if not isinstance(payload, dict):
    payload = {"payload": payload}
payload["_agentflow_hook_id"] = ` + strconv.Quote(hook.ID) + `
payload["_agentflow_hook_name"] = ` + strconv.Quote(hook.Name) + `
payload["_agentflow_provider"] = ` + strconv.Quote(provider) + `
payload["_agentflow_matcher"] = ` + strconv.Quote(hook.Matcher) + `
payload["_agentflow_language"] = ` + strconv.Quote(hook.Language) + `
output = os.environ.get("AGENTFLOW_HOOK_OUTPUT", "")
def emit(phase, duration_ms=None, exit_code=None, stdout="", stderr="", target=None):
    if not output:
        return
    record = dict(payload)
    record["_agentflow_execution_phase"] = phase
    record["_ts"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    if duration_ms is not None:
        record["_agentflow_duration_ms"] = duration_ms
    if exit_code is not None:
        record["_agentflow_exit_code"] = exit_code
    if stdout:
        record["_agentflow_stdout"] = stdout
    if stderr:
        record["_agentflow_stderr"] = stderr
    if target:
        record["_agentflow_repository_id"] = str(target.get("id", ""))
        record["_agentflow_repository_name"] = str(target.get("name", ""))
        record["_agentflow_repository_dir"] = str(target.get("dir", ""))
    parent = os.path.dirname(output)
    if parent:
        os.makedirs(parent, exist_ok=True)
    with open(output, "a", encoding="utf-8") as stream:
        stream.write(json.dumps(record, ensure_ascii=False) + "\n")
def preview(stream, limit=2000):
    stream.flush()
    stream.seek(0)
    return stream.read(limit).decode("utf-8", errors="replace").strip()
script = os.path.join(os.path.dirname(os.path.abspath(__file__)), ` + strconv.Quote(scriptName) + `)
scope = ` + strconv.Quote(normalizeHookExecutionScope(hook.ExecutionScope)) + `
workspace = os.getcwd()
targets = [{}]
if scope == "repositories":
    try:
        targets = json.loads(os.environ.get("AGENTFLOW_REPOSITORIES_JSON", "[]"))
    except Exception:
        targets = []
    if not targets:
        message = "repository-scoped hook requires at least one bound repository"
        emit("finish", 0, 2, "", message)
        print(message, file=sys.stderr)
        raise SystemExit(2)
result = None
for target in targets:
    emit("start", target=target)
    started = time.monotonic()
    with tempfile.TemporaryFile() as stdout_buffer, tempfile.TemporaryFile() as stderr_buffer:
        env = os.environ.copy()
        env["AGENTFLOW_WORKSPACE"] = workspace
        env["AGENTFLOW_EXECUTION_SCOPE"] = scope
        af_bin = env.get("AGENTFLOW_BIN") or ""
        if af_bin and os.path.isfile(af_bin) and os.access(af_bin, os.X_OK):
            env["PATH"] = os.path.dirname(os.path.abspath(af_bin)) + os.pathsep + env.get("PATH", "")
        cwd = workspace
        if scope == "repositories":
            directory = str(target.get("dir", ""))
            env["AGENTFLOW_REPOSITORY_ID"] = str(target.get("id", ""))
            env["AGENTFLOW_REPOSITORY_NAME"] = str(target.get("name", ""))
            env["AGENTFLOW_REPOSITORY_DIR"] = directory
            if directory:
                cwd = os.path.join(workspace, directory)
        try:
            result = subprocess.run([` + strconv.Quote(interpreter) + `, script], input=raw, stdout=stdout_buffer, stderr=stderr_buffer, cwd=cwd, env=env)
        except Exception as error:
            duration_ms = int((time.monotonic() - started) * 1000)
            emit("finish", duration_ms, 127, "", str(error), target)
            print(str(error), file=sys.stderr)
            raise
        duration_ms = int((time.monotonic() - started) * 1000)
        stdout_preview = preview(stdout_buffer)
        stderr_preview = preview(stderr_buffer)
        emit("finish", duration_ms, result.returncode, stdout_preview, stderr_preview, target)
        if scope == "run":
            stdout_buffer.seek(0)
            stderr_buffer.seek(0)
            shutil.copyfileobj(stdout_buffer, sys.stdout.buffer)
            shutil.copyfileobj(stderr_buffer, sys.stderr.buffer)
        elif result.returncode != 0:
            stderr_buffer.seek(0)
            shutil.copyfileobj(stderr_buffer, sys.stderr.buffer)
    if result.returncode != 0:
        break
raise SystemExit(result.returncode)
`
}

func normalizeHookExecutionScope(scope string) string {
	if strings.EqualFold(strings.TrimSpace(scope), domain.ExecutionScopeRepositories) {
		return domain.ExecutionScopeRepositories
	}
	return domain.ExecutionScopeRun
}

func hookSettings(hooks []compiledHook) map[string]any {
	events := map[string][]any{}
	for _, hook := range hooks {
		group := map[string]any{
			"hooks": []map[string]any{{"type": "command", "command": hook.Command, "timeout": hook.TimeoutSec}},
		}
		if hook.Matcher != "" {
			group["matcher"] = hook.Matcher
		}
		events[hook.Event] = append(events[hook.Event], group)
	}
	return map[string]any{"hooks": events}
}

func writeHookSettings(path string, hooks []compiledHook) error {
	raw, err := json.MarshalIndent(hookSettings(hooks), "", "  ")
	if err != nil {
		return err
	}
	return writeManagedFile(path, raw, 0o600)
}

func mergeHookSettings(path string, hooks []compiledHook) error {
	if len(hooks) == 0 {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return err
	}
	events, _ := settings["hooks"].(map[string]any)
	if events == nil {
		events = map[string]any{}
		settings["hooks"] = events
	}
	userEvents := hookSettings(hooks)["hooks"].(map[string][]any)
	for event, groups := range userEvents {
		existing, _ := events[event].([]any)
		events[event] = append(existing, groups...)
	}
	merged, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return writeManagedFile(path, merged, 0o600)
}

func codexHookConfigArgs(hooks []compiledHook) []string {
	if len(hooks) == 0 {
		return nil
	}
	grouped := map[string][]compiledHook{}
	var order []string
	for _, hook := range hooks {
		if _, ok := grouped[hook.Event]; !ok {
			order = append(order, hook.Event)
		}
		grouped[hook.Event] = append(grouped[hook.Event], hook)
	}
	args := make([]string, 0, len(order)*2)
	for _, event := range order {
		groups := make([]string, 0, len(grouped[event]))
		for _, hook := range grouped[event] {
			group := "{hooks=[{type=\"command\",command=" + strconv.Quote(hook.Command) + ",timeout=" + strconv.Itoa(hook.TimeoutSec) + "}]"
			if hook.Matcher != "" {
				group += ",matcher=" + strconv.Quote(hook.Matcher)
			}
			groups = append(groups, group+"}")
		}
		args = append(args, "-c", "hooks."+event+"=["+strings.Join(groups, ",")+"]")
	}
	return args
}
