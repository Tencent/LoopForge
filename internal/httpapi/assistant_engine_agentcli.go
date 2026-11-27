package httpapi

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"agentflow/internal/runtime"
)

// prepareAssistantAgentCLIInvocation runs one chat turn on an agent CLI in
// its own headless mode, read-only: the session's MCP gateway stays
// available while the CLI's shell, edit and web tools are denied.
func prepareAssistantAgentCLIInvocation(kind string, o assistantEngineInvocationOpts) (assistantCLIInvocation, error) {
	servers, err := readAssistantMCPServers(o.Config)
	if err != nil {
		return assistantCLIInvocation{}, err
	}
	entries := make(map[string]any, len(servers))
	for id, server := range servers {
		entry := map[string]any{}
		for key, value := range server {
			// CodeBuddy's per-tool defer_loading extension.
			if key != "tools" {
				entry[key] = value
			}
		}
		if url, _ := entry["url"].(string); strings.TrimSpace(url) != "" {
			entry["type"] = "http"
		}
		entries[id] = entry
	}
	if err := runtime.WriteAgentCLIMCPConfig(kind, runtime.AgentCLIHome{Base: o.Root, Rel: "home"}, entries, runtime.AgentCLIMCPOptions{ReadOnly: !o.Workspace}); err != nil {
		return assistantCLIInvocation{}, err
	}
	resume := ""
	cursorACP := false
	if o.Resume {
		resume, cursorACP = readAgentCLISession(o.Root, kind, o.SessionID)
		if !agentCLISessionValid(resume) {
			return assistantCLIInvocation{resumeMissing: true}, nil
		}
	}
	systemFile := filepath.Join(o.Root, kind+"-system.md")
	if err := os.WriteFile(systemFile, []byte(o.SystemPrompt), 0o600); err != nil {
		return assistantCLIInvocation{}, err
	}
	agentFile := ""
	if kind == runtime.AgentCLIKimi {
		agentFile = filepath.Join(o.Root, "kimi-agent.md")
		// Kimi binds an agent at session creation. Keep its native tools stable
		// across resumes; the per-invocation PreToolUse hook gates each mode.
		if err := runtime.WriteKimiConversationAgent(agentFile, o.SystemPrompt, false); err != nil {
			return assistantCLIInvocation{}, err
		}
	}
	command, input, err := runtime.StageAgentCLIConversationCommand(o.Root, kind, runtime.AgentCLICommandOptions{
		Binary: o.Bin, Model: o.Model, Effort: o.Effort, Resume: resume, Prompt: o.Prompt,
		SystemPrompt: o.SystemPrompt, SystemPromptFile: systemFile, Chat: true, Workspace: o.Workspace, AgentFile: agentFile,
		RunnerLogin: o.RunnerLogin, ImagePaths: o.ImagePaths, ImageInput: o.ImageInput, CursorACP: cursorACP,
	})
	if err != nil {
		return assistantCLIInvocation{}, err
	}
	command.CredentialBindings = []runtime.CredentialBinding{runtime.BindAgentCLI}
	translate := runtime.NewAgentCLITranslator(kind)
	var interactive *assistantInteractiveInput
	cursorACP = input.ACPPrompt != nil
	if cursorACP {
		acp, err := newAssistantACP(filepath.Join(o.Root, "ws"), *input.ACPPrompt, resume)
		if err != nil {
			input.Close()
			return assistantCLIInvocation{}, err
		}
		interactive = acp.assistantInteractiveInput
		command.Stdin, translate = acp, acp.translate
	} else if kind == runtime.AgentCLIAntigravity && command.Stdin != nil {
		// The NDJSON session ends on EOF. Keep it open until its first result.
		frame, err := io.ReadAll(command.Stdin)
		if err != nil {
			input.Close()
			return assistantCLIInvocation{}, err
		}
		interactive, err = newAssistantInteractiveInput()
		if err != nil {
			input.Close()
			return assistantCLIInvocation{}, err
		}
		interactive.enqueue(frame)
		command.Stdin = interactive.input
	}
	native := ""
	return assistantCLIInvocation{
		command:     command,
		credentials: runtime.RunCredentials{AgentCLI: map[string]runtime.ProviderCredential{kind: o.User}},
		sessionRef:  resume,
		translate: func(line string) []string {
			out := translate(line)
			for i, translated := range out {
				var finished bool
				out[i], finished = assistantAgentCLILine(kind, translated, &native)
				if finished && kind == runtime.AgentCLIAntigravity && interactive != nil {
					interactive.closeWriter()
				}
				if !o.ReviewOnly && agentCLISessionValid(native) {
					_ = writeAgentCLISession(o.Root, kind, o.SessionID, native, cursorACP)
				}
			}
			return out
		},
		finish: func(ok bool) {
			defer input.Close()
			if interactive != nil {
				defer interactive.close()
			}
			if !o.ReviewOnly && agentCLISessionValid(native) {
				_ = writeAgentCLISession(o.Root, kind, o.SessionID, native, cursorACP)
			}
		},
	}, nil
}

// assistantAgentCLILine records the native session a translated line names
// and turns a failed result into an error line: the pipeline reports error
// lines (and recognises a rejected key in them), while a failed result would
// read as an empty reply.
func assistantAgentCLILine(kind, line string, native *string) (string, bool) {
	var event struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		IsError   bool   `json:"is_error"`
		Result    string `json:"result"`
	}
	if json.Unmarshal([]byte(line), &event) != nil {
		return line, false
	}
	if id := strings.TrimSpace(event.SessionID); id != "" {
		*native = id
	}
	if event.Type != "result" || !event.IsError {
		return line, event.Type == "result"
	}
	message := strings.TrimSpace(event.Result)
	if message == "" {
		message = assistantEngineLabel(kind) + " 本轮执行失败"
	}
	return codexLine(map[string]any{"type": "error", "error": message, "session_id": *native}), true
}

// agent-cli-sessions.json maps "<kind>:<chat session>" onto the CLI's own
// session ID. A matching Cursor ACP entry pins that native session's transport.
var agentCLISessionsMu sync.Mutex

const cursorACPSessionPrefix = "cursor-acp:"

// agentCLISessionValid guards the native ID before it reaches an argument.
func agentCLISessionValid(id string) bool {
	return id != "" && id[0] != '-' && codexConfigKeyRe.MatchString(id)
}

func agentCLISessionsPath(root string) string {
	return filepath.Join(root, "agent-cli-sessions.json")
}

func readAgentCLISession(root, kind, sessionID string) (string, bool) {
	agentCLISessionsMu.Lock()
	defer agentCLISessionsMu.Unlock()
	sessions := map[string]string{}
	if data, err := os.ReadFile(agentCLISessionsPath(root)); err == nil {
		_ = json.Unmarshal(data, &sessions)
	}
	native := sessions[kind+":"+sessionID]
	return native, kind == runtime.AgentCLICursor && native != "" && sessions[cursorACPSessionPrefix+sessionID] == native
}

func writeAgentCLISession(root, kind, sessionID, native string, cursorACP bool) error {
	agentCLISessionsMu.Lock()
	defer agentCLISessionsMu.Unlock()
	sessions := map[string]string{}
	if data, err := os.ReadFile(agentCLISessionsPath(root)); err == nil {
		_ = json.Unmarshal(data, &sessions)
	}
	sessions[kind+":"+sessionID] = native
	if kind == runtime.AgentCLICursor {
		if cursorACP {
			sessions[cursorACPSessionPrefix+sessionID] = native
		} else {
			delete(sessions, cursorACPSessionPrefix+sessionID)
		}
	}
	data, err := json.Marshal(sessions)
	if err != nil {
		return err
	}
	return os.WriteFile(agentCLISessionsPath(root), data, 0o600)
}
