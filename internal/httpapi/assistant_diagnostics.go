package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

type assistantDiagnosticsKey struct{}

type assistantDiagnosticsCollector struct {
	mu               sync.Mutex
	report           domain.AssistantDiagnostics
	sequence         int
	timelineSequence int
	toolSequence     int
	// persisted fingerprints the evidence last written by a running
	// checkpoint, so unchanged evidence is not rewritten every tick.
	persisted   [sha256.Size]byte
	persistedAt time.Time
}

// assistantDiagnosticsRewriteInterval bounds how long a running turn's stored
// evidence may go without a rewrite; it must stay well below the 45s after
// which a silent row is reported as CHECKPOINT_LOST.
const assistantDiagnosticsRewriteInterval = 10 * time.Second

func newAssistantDiagnosticsCollector(g domain.AssistantGeneration) *assistantDiagnosticsCollector {
	environment := assistantDiagnosticEnvironment()
	d := &assistantDiagnosticsCollector{report: domain.AssistantDiagnostics{Version: 2, GenerationID: g.ID, SessionID: g.SessionID, StartedAt: g.StartedAt, Status: g.Status, Environment: &environment, Attempts: []domain.AssistantCLIAttempt{}}}
	d.appendTimelineLocked(domain.AssistantDiagnosticEvent{At: g.StartedAt, Source: "platform", Kind: "generation.started"})
	return d
}

func assistantDiagnosticsFrom(ctx context.Context) *assistantDiagnosticsCollector {
	d, _ := ctx.Value(assistantDiagnosticsKey{}).(*assistantDiagnosticsCollector)
	return d
}

func (d *assistantDiagnosticsCollector) snapshot(g domain.AssistantGeneration) domain.AssistantDiagnostics {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.report.Status, d.report.UpdatedAt = g.Status, time.Now().UTC()
	if g.Failure != nil || g.Status == "cancelled" || g.Status == "completed" {
		d.report.Failure = g.Failure
	}
	// Deep copy bounded slices so a checkpoint never races with process output.
	raw, _ := json.Marshal(d.report)
	var out domain.AssistantDiagnostics
	_ = json.Unmarshal(raw, &out)
	domain.AnalyzeAssistantDiagnostics(&out)
	return out
}

// checkpointSnapshot returns the report for a running checkpoint and whether it
// must be written: evidence changed, or the rewrite interval elapsed.
func (d *assistantDiagnosticsCollector) checkpointSnapshot(g domain.AssistantGeneration) (domain.AssistantDiagnostics, bool) {
	report := d.snapshot(g)
	stable := report
	stable.UpdatedAt, stable.LastCheckpointAt = time.Time{}, nil
	raw, _ := json.Marshal(stable)
	sum := sha256.Sum256(raw)
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if g.Status == "running" && sum == d.persisted && now.Sub(d.persistedAt) < assistantDiagnosticsRewriteInterval {
		return report, false
	}
	d.persisted, d.persistedAt = sum, now
	return report, true
}

func (d *assistantDiagnosticsCollector) fail(ctx context.Context, err error, user domain.User) domain.AssistantFailure {
	f := classifyAssistantFailure(ctx, err)
	if d == nil {
		return f
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// Unless the error itself names the cause, prefer the structured provider
	// failure captured before translation over keywords in the stderr tail:
	// the newest error event is the one that ended the turn.
	specific := false
	if err != nil && ctx.Err() == nil {
		_, specific = classifyAssistantFailureParts(domain.AssistantFailure{Code: "GENERATION_FAILED"}, err)
	}
	if !specific && (assistantFailureGeneric(f.Code) || f.Phase == "provider" || f.Phase == "connection") {
		if candidate, ok := d.structuredFailureLocked(ctx); ok {
			f = candidate
		}
	}
	if f.Code == "AUTHENTICATION_FAILED" && len(d.report.Attempts) > 0 {
		engine := d.report.Attempts[len(d.report.Attempts)-1].Engine
		if engine != assistantEngineCodeBuddy {
			f.Message = fmt.Sprintf("%s 请求鉴权失败，请检查个人配置后重试", assistantEngineLabel(engine))
		}
	}
	d.report.Failure = &f
	if err != nil {
		d.report.Error = sanitizeAssistantDiagnostic(err.Error(), user, 4096)
	}
	d.appendTimelineLocked(domain.AssistantDiagnosticEvent{Kind: "generation.failed", Source: "platform", Code: f.Code, Phase: f.Phase, Message: d.report.Error})
	return f
}

// structuredFailureLocked classifies the newest CLI error event, falling back
// to its HTTP status when the provider text matches no known wording.
func (d *assistantDiagnosticsCollector) structuredFailureLocked(ctx context.Context) (domain.AssistantFailure, bool) {
	for i := len(d.report.Attempts) - 1; i >= 0; i-- {
		attempt := d.report.Attempts[i]
		candidates := append([]domain.AssistantDiagnosticEvent{}, attempt.Events...)
		if attempt.FirstError != nil {
			candidates = append([]domain.AssistantDiagnosticEvent{*attempt.FirstError}, candidates...)
		}
		for j := len(candidates) - 1; j >= 0; j-- {
			e := candidates[j]
			if e.Kind == "provider.metadata" || !(e.StatusCode >= 400 || strings.Contains(e.Kind, "error") || strings.Contains(e.Kind, "fail")) {
				continue
			}
			candidate := classifyAssistantFailure(ctx, fmt.Errorf("%s %s", e.Code, e.Message))
			if assistantFailureGeneric(candidate.Code) && e.StatusCode >= 400 {
				candidate = classifyAssistantFailure(ctx, fmt.Errorf("http %d", e.StatusCode))
				if assistantFailureGeneric(candidate.Code) && e.StatusCode == http.StatusNotFound {
					candidate = classifyAssistantFailure(ctx, errors.New("model_not_found"))
				}
			}
			if !assistantFailureGeneric(candidate.Code) {
				return candidate, true
			}
		}
	}
	return domain.AssistantFailure{}, false
}

func (d *assistantDiagnosticsCollector) failureForMessage(message string) *domain.AssistantFailure {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.report.Failure == nil || d.report.Failure.Message != message {
		d.report.Failure = &domain.AssistantFailure{Code: "GENERATION_FAILED", Phase: "orchestration", Message: message, Suggestion: "下载诊断信息交给管理员排查；重试前请检查已执行的操作。"}
	}
	f := *d.report.Failure
	return &f
}

type assistantDiagnosticAttempt struct {
	d    *assistantDiagnosticsCollector
	id   string
	user domain.User
}

func (d *assistantDiagnosticsCollector) termination() *domain.AssistantDiagnosticTermination {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.report.Termination == nil {
		return nil
	}
	copy := *d.report.Termination
	return &copy
}

func (d *assistantDiagnosticsCollector) currentAttemptID() string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := len(d.report.Attempts) - 1; i >= 0; i-- {
		if d.report.Attempts[i].Status == "running" {
			return d.report.Attempts[i].ID
		}
	}
	if n := len(d.report.Attempts); n > 0 {
		return d.report.Attempts[n-1].ID
	}
	return ""
}

func (d *assistantDiagnosticsCollector) begin(engine, model string, resume bool, user domain.User) *assistantDiagnosticAttempt {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sequence++
	id := fmt.Sprintf("%s/%d", d.report.GenerationID, d.sequence)
	if len(d.report.Attempts) >= 16 {
		d.report.Attempts = d.report.Attempts[1:]
		d.dropLocked("attempts", 1)
	}
	d.report.Attempts = append(d.report.Attempts, domain.AssistantCLIAttempt{ID: id, Engine: engine, Model: sanitizeAssistantDiagnostic(model, user, 200), Resume: resume, StartedAt: time.Now().UTC(), Phase: "setup", Status: "running"})
	d.appendTimelineLocked(domain.AssistantDiagnosticEvent{Source: "platform", Kind: "cli.preparing", AttemptID: id, Phase: "setup"})
	return &assistantDiagnosticAttempt{d: d, id: id, user: user}
}

func (a *assistantDiagnosticAttempt) update(fn func(*domain.AssistantCLIAttempt)) {
	if a == nil {
		return
	}
	a.d.mu.Lock()
	defer a.d.mu.Unlock()
	for i := range a.d.report.Attempts {
		if a.d.report.Attempts[i].ID == a.id {
			fn(&a.d.report.Attempts[i])
			return
		}
	}
}

func (a *assistantDiagnosticAttempt) phase(phase string) {
	a.update(func(attempt *domain.AssistantCLIAttempt) { attempt.Phase = phase })
}

func (a *assistantDiagnosticAttempt) finish(err error) {
	a.update(func(attempt *domain.AssistantCLIAttempt) {
		now := time.Now().UTC()
		attempt.FinishedAt = &now
		attempt.Status = "completed"
		if err != nil {
			attempt.Status = "failed"
			attempt.Error = sanitizeAssistantDiagnostic(err.Error(), a.user, 4096)
			if len([]rune(attempt.Error)) > 4096 {
				a.d.report.Truncated = true
			}
			if errors.Is(err, context.Canceled) {
				attempt.Status = "cancelled"
			}
		}
		a.d.appendTimelineLocked(domain.AssistantDiagnosticEvent{At: now, Source: "platform", Kind: "cli." + attempt.Status, AttemptID: a.id, Phase: attempt.Phase, Message: attempt.Error})
	})
}

// Process exit and generation outcome differ: a zero exit may have no result.
func (a *assistantDiagnosticAttempt) exited(err error) {
	a.update(func(attempt *domain.AssistantCLIAttempt) {
		if err == nil {
			code := 0
			attempt.ExitCode = &code
			return
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code := exit.ExitCode()
			attempt.ExitCode = &code
		}
	})
}

func (a *assistantDiagnosticAttempt) stderr(line string) {
	if a == nil {
		return
	}
	rawLine := line
	line = sanitizeAssistantDiagnostic(line, a.user, 1024)
	if strings.TrimSpace(line) == "" {
		return
	}
	a.update(func(attempt *domain.AssistantCLIAttempt) {
		if len([]rune(rawLine)) > 1024 {
			a.d.dropLocked("stderrCharacters", len([]rune(rawLine))-1024)
		}
		if len(attempt.FirstStderr) < 8 {
			attempt.FirstStderr = append(attempt.FirstStderr, line)
		}
		attempt.Stderr = append(attempt.Stderr, line)
		if len(attempt.Stderr) > 20 {
			attempt.Stderr = attempt.Stderr[1:]
			a.d.dropLocked("stderrLines", 1)
		}
	})
	var entry struct {
		Level  string `json:"level"`
		Target string `json:"target"`
		Fields struct {
			Message       string `json:"message"`
			SamplingError string `json:"sampling_error"`
		} `json:"fields"`
	}
	if json.Unmarshal([]byte(rawLine), &entry) == nil && (entry.Level == "ERROR" || entry.Fields.SamplingError != "") {
		// Benign Codex streaming order logs stay in raw stderr but must not mask real errors.
		if entry.Fields.SamplingError == "" && entry.Target == "codex_core::util" && strings.Contains(entry.Fields.Message, "without active item") {
			return
		}
		kind := "cli.stderr.error"
		message := entry.Fields.Message
		if entry.Fields.SamplingError != "" {
			kind = "cli.stderr.sampling_error"
			if message != "" {
				message += ": " + entry.Fields.SamplingError
			} else {
				message = entry.Fields.SamplingError
			}
		}
		e := domain.AssistantDiagnosticEvent{At: time.Now().UTC(), Kind: kind, Message: sanitizeAssistantDiagnostic(message, a.user, 1024)}
		diagnosticTextMetadata(&e)
		record := true
		a.update(func(attempt *domain.AssistantCLIAttempt) {
			if len(attempt.Events) > 0 {
				last := attempt.Events[len(attempt.Events)-1]
				record = last.Message != e.Message || last.RequestID != e.RequestID
			}
		})
		if record {
			a.recordEvent(e)
		}
	}
}

// Inspect only protocol metadata containers. Never keep assistant text, tool
// input/output, headers, config, argv or the original stdout JSON.
func (a *assistantDiagnosticAttempt) protocol(line string) {
	if a == nil {
		return
	}
	if len(line) > 256<<10 {
		a.update(func(*domain.AssistantCLIAttempt) { a.d.report.Truncated = true })
		return
	}
	var raw map[string]any
	if json.Unmarshal([]byte(line), &raw) != nil {
		return
	}
	a.nativeMetadata(raw)
	e := domain.AssistantDiagnosticEvent{At: time.Now().UTC()}
	var inspect func(map[string]any, int)
	inspect = func(v map[string]any, depth int) {
		if depth > 5 {
			return
		}
		for _, key := range []string{"type", "method"} {
			if s, _ := v[key].(string); s != "" && (strings.Contains(s, "error") || strings.Contains(s, "failed") || strings.Contains(s, "retry") || strings.Contains(s, "warning")) {
				e.Kind = sanitizeAssistantDiagnostic(s, a.user, 120)
			}
		}
		for _, field := range []struct {
			key  string
			dest *string
		}{{"requestId", &e.RequestID}, {"request_id", &e.RequestID}, {"traceId", &e.TraceID}, {"trace_id", &e.TraceID}, {"errorCode", &e.Code}, {"providerErrorCode", &e.Code}, {"errorPhase", &e.Phase}} {
			if s, _ := v[field.key].(string); s != "" {
				*field.dest = sanitizeAssistantDiagnostic(s, a.user, 160)
			}
		}
		if s, _ := v["code"].(string); s != "" && e.Code == "" {
			e.Code = sanitizeAssistantDiagnostic(s, a.user, 160)
		}
		for _, key := range []string{"statusCode", "status_code", "httpStatusCode"} {
			if n, ok := v[key].(float64); ok && n >= 100 && n <= 599 {
				e.StatusCode = int(n)
			}
		}
		for _, key := range []string{"retryable", "willRetry"} {
			if b, ok := v[key].(bool); ok {
				value := b
				e.Retryable = &value
			}
		}
		for _, key := range []string{"payload", "params", "data", "error", "attribution", "turn", "codexErrorInfo", "httpConnectionFailed", "responseStreamDisconnected", "responseStreamConnectionFailed"} {
			if nested, ok := v[key].(map[string]any); ok {
				if key == "error" {
					e.Kind = "provider.error"
				}
				inspect(nested, depth+1)
			}
		}
		if e.Kind != "" {
			for _, key := range []string{"message", "providerErrorMessage", "error"} {
				if s, _ := v[key].(string); s != "" {
					e.Message = sanitizeAssistantDiagnostic(s, a.user, 1024)
				}
			}
		}
	}
	inspect(raw, 0)
	diagnosticTextMetadata(&e)
	if e.Kind == "" && e.RequestID == "" && e.TraceID == "" && e.StatusCode == 0 {
		return
	}
	if e.Kind == "" {
		e.Kind = "provider.metadata"
	}
	a.recordEvent(e)
}

var (
	diagnosticANSI   = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	diagnosticPEM    = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?(?:-----END [^-]*PRIVATE KEY-----|$)`)
	diagnosticBearer = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`)
	diagnosticSecret = regexp.MustCompile(`(?i)((?:["']?(?:[a-z0-9_-]*(?:api[_-]?key|auth[_-]?token|access[_-]?token|refresh[_-]?token|password|passwd|client[_-]?secret)|authorization|cookie|set-cookie|token|secret)["']?)\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}]+)`)
	diagnosticKey    = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{8,}|ghp_[A-Za-z0-9]+|github_pat_[A-Za-z0-9_]+|glpat-[A-Za-z0-9_-]+)\b`)
	diagnosticURL    = regexp.MustCompile(`https?://[^\s<>"']+`)
	diagnosticPath   = regexp.MustCompile(`(?:/(?:Users|home|root|tmp|private|var|data|workspace|workspaces|opt|srv)/|[A-Za-z]:\\)[^\s"'<>;,|]+`)
)

func sanitizeAssistantDiagnostic(text string, user domain.User, limit int) string {
	secrets := []string{user.CodeBuddyAPIKey, user.CodexAPIKey, user.ClaudeCodeAPIKey, user.GitToken, user.GongfengRefreshToken}
	for _, credential := range user.AgentCLICredentials {
		secrets = append(secrets, credential.APIKey)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "<redacted>")
		}
	}
	text = diagnosticANSI.ReplaceAllString(text, "")
	text = diagnosticPEM.ReplaceAllString(text, "<redacted private key>")
	text = diagnosticBearer.ReplaceAllString(text, "$1 <redacted>")
	text = diagnosticSecret.ReplaceAllString(text, "${1}<redacted>")
	text = diagnosticKey.ReplaceAllString(text, "<redacted>")
	text = diagnosticURL.ReplaceAllStringFunc(text, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "<redacted URL>"
		}
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		// Session MCP paths can contain authentication tokens.
		if strings.Contains(u.Path, "/mcp/") {
			u.Path, u.RawPath = "/mcp/<redacted>", ""
		}
		return u.String()
	})
	text = diagnosticPath.ReplaceAllString(text, "<path>")
	return truncateRunes(text, limit)
}

func (h *assistantHandlers) generationDiagnostics(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	// Like attachments outside the transcript, error excerpts require project
	// membership. Public project-conversation links expose only the safe summary.
	ownerID, _, err := h.conversationScope(c, c.Param("id"), "", "", user.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	id := c.Param("generationId")
	report, ok := h.repo.AssistantDiagnostics(ownerID, c.Param("id"), id)
	if !ok {
		c.JSON(404, gin.H{"error": "该轮次没有可用的诊断记录"})
		return
	}
	if value, live := assistantLiveGenerations.Load(id); live {
		l := value.(*assistantLiveGeneration)
		if l.diagnostics != nil {
			report = l.diagnostics()
		}
	}
	c.Header("Cache-Control", "no-store")
	if c.Query("download") == "1" {
		data, _ := json.MarshalIndent(report, "", "  ")
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="loopforge-diagnostics-%s.json"`, url.PathEscape(report.GenerationID)))
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(200, "application/json; charset=utf-8", data)
		return
	}
	c.JSON(200, report)
}
