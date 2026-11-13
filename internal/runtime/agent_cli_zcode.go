package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
)

const (
	zcodeConfigPath      = ".zcode/cli/config.json"
	zcodeProviderDir     = ".zcode/v2"
	zcodePersonalFileEnv = "ZCODE_PERSONAL_PROVIDER_CONFIG_FILE"
	// zcodeDefaultBaseURL is BigModel's Anthropic-compatible endpoint.
	zcodeDefaultBaseURL = "https://open.bigmodel.cn/api/anthropic"
	zcodeDefaultModel   = "glm-5.3"
	zcodeRemoveKeyFile  = `rm -f "$` + zcodePersonalFileEnv + `"`

	// zcodeSOPDisallowedTools are the tools that wait for a person.
	zcodeSOPDisallowedTools = "AskUserQuestion EnterPlanMode ExitPlanMode"
	// zcodeChatDisallowedTools adds what acts outside the chat's MCP gateway:
	// shell, edits, web, sub-agents, background tasks and the JS REPL.
	zcodeChatDisallowedTools = zcodeSOPDisallowedTools + " Bash Edit Write WebFetch Agent SendMessage TaskOutput TaskStop mcp__node_repl__js"
)

// zcodeHostEnv are host settings that would redirect ZCode's providers or
// data. ZCode treats an empty value as set and fails on it, so the wrapper
// unsets them instead of relying on the empty overrides of agentCLIHostEnv.
var zcodeHostEnv = []string{
	"ZCODE_BUILTIN_PROVIDER_CONFIG_FILE", "ZCODE_BUILTIN_PROVIDER_BUNDLED_CONFIG_FILE", "ZCODE_STORAGE_DIR", "ZCODE_SESSION_DB_PATH",
	"ZCODE_BASE_URL", "ZCODE_ENDPOINT_ORIGIN", "ZCODE_OFFICIAL_ENDPOINT", "ZCODE_OFFICIAL_ENDPOINT_TOKEN", "ZCODE_API_KEY", "ZCODE_MODEL",
}

// zcodeCleanupScript runs the CLI from "$@". The hook drops the key file once
// ZCode has read it; the trap covers a run that exits before the hook fires
// and removes the empty file ZCode recreates.
var zcodeCleanupScript = "unset " + strings.Join(zcodeHostEnv, " ") + "; trap '" + zcodeRemoveKeyFile + `' EXIT; trap 'exit 129' HUP; trap 'exit 130' INT; trap 'exit 143' TERM; "$@"`

// zcodeMCPServer renders one MCP entry for ZCode, which drops a server that
// carries an unknown or null field, so only non-empty fields are copied.
func zcodeMCPServer(entry map[string]any) map[string]any {
	if url, _ := entry["url"].(string); url != "" {
		server := map[string]any{"type": "http", "url": url}
		if nonEmpty(entry["headers"]) {
			server["headers"] = entry["headers"]
		}
		return server
	}
	server := map[string]any{"type": "stdio", "command": entry["command"], "timeoutMs": 600000, "enabled": true}
	for _, key := range []string{"args", "cwd", "env"} {
		if nonEmpty(entry[key]) {
			server[key] = entry[key]
		}
	}
	return server
}

func nonEmpty(value any) bool {
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Invalid:
		return false
	case reflect.Slice, reflect.Map, reflect.String:
		return v.Len() > 0
	}
	return true
}

// applyZCodeCredential gives ZCode the key as a personal provider file that
// exists only until ZCode has read it: ZCode accepts a literal key and
// nothing else, and the key must not stay on disk.
func applyZCodeCredential(home AgentCLIHome, homeEnv, key, baseURL, protocol, model, effort string, argv []string) ([]string, map[string]string, error) {
	// The private provider has no model of its own, so "CLI default" (sent as
	// "default") must name a real one: BigModel's coding model on the default
	// endpoint, while a custom endpoint has no model we could guess.
	if model == "" || model == "default" {
		if baseURL != "" {
			return nil, nil, fmt.Errorf("%s 需要指定模型", AgentCLILabel(AgentCLIZCode))
		}
		model = zcodeDefaultModel
	}
	apiType := map[string]string{AgentCLIProtocolChat: "openai-chat-completions", AgentCLIProtocolResponses: "openai-responses", AgentCLIProtocolMessages: "anthropic-messages"}[protocol]
	if baseURL == "" {
		baseURL, apiType = zcodeDefaultBaseURL, "anthropic-messages"
	}
	// ZCode has no effort flag: the reasoning level rides on the model
	// selection and becomes the request's output_config.effort. Unset, ZCode
	// uses the model's strongest level.
	selection := map[string]any{"providerId": "agentflow", "modelId": model}
	if effort != "" {
		selection["options"] = map[string]any{"reasoningLevel": effort}
	}
	providerConfig, err := json.MarshalIndent(map[string]any{"schemaVersion": 1, "config": map[string]any{
		"providerConfigRules": map[string]any{"providerRules": []any{map[string]any{"providerId": "agentflow", "config": map[string]any{
			"group":            "standard-personal",
			"access":           map[string]any{"type": "api-key", "apiKey": key},
			"api":              map[string]any{"type": apiType, "baseUrl": baseURL},
			"personalModelIds": []string{model},
		}}}},
		"modelConfigRules":      map[string]any{"providerModelRules": []any{}, "manualProviderModelRules": []any{}},
		"defaultModelSelection": selection,
	}}, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	// One file per call: concurrent calls in the same HOME must not delete each other's key.
	keyFile := zcodeProviderDir + "/provider_config." + newAgentCLISessionID() + ".json"
	if err := writeAgentCLIFile(home, keyFile, providerConfig); err != nil {
		return nil, nil, err
	}
	if err := addZCodeKeyCleanupHook(home); err != nil {
		return nil, nil, err
	}
	env := map[string]string{
		zcodePersonalFileEnv:  homeEnv + "/" + keyFile,
		"ZCODE_DATA_BASE_DIR": homeEnv,
		"USERPROFILE":         homeEnv,
	}
	return append([]string{"sh", "-c", zcodeCleanupScript, "zcode"}, argv...), env, nil
}

// addZCodeKeyCleanupHook merges a SessionStart hook into ZCode's config,
// keeping whatever the MCP configuration put there.
func addZCodeKeyCleanupHook(home AgentCLIHome) error {
	document := map[string]any{}
	root, err := openAgentCLIHome(home)
	if err != nil {
		return err
	}
	data, err := root.ReadFile(zcodeConfigPath)
	root.Close()
	if err == nil {
		err = json.Unmarshal(data, &document)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	hooks, _ := document["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	events, _ := hooks["events"].(map[string]any)
	if events == nil {
		events = map[string]any{}
	}
	sessionStart, _ := events["SessionStart"].([]any)
	events["SessionStart"] = append(sessionStart, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": zcodeRemoveKeyFile}}})
	hooks["events"] = events
	hooks["enabled"] = true
	document["hooks"] = hooks
	data, err = json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return writeAgentCLIFile(home, zcodeConfigPath, data)
}

// zcodeStream: `zcode --output-format stream-json` prints one session event
// per line; text and reasoning arrive as deltas closed by an _end event that
// carries no text of its own.
type zcodeStream struct {
	started         bool
	text, reasoning strings.Builder
}

func (z *zcodeStream) translate(s *ClaudeStream, raw map[string]any) []string {
	var out []string
	if !z.started {
		if session := stringValue(raw["sessionId"]); session != "" {
			z.started = true
			out = s.Init(session, "")
		}
	}
	payload, _ := raw["payload"].(map[string]any)
	switch stringValue(raw["type"]) {
	case "model.streaming":
		message := stringValue(payload["assistantMessageId"])
		switch stringValue(payload["kind"]) {
		case "text_delta":
			delta := stringValue(payload["delta"])
			z.text.WriteString(delta)
			out = append(out, s.Delta(message+"#text", "text", delta)...)
		case "text_end":
			out = append(out, s.Text(z.text.String())...)
			z.text.Reset()
		case "reasoning_delta":
			delta := stringValue(payload["delta"])
			z.reasoning.WriteString(delta)
			out = append(out, s.Delta(message+"#thinking", "thinking", delta)...)
		case "reasoning_end":
			out = append(out, s.Thinking(z.reasoning.String())...)
			z.reasoning.Reset()
		case "tool_call":
			out = append(out, s.ToolUse(stringValue(payload["toolCallId"]), stringValue(payload["toolName"]), payload["input"])...)
		}
	case "tool.updated":
		if stringValue(payload["kind"]) == "result" {
			result, _ := payload["result"].(map[string]any)
			success, _ := result["success"].(bool)
			out = append(out, s.ToolResult(stringValue(payload["toolCallId"]), !success, contentText(result["content"]))...)
		}
	case "turn.failed":
		failure, _ := payload["error"].(map[string]any)
		message := stringValue(failure["message"])
		if message == "" {
			message = "ZCode 本轮执行失败"
		}
		// assistant_failure.go recognises a rejected key by this wording.
		if attribution, _ := failure["attribution"].(map[string]any); int64Value(attribution["statusCode"]) == 401 {
			message += " (HTTP 401)"
		}
		out = append(out, s.Result(message, true)...)
	case "result":
		usage, _ := raw["usage"].(map[string]any)
		s.SetUsage(int64Value(usage["inputTokens"]), int64Value(usage["outputTokens"]), int64Value(usage["cacheReadTokens"]), int64Value(usage["cacheWriteTokens"]), int64Value(usage["reasoningTokens"]))
		out = append(out, s.Result(stringValue(raw["response"]), false)...)
	}
	return out
}
