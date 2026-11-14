package runtime

import (
	"regexp"
	"strings"
)

// ModelErrorClass identifies a model-service failure when the CLI has
// reported a terminal error. Unknown failures retain their original error.
type ModelErrorClass string

const (
	ModelErrorRateLimited     ModelErrorClass = "rate_limited"
	ModelErrorOverloaded      ModelErrorClass = "overloaded"
	ModelErrorTimeout         ModelErrorClass = "timeout"
	ModelErrorAuth            ModelErrorClass = "auth"
	ModelErrorQuotaExhausted  ModelErrorClass = "quota_exhausted"
	ModelErrorContextOverflow ModelErrorClass = "context_overflow"
	ModelErrorBadRequest      ModelErrorClass = "bad_request"
)

// ModelError wraps the original error so its message and errors.Is semantics
// remain available to existing callers. Classification alone does not change
// the engine's retry policy.
type ModelError struct {
	Class ModelErrorClass
	Err   error
}

func (e *ModelError) Error() string { return e.Err.Error() }
func (e *ModelError) Unwrap() error { return e.Err }

var modelHTTPStatus = regexp.MustCompile(`\b(?:http(?: status)?|status code|error code)\s*[:=]?\s*(400|401|403|408|429|500|502|503|504|529)\b|\b(400|401|403|408|429|503|504|529)\s+(?:bad request|unauthorized|forbidden|request timeout|too many requests|service unavailable|gateway timeout|overloaded)\b`)

// classifyModelFailure only accepts text from a terminal provider error, not
// raw transcripts, assistant messages or tool output. Specific error codes
// take precedence over HTTP status (e.g. insufficient_quota can use 429).
func classifyModelFailure(message string) ModelErrorClass {
	s := strings.ToLower(message)
	contains := func(phrases ...string) bool {
		for _, phrase := range phrases {
			if strings.Contains(s, phrase) {
				return true
			}
		}
		return false
	}
	switch {
	case contains("insufficient_quota", "quota_exhausted", "quota exhausted", "insufficient balance", "credit balance is too low"):
		return ModelErrorQuotaExhausted
	case contains("context_length_exceeded", "context window exceeded", "context length exceeded", "maximum context length", "prompt is too long"):
		return ModelErrorContextOverflow
	case contains("invalid_api_key", "authentication_error", "invalid api key", "authentication failed"):
		return ModelErrorAuth
	case contains("rate_limit_error", "rate limit exceeded", "rate limited", "too many requests"):
		return ModelErrorRateLimited
	case contains("overloaded_error", "model is overloaded", "service is overloaded"):
		return ModelErrorOverloaded
	case contains("request timed out", "request timeout", "gateway timeout"):
		return ModelErrorTimeout
	}
	match := modelHTTPStatus.FindStringSubmatch(s)
	if len(match) == 0 {
		return ""
	}
	status := match[1]
	if status == "" {
		status = match[2]
	}
	switch status {
	case "400":
		return ModelErrorBadRequest
	case "401", "403":
		return ModelErrorAuth
	case "408", "504":
		return ModelErrorTimeout
	case "429":
		return ModelErrorRateLimited
	case "503", "529":
		return ModelErrorOverloaded
	}
	return ""
}
