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
	if err := os.WriteFile(script, []byte(hookPython), 0o755); err != nil {
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
	return os.WriteFile(filepath.Join(workspace, filepath.Dir(relativeRoot), "codebuddy-settings.json"), raw, 0o600)
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
	msg := strings.TrimSpace(name)
	category, eventName, phase := "lifecycle", "hook."+strings.ToLower(name), "progress"
	switch strings.ToLower(name) {
	case "sessionstart":
		msg, eventName, phase = "hook: session start", "agent.initialized", "start"
	case "userpromptsubmit":
		msg, category, eventName = "hook: prompt submitted", "lifecycle", "agent.prompt"
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
	provider := firstString(payload, "_agentflow_provider")
	if provider == "" {
		provider = "codebuddy"
	}
	hookID := firstString(payload, "_agentflow_hook_id")
	hookName := firstString(payload, "_agentflow_hook_name")
	executionPhase := firstString(payload, "_agentflow_execution_phase")
	exitCode, hasExitCode := firstInt64(payload, "_agentflow_exit_code")
	durationMS, hasDuration := firstInt64(payload, "_agentflow_duration_ms")
	if hookID != "" && (executionPhase == "start" || executionPhase == "finish") {
		category, eventName, phase = "hook", "hook."+executionPhase, executionPhase
		label := hookName
		if label == "" {
			label = hookID
		}
		if executionPhase == "start" {
			msg = label + " · " + name + " · 开始"
		} else {
			status := "成功"
			if hasExitCode && exitCode != 0 {
				status = fmt.Sprintf("失败（exit %d）", exitCode)
			}
			msg = label + " · " + name + " · " + status
			if hasDuration {
				msg += fmt.Sprintf(" · %d ms", durationMS)
			}
		}
	} else if hookName != "" {
		msg = hookName + " · " + msg
	}
	attributes := map[string]any{"hook": name, "tool": tool}
	if hookID != "" {
		attributes["hookId"] = hookID
	}
	if hookName != "" {
		attributes["hookName"] = hookName
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
	if hasExitCode {
		attributes["exitCode"] = exitCode
	}
	if hasDuration {
		attributes["durationMs"] = durationMS
	}
	level := ""
	if hasExitCode && exitCode != 0 {
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
		Attributes: attributes,
	}, true
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
