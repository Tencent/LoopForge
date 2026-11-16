package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestHandoffPromptsAskForAShortAckInsteadOfSilence(t *testing.T) {
	for name, prompt := range map[string]string{
		"workspace system": assistantWorkspaceSystemPrompt,
		"platform":         assistantPlatformExecutionReminder,
		"tool result":      assistantHandoffNotice,
	} {
		// A phase with reasoning only (no text, no tool call) is replayed on
		// resume and rejected by providers, so no prompt may ask for silence.
		if strings.Contains(prompt, "静默") || strings.Contains(prompt, "不再输出说明") || !strings.Contains(prompt, "「"+assistantHandoffAck+"」") {
			t.Fatalf("%s prompt does not ask for the handoff ack: %s", name, prompt)
		}
	}
}

// A fallback after a handoff runs in the new mode's tool set, so the replayed
// context must describe that mode, not the one the turn started in.
func TestFreshPhasePromptFollowsCurrentMode(t *testing.T) {
	h := &assistantHandlers{}
	req := assistantRequest{Messages: []assistantMessage{{Role: "user", Content: "改一下文案"}}}
	platform := h.freshPhasePrompt(req, t.TempDir(), 0)
	req.workspaceMode = true
	workspace := h.freshPhasePrompt(req, t.TempDir(), 1)
	if !strings.Contains(platform, assistantPlatformExecutionReminder) || strings.Contains(platform, "工作区原生文件、补丁和命令直接执行") {
		t.Fatal("platform phase prompt does not describe platform mode")
	}
	if strings.Contains(workspace, assistantPlatformExecutionReminder) || !strings.Contains(workspace, "工作区原生文件、补丁和命令直接执行") {
		t.Fatal("workspace phase prompt still describes platform mode")
	}
	for _, prompt := range []string{platform, workspace} {
		if !strings.Contains(prompt, "改一下文案") {
			t.Fatal("fresh phase prompt lost the transcript")
		}
	}
	if strings.Contains(platform, "本轮之前的阶段已执行过操作") || !strings.Contains(workspace, "本轮之前的阶段已执行过操作") {
		t.Fatal("prior-phase note does not follow the handoff count")
	}
}

func TestHandoffOutlivesOldPhaseError(t *testing.T) {
	s := &AgentToolSession{}
	ctx := context.Background()
	if assistantHandoffOutlivesPhaseError(ctx, s, errAssistantMissingResult) {
		t.Fatal("error without a handoff was ignored")
	}
	s.requestMode(domain.AssistantModeWorkspace)
	if !assistantHandoffOutlivesPhaseError(ctx, s, errAssistantMissingResult) {
		t.Fatal("missing result after a successful handoff failed the turn")
	}
	if !assistantHandoffOutlivesPhaseError(ctx, s, fmt.Errorf("exit status 1; stderr=boom")) {
		t.Fatal("non-zero exit after a successful handoff failed the turn")
	}
	if assistantHandoffOutlivesPhaseError(ctx, s, fmt.Errorf("%w: x", runtime.ErrConversationNotStarted)) {
		t.Fatal("a process that never started cannot have handed off")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if assistantHandoffOutlivesPhaseError(cancelled, s, context.Canceled) {
		t.Fatal("cancellation must still stop the turn")
	}
}

func TestResumeRecoverableOnlyForUnusableJournals(t *testing.T) {
	ctx := context.Background()
	for _, err := range []error{
		errAssistantSessionMissing,
		errors.New("exit status 1; stderr=API Error: 400 the request parameters were rejected by the model provider"),
		errors.New("API Error: 400 {\"error\":\"the conversation must end with a user message\"}"),
		errAssistantMissingResult,
		errors.New("exit status 1"),
	} {
		if !assistantResumeRecoverable(ctx, err) {
			t.Fatalf("unusable journal not recovered: %v", err)
		}
	}
	for _, err := range []error{
		nil,
		errors.New("exit status 1; stderr=network interrupted"),
		errors.New("status code 429: rate limit exceeded"),
		errors.New("status 401 unauthorized"),
		errors.New("insufficient_quota"),
		errAssistantCredentialRejected,
		errAssistantCLIMissing,
		assistantCredentialError{engine: "codex"},
		fmt.Errorf("%w: offline", errAssistantExecutionUnavailable),
		fmt.Errorf("%w: sandbox", runtime.ErrConversationNotStarted),
		runtime.ErrMaxTurns,
		context.Canceled,
	} {
		if assistantResumeRecoverable(ctx, err) {
			t.Fatalf("restarted the native session for %v", err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if assistantResumeRecoverable(cancelled, errAssistantSessionMissing) {
		t.Fatal("restarted after cancellation")
	}
}

func TestPhaseProgressIgnoresStatusOnly(t *testing.T) {
	if assistantPhaseProgress(assistantStreamEvent{Kind: "status"}) {
		t.Fatal("status counted as progress")
	}
	for _, kind := range []string{"tool", "text", "answer", "thinking"} {
		if !assistantPhaseProgress(assistantStreamEvent{Kind: kind}) {
			t.Fatalf("%s not counted as progress", kind)
		}
	}
}
