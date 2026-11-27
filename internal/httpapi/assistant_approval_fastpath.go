package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"agentflow/internal/domain"
)

const (
	assistantApprovalRuleModel            = "rule"
	assistantApprovalPolicyModel          = "policy"
	assistantApprovalMaxConsecutiveDenies = 3
	assistantApprovalCommentFastMaxRunes  = 500
)

// evaluateApprovalFastPath settles strictly safe, low-risk, reversible built-in
// platform comments with zero LLM tokens before invoking the review model. It
// verifies gateway origin so external MCP tools can never bypass AI review,
// requires short plain-text comments free of links or secrets, and never
// hard-denies.
func (h *assistantHandlers) evaluateApprovalFastPath(ctx context.Context, user domain.User, tool string, args map[string]any) (assistantApprovalReview, bool) {
	tool = strings.TrimSpace(tool)
	if approvalActionSourceFrom(ctx) != approvalSourceBuiltinPlatform || tool != "issues_comments_add" {
		return assistantApprovalReview{}, false
	}
	if !approvalCommentFastPathSafe(args, user) {
		return assistantApprovalReview{}, false
	}
	return assistantApprovalReview{
		Outcome:           "allow",
		RiskLevel:         "low",
		UserAuthorization: "high",
		Model:             assistantApprovalRuleModel,
		Rationale:         "项目内简短纯文本评论写入，范围明确且无外链或敏感内容",
	}, true
}

func approvalCommentFastPathSafe(args map[string]any, user domain.User) bool {
	if len(args) == 0 {
		return false
	}
	normalized, ok := normalizeApprovalForRedaction(args).(map[string]any)
	if !ok {
		return false
	}
	bodyMap, ok := normalized["body"].(map[string]any)
	if !ok {
		return false
	}
	text, _ := bodyMap["body"].(string)
	if strings.TrimSpace(text) == "" {
		text, _ = bodyMap["content"].(string)
	}
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > assistantApprovalCommentFastMaxRunes {
		return false
	}
	lower := strings.ToLower(text)
	for _, scheme := range []string{"http://", "https://", "ftp://", "mailto:"} {
		if strings.Contains(lower, scheme) {
			return false
		}
	}
	if redactSecretValues(redactAgentToolResult(text, user)) != text {
		return false
	}
	var hidden []string
	redactJSONStrings(normalized, user, "", &hidden)
	return len(hidden) == 0
}

// redactApprovalArguments preserves every non-secret execution argument
// verbatim while reusing manual approval's structured redaction (known user
// credentials, secret-shaped field names like password/api_key/client_secret,
// and token patterns in strings) on a deep copy so the caller's args stay
// intact for execution.
func redactApprovalArguments(args map[string]any, user domain.User) map[string]any {
	if len(args) == 0 {
		return nil
	}
	normalized := normalizeApprovalForRedaction(args)
	var hidden []string
	redacted := redactJSONStrings(normalized, user, "", &hidden)
	out, _ := redacted.(map[string]any)
	return out
}

func normalizeApprovalForRedaction(v any) any {
	switch val := v.(type) {
	case nil, string, bool, json.Number, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return val
	case json.RawMessage:
		trimmed := bytes.TrimSpace(val)
		if len(trimmed) == 0 {
			return nil
		}
		if json.Valid(trimmed) {
			dec := json.NewDecoder(bytes.NewReader(trimmed))
			dec.UseNumber()
			var decoded any
			if dec.Decode(&decoded) == nil {
				return decoded
			}
		}
		return string(val)
	case []byte:
		return string(val)
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[k] = normalizeApprovalForRedaction(item)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[k] = item
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = normalizeApprovalForRedaction(item)
		}
		return out
	case []string:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = item
		}
		return out
	default:
		raw, err := json.Marshal(val)
		if err != nil {
			return fmt.Sprint(val)
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var decoded any
		if dec.Decode(&decoded) == nil {
			return decoded
		}
		return string(raw)
	}
}

// fitApprovalReviewBudget checks that the serialized review payload fits within
// maxBytes without ever truncating execution arguments. If needed it drops older
// conversation messages (marking Truncated=true); if full arguments alone exceed
// the budget, it returns ok=false so auto-approval fails closed.
func fitApprovalReviewBudget(action assistantApprovalInput, maxBytes int) (assistantApprovalInput, bool) {
	data, err := json.Marshal(action)
	if err != nil {
		return action, false
	}
	if len(data) <= maxBytes {
		return action, true
	}
	for len(action.Messages) > 1 {
		action.Messages = action.Messages[1:]
		action.Truncated = true
		data, err = json.Marshal(action)
		if err != nil {
			return action, false
		}
		if len(data) <= maxBytes {
			return action, true
		}
	}
	return action, false
}

func approvalActionFingerprint(tool string, args map[string]any) string {
	raw, err := json.Marshal(struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args,omitempty"`
	}{Tool: strings.TrimSpace(tool), Args: args})
	if err != nil {
		return strings.TrimSpace(tool)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
