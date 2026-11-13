package httpapi

import (
	"context"
	"errors"
	"regexp"

	"agentflow/internal/runtime"
)

// freshPhasePrompt rebuilds the full transcript for a fresh native session.
// req.workspaceMode follows the current phase, so a fallback after a handoff
// describes the new mode rather than the one the turn started in.
func (h *assistantHandlers) freshPhasePrompt(req assistantRequest, sourceRoot string, switches int) string {
	prompt := h.turnPrompt(req, sourceRoot, false)
	if switches > 0 {
		prompt += "\n本轮之前的阶段已执行过操作。先核对工作区实际文件与状态，避免重复执行。"
	}
	return prompt
}

// assistantResumeRejectedRe matches providers refusing a replayed native
// journal: a 400/422, or the wording CodeBuddy and OpenAI-compatible
// gateways use for a transcript whose structure they reject.
var assistantResumeRejectedRe = regexp.MustCompile(`(?i)(must end with a user message|request parameters were rejected|invalid_request_error|invalid request|(?:status|status code|http)[ =:]*(?:400|422)\b|\b(?:400|422) bad request)`)

// assistantPhaseProgress reports whether an event shows the CLI got past
// replaying its journal: model output or a tool call. A phase that failed
// with none of these executed nothing and can safely start over.
func assistantPhaseProgress(ev assistantStreamEvent) bool {
	switch ev.Kind {
	case "tool", "toolDone", "toolEnd", "text", "answer", "agentText", "draftStart", "draftDelta", "thinking", "thinkingStart":
		return true
	}
	return false
}

// assistantResumeRecoverable reports whether resuming a native session failed
// because the journal itself is unusable (missing, or rejected by the
// provider) rather than for a reason a fresh session would hit again or that
// the user must fix: cancellation, credentials, an unavailable execution
// machine, a missing CLI, rate limits, quota or network failures.
func assistantResumeRecoverable(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if errors.Is(err, errAssistantSessionMissing) {
		return true
	}
	var credential assistantCredentialError
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, runtime.ErrConversationNotStarted) || errors.Is(err, errAssistantExecutionUnavailable) ||
		errors.Is(err, errAssistantCredentialRejected) || errors.Is(err, errAssistantCLIMissing) ||
		errors.Is(err, runtime.ErrMaxTurns) || errors.As(err, &credential) {
		return false
	}
	failure := classifyAssistantFailure(ctx, err)
	if assistantResumeRejectedRe.MatchString(err.Error()) {
		// A rejection wording outranks generic exit codes, but never an
		// explicit provider cause such as authentication or rate limiting.
		return assistantFailureGeneric(failure.Code)
	}
	return failure.Code == "MISSING_RESULT" || failure.Code == "CLI_EXIT_NONZERO"
}

// assistantHandoffOutlivesPhaseError reports whether a phase error should be
// ignored because a mode handoff already succeeded in that phase. The old CLI
// may end without type=result once it has nothing left to do; failing the
// turn there would leave the session in the old mode.
func assistantHandoffOutlivesPhaseError(ctx context.Context, s *AgentToolSession, err error) bool {
	return err != nil && s != nil && s.requestedMode() != "" && ctx.Err() == nil && !errors.Is(err, runtime.ErrConversationNotStarted)
}
