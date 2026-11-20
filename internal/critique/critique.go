// Package critique ports AegisOS's CritiqueHandler: one model call judges a
// change before a human approves delivery. A wrong PASS costs more than a
// wrong REJECT, so an unusable answer or missing credentials always yields
// REJECT, never PASS.
package critique

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxPatchChars = 60_000
	firstBudget   = 4096
	maxAttempts   = 2
)

// SystemPrompt mirrors AegisOS CritiqueHandler.SYSTEM_PROMPT.
const SystemPrompt = `You review one change to a Java repository. You are the last check before a human
approves delivery, so a wrong PASS costs more than a wrong REJECT.

Answer with JSON only, no prose before or after, in exactly this shape:

{"verdict":"PASS"|"REJECT","summary":"one sentence","findings":["...","..."]}

Judge only these:
- does the change satisfy every clause of the requirement;
- is anything in the change unrelated to the requirement (out of scope);
- does it break behaviour the diff does not test;
- is there a defect a reader would call a bug: wrong condition, missing null check on a
  path the change introduces, exception swallowed, resource not closed.

Do not ask for more tests, more documentation, refactoring or style changes. Do not
comment on the code that was there before the change. A REJECT needs at least one
finding that names the file and says what is wrong; if you cannot name one, answer PASS.`

// Config points at an OpenAI-compatible endpoint.
type Config struct {
	URL     string
	Key     string
	Model   string
	Timeout time.Duration
	Client  *http.Client
}

// Configured reports whether a real model call is possible.
func (c Config) Configured() bool {
	return strings.TrimSpace(c.URL) != "" && strings.TrimSpace(c.Key) != "" && strings.TrimSpace(c.Model) != ""
}

// Request is the review input. Patch may be supplied directly; otherwise Repo
// and BaseCommit are used to read it. The patch must hash to DiffHash.
type Request struct {
	DiffHash        string       `json:"diffHash"`
	Repo            string       `json:"repo,omitempty"`
	BaseCommit      string       `json:"baseCommit,omitempty"`
	Patch           string       `json:"patch,omitempty"`
	Requirement     string       `json:"requirementTitle,omitempty"`
	RequirementBody string       `json:"requirementBody,omitempty"`
	Clauses         string       `json:"clauses,omitempty"`
	Spec            string       `json:"spec,omitempty"`
	Verification    Verification `json:"verification,omitempty"`
}

type Verification struct {
	Result   string `json:"result,omitempty"`
	Tests    int    `json:"tests,omitempty"`
	Failures int    `json:"failures,omitempty"`
	Errors   int    `json:"errors,omitempty"`
}

// Response is the verdict and its evidence.
type Response struct {
	Verdict  string   `json:"verdict"`
	Summary  string   `json:"summary"`
	DiffHash string   `json:"diffHash"`
	Findings []string `json:"findings,omitempty"`
}

// Review returns PASS only for a valid model answer that says PASS. Every
// failure path returns a REJECT response together with an error the caller can
// surface; the verdict is never upgraded on error.
func Review(ctx context.Context, config Config, req Request) (Response, error) {
	hash := strings.ToLower(strings.TrimSpace(req.DiffHash))
	if !isSHA256(hash) {
		return reject(hash, "diff hash is not a 64-hex sha256"), errors.New("diff hash is not a 64-hex sha256")
	}
	patch := req.Patch
	if strings.TrimSpace(patch) == "" {
		return reject(hash, "patch is required for critique"), errors.New("patch is required for critique")
	}
	sum := sha256.Sum256([]byte(patch))
	if hex.EncodeToString(sum[:]) != hash {
		return reject(hash, "patch does not match the diff hash"), fmt.Errorf("patch hashes to %s, not %s", hex.EncodeToString(sum[:]), hash)
	}
	if !config.Configured() {
		return reject(hash, "critique model is not configured"), errors.New("critique model is not configured (AEGIS_MODEL_URL, AEGIS_MODEL_KEY, model name)")
	}

	budget := firstBudget
	var lastProblem string
	for attempt := 0; attempt < maxAttempts; attempt++ {
		content, err := config.complete(ctx, messages(req, patch, lastProblem), budget, "loopforge-critique-"+hash[:8])
		if err != nil {
			lastProblem = "model call failed: " + err.Error()
			budget *= 2
			continue
		}
		answer, problem := parseAnswer(content)
		if problem != "" {
			lastProblem = problem
			budget *= 2
			continue
		}
		answer.DiffHash = hash
		return answer, nil
	}
	response := reject(hash, "critique answer invalid: "+lastProblem)
	return response, errors.New(response.Summary)
}

func messages(req Request, patch, previous string) []map[string]string {
	var user strings.Builder
	user.WriteString("## Requirement\n\n")
	user.WriteString(strings.TrimSpace(req.Requirement))
	user.WriteByte('\n')
	user.WriteString(strings.TrimSpace(req.RequirementBody))
	user.WriteString("\n")
	if clauses := strings.TrimSpace(req.Clauses); clauses != "" {
		user.WriteString("\n## Clauses\n\n" + clauses + "\n")
	}
	if spec := strings.TrimSpace(req.Spec); spec != "" {
		user.WriteString("\nAccepted spec: " + spec + "\n")
	}
	if result := strings.TrimSpace(req.Verification.Result); result != "" {
		user.WriteString(fmt.Sprintf("Verification: %s (%d tests, %d failures, %d errors)\n",
			result, req.Verification.Tests, req.Verification.Failures, req.Verification.Errors))
	}
	user.WriteString("\n## Change\n\n```diff\n" + truncatePatch(patch) + "\n```\n")
	if previous != "" {
		user.WriteString("\nYour previous answer was rejected: " + previous + "\nAnswer again in the required JSON shape.\n")
	}
	return []map[string]string{
		{"role": "system", "content": SystemPrompt},
		{"role": "user", "content": user.String()},
	}
}

func truncatePatch(patch string) string {
	if len(patch) <= maxPatchChars {
		return patch
	}
	return patch[:maxPatchChars] + fmt.Sprintf("\n\n[the patch is truncated at %d characters; judge what is shown]", maxPatchChars)
}

func (c Config) complete(ctx context.Context, messages []map[string]string, budget int, session string) (string, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"model":       c.Model,
		"messages":    messages,
		"max_tokens":  budget,
		"temperature": 0,
	})
	if err != nil {
		return "", err
	}
	client := c.Client
	if client == nil {
		client = &http.Client{}
	}
	url := strings.TrimRight(strings.TrimSpace(c.URL), "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.Key))
	// OpenCode routes by session; AegisOS sends the same header.
	httpReq.Header.Set("x-opencode-session", session)
	response, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("model returned %d: %s", response.StatusCode, truncate(string(payload), 500))
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("decode model response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return "", errors.New("model returned no choices")
	}
	return decoded.Choices[0].Message.Content, nil
}

func parseAnswer(content string) (Response, string) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	start, end := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return Response{}, "answer is not JSON"
	}
	var answer Response
	if err := json.Unmarshal([]byte(content[start:end+1]), &answer); err != nil {
		return Response{}, "answer is not the required JSON: " + err.Error()
	}
	answer.Verdict = strings.ToUpper(strings.TrimSpace(answer.Verdict))
	answer.Summary = strings.TrimSpace(answer.Summary)
	if answer.Verdict != "PASS" && answer.Verdict != "REJECT" {
		return Response{}, fmt.Sprintf("verdict %q is not PASS or REJECT", answer.Verdict)
	}
	if answer.Summary == "" {
		return Response{}, "answer has no summary"
	}
	if answer.Verdict == "REJECT" && len(answer.Findings) == 0 {
		return Response{}, "REJECT without a finding"
	}
	return answer, ""
}

func reject(hash, summary string) Response {
	return Response{Verdict: "REJECT", Summary: summary, DiffHash: hash}
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return false
		}
	}
	return true
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
