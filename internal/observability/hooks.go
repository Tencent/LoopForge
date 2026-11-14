package observability

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/config"
	"agentflow/internal/domain"
)

const (
	AdapterLive        = "anthropic-stream-v1"
	AdapterHooks       = "agent-hooks-v1"
	AdapterLegacyHooks = "codebuddy-hooks-v1"
	AdapterOTEL        = "codebuddy-otel-v1"
)

const hookPython = `#!/usr/bin/env python3
import json, os, sys, time
raw = sys.stdin.read()
try:
    payload = json.loads(raw) if raw.strip() else {}
except Exception:
    payload = {"raw": raw[:4000]}
if not isinstance(payload, dict):
    payload = {"payload": payload}
payload["_ts"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
path = os.environ.get("AGENTFLOW_HOOK_OUTPUT") or os.path.join(os.path.dirname(os.path.abspath(__file__)), "hooks.ndjson")
os.makedirs(os.path.dirname(path), exist_ok=True)
with open(path, "a", encoding="utf-8") as f:
    f.write(json.dumps(payload, ensure_ascii=False) + "\n")
`

func WriteCodeBuddyHookBridge(workspace string) error {
	return WriteCodeBuddyHookBridgeAt(workspace, filepath.Join(".agentflow", "observability"))
}

func WriteCodeBuddyHookBridgeAt(workspace, relativeRoot string) error {
	root := filepath.Join(workspace, relativeRoot)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	script := filepath.Join(root, "hook.py")
	if err := writeFileIfChanged(script, []byte(hookPython), 0o755); err != nil {
		return err
	}
	cmd := codeBuddyHookCommand(filepath.Join(relativeRoot, "hook.py"))
	hook := func(timeout int) map[string]any {
		return map[string]any{"hooks": []map[string]any{{"type": "command", "command": cmd, "timeout": timeout}}}
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"SessionStart":     []any{hook(5)},
			"UserPromptSubmit": []any{hook(5)},
			"PreToolUse":       []any{map[string]any{"matcher": "", "hooks": []map[string]any{{"type": "command", "command": cmd, "timeout": 8}}}},
			"PostToolUse":      []any{map[string]any{"matcher": "", "hooks": []map[string]any{{"type": "command", "command": cmd, "timeout": 10}}}},
			"Stop":             []any{hook(8)},
		},
	}
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return writeFileIfChanged(filepath.Join(workspace, filepath.Dir(relativeRoot), "codebuddy-settings.json"), raw, 0o600)
}

// writeFileIfChanged leaves an identical file untouched and otherwise
// replaces it through a rename: a CLI of a parallel branch may be executing
// hook.py or reading its settings at this moment and must never observe a
// truncated file.
func writeFileIfChanged(path string, data []byte, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm() == mode.Perm() {
		if existing, err := os.ReadFile(path); err == nil && string(existing) == string(data) {
			return nil
		}
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// codeBuddyHookCommand returns a PreToolUse-safe shell command that recreates
// hook.py when an agent git stash/clean removes the untracked runtime file.
func codeBuddyHookCommand(relativeScriptPath string) string {
	script := filepath.ToSlash(relativeScriptPath)
	encoded := base64.StdEncoding.EncodeToString([]byte(hookPython))
	return fmt.Sprintf(
		`bash -lc 'H=%s; if [ ! -f "$H" ]; then mkdir -p "$(dirname "$H")" && echo %s | base64 -d > "$H" && chmod +x "$H"; fi; exec python3 "$H"'`,
		script,
		encoded,
	)
}

func HookFile(workspace string) string {
	return filepath.Join(workspace, ".agentflow", "observability", "hooks.ndjson")
}

func HookFileAt(workspace, relativeRoot string) string {
	return filepath.Join(workspace, relativeRoot, "hooks.ndjson")
}

func HookEvent(raw []byte) (domain.TelemetryEvent, bool) {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return domain.TelemetryEvent{}, false
	}
	name := firstString(payload, "hook_event_name", "hookEventName", "event")
	tool := firstString(payload, "tool_name", "toolName")
	category, eventName, phase, msg := hookLifecycle(name, tool, payload)
	provider := firstString(payload, "_agentflow_provider")
	if provider == "" {
		provider = "codebuddy"
	}
	exec := readHookExecution(payload)
	if exec.reported() {
		category, eventName, phase = "hook", "hook."+exec.phase, exec.phase
		msg = exec.summary(name)
	} else if exec.name != "" {
		msg = exec.name + " · " + msg
	}
	level := ""
	if exec.failed() {
		level = "error"
	}
	return domain.TelemetryEvent{
		Category:   category,
		Name:       eventName,
		Phase:      phase,
		Level:      level,
		Message:    clip(msg, 180),
		SessionID:  firstString(payload, "session_id", "sessionId"),
		ToolCallID: firstString(payload, "tool_use_id", "toolUseId"),
		Source:     domain.TelemetrySource{Layer: "executor", Adapter: AdapterHooks, Provider: provider, Protocol: "hooks"},
		Attributes: hookAttributes(payload, name, tool, exec),
	}, true
}

// hookLifecycle maps the agent hook event name to the telemetry category,
// event name, phase and default message.
func hookLifecycle(name, tool string, payload map[string]any) (category, eventName, phase, msg string) {
	msg = strings.TrimSpace(name)
	category, eventName, phase = "lifecycle", "hook."+strings.ToLower(name), "progress"
	switch strings.ToLower(name) {
	case "sessionstart":
		msg, eventName, phase = "hook: session start", "agent.initialized", "start"
	case "userpromptsubmit":
		msg, eventName = "hook: prompt submitted", "agent.prompt"
	case "pretooluse":
		category, eventName, phase = "tool", "tool.call", "start"
		msg = summarizeHookTool(tool, payload["tool_input"], payload["toolInput"], false)
	case "posttooluse":
		category, eventName, phase = "tool", "tool.call", "finish"
		msg = summarizeHookTool(tool, payload["tool_input"], payload["toolInput"], true)
	case "stop":
		msg, eventName, phase = "hook: session stop", "agent.completed", "finish"
	}
	if msg == "" {
		msg = "hook event"
	}
	return category, eventName, phase, msg
}

// hookExecution is the platform-injected record of one configured hook run
// (the _agentflow_* payload fields).
type hookExecution struct {
	id, name, phase string
	exitCode        int64
	hasExitCode     bool
	durationMS      int64
	hasDuration     bool
}

func readHookExecution(payload map[string]any) hookExecution {
	exec := hookExecution{
		id:    firstString(payload, "_agentflow_hook_id"),
		name:  firstString(payload, "_agentflow_hook_name"),
		phase: firstString(payload, "_agentflow_execution_phase"),
	}
	exec.exitCode, exec.hasExitCode = firstInt64(payload, "_agentflow_exit_code")
	exec.durationMS, exec.hasDuration = firstInt64(payload, "_agentflow_duration_ms")
	return exec
}

// reported reports whether the payload describes the start or finish of a
// configured hook rather than a plain agent lifecycle event.
func (h hookExecution) reported() bool {
	return h.id != "" && (h.phase == "start" || h.phase == "finish")
}

func (h hookExecution) failed() bool { return h.hasExitCode && h.exitCode != 0 }

// summary renders "label · event · status[ · N ms]" for a reported hook run.
func (h hookExecution) summary(event string) string {
	label := h.name
	if label == "" {
		label = h.id
	}
	if h.phase == "start" {
		return label + " · " + event + " · 开始"
	}
	status := "成功"
	if h.failed() {
		status = fmt.Sprintf("失败（exit %d）", h.exitCode)
	}
	msg := label + " · " + event + " · " + status
	if h.hasDuration {
		msg += fmt.Sprintf(" · %d ms", h.durationMS)
	}
	return msg
}

func hookAttributes(payload map[string]any, name, tool string, exec hookExecution) map[string]any {
	attributes := map[string]any{"hook": name, "tool": tool}
	if exec.id != "" {
		attributes["hookId"] = exec.id
	}
	if exec.name != "" {
		attributes["hookName"] = exec.name
	}
	for source, target := range map[string]string{
		"_agentflow_execution_phase": "executionPhase",
		"_agentflow_matcher":         "matcher",
		"_agentflow_language":        "language",
		"_agentflow_stdout":          "stdout",
		"_agentflow_stderr":          "stderr",
		"_agentflow_repository_id":   "repositoryId",
		"_agentflow_repository_name": "repositoryName",
		"_agentflow_repository_dir":  "repositoryDir",
	} {
		if value := firstString(payload, source); value != "" {
			attributes[target] = clip(value, 2000)
		}
	}
	if exec.hasExitCode {
		attributes["exitCode"] = exec.exitCode
	}
	if exec.hasDuration {
		attributes["durationMs"] = exec.durationMS
	}
	return attributes
}

func firstInt64(m map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		switch value := m[key].(type) {
		case float64:
			return int64(value), true
		case int64:
			return value, true
		case int:
			return int64(value), true
		}
	}
	return 0, false
}

func summarizeHookTool(tool string, inputA, inputB any, done bool) string {
	label := strings.TrimSpace(tool)
	if label == "" {
		label = "tool"
	}
	detail := hookToolDetail(inputA)
	if detail == "" {
		detail = hookToolDetail(inputB)
	}
	if done {
		if detail == "" {
			return label + " 完成"
		}
		return clip(label+" 完成: "+detail, 180)
	}
	if detail == "" {
		return label
	}
	return clip(label+" "+detail, 180)
}

func hookToolDetail(input any) string {
	m, _ := input.(map[string]any)
	if m == nil {
		return ""
	}
	for _, k := range []string{"command", "cmd", "path", "file_path", "filePath", "pattern", "glob", "query"} {
		if s, _ := m[k].(string); strings.TrimSpace(s) != "" {
			return strings.Join(strings.Fields(s), " ")
		}
	}
	return ""
}

func WatchHookFile(ctx context.Context, path string, emit func(domain.TelemetryEvent)) {
	if path == "" || emit == nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	var offset int64
	drain := func() {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		if offset > 0 {
			if _, err := f.Seek(offset, 0); err != nil {
				offset = 0
				_, _ = f.Seek(0, 0)
			}
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			if event, ok := HookEvent([]byte(line)); ok {
				emit(event)
			}
		}
		if info, err := f.Stat(); err == nil {
			offset = info.Size()
		}
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	drain()
	for {
		select {
		case <-ctx.Done():
			drain()
			return
		case <-ticker.C:
			drain()
		}
	}
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func MatchAdapter(event domain.TelemetryEvent, view string) bool {
	adapter := event.Source.Adapter
	switch strings.TrimSpace(view) {
	case "", "all":
		return true
	case "hooks":
		return adapter == AdapterHooks || adapter == AdapterLegacyHooks
	case "otel":
		return adapter == AdapterOTEL
	case "live":
		return adapter != AdapterHooks && adapter != AdapterLegacyHooks && adapter != AdapterOTEL
	default:
		return adapter == view
	}
}

func CollectorTracesURL() string {
	if v := strings.TrimSpace(os.Getenv("AGENTFLOW_OTEL_TRACES_ENDPOINT")); v != "" {
		return v
	}
	base := strings.TrimSpace(os.Getenv("AGENTFLOW_OTEL_ENDPOINT"))
	if base == "" {
		base = config.Current().CollectorBase + "/api/otel"
	}
	return strings.TrimRight(base, "/") + "/v1/traces"
}

func InjectCodeBuddyEnv(env map[string]string, runID, nodeID string) {
	if env == nil {
		return
	}
	env["AGENTFLOW_RUN_ID"] = runID
	env["AGENTFLOW_NODE_ID"] = nodeID
	env["CODEBUDDY_CODE_ENABLE_TELEMETRY"] = "1"
	env["CLAUDE_CODE_ENABLE_TELEMETRY"] = "1"
	env["OTEL_TRACES_EXPORTER"] = "otlp"
	env["OTEL_EXPORTER_OTLP_PROTOCOL"] = "http/protobuf"
	env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] = CollectorTracesURL()
	env["OTEL_LOG_TOOL_DETAILS"] = "1"
	attrs := fmt.Sprintf("agentflow.run_id=%s,agentflow.node_id=%s,service.name=agentflow-codebuddy", runID, nodeID)
	env["OTEL_RESOURCE_ATTRIBUTES"] = attrs
}
