package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	stdsync "sync"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

const (
	clarifyJobTimeout  = 10 * time.Minute
	clarifyMaxInflight = 2
	clarifyThoughtCap  = 80
	composerJobPrefix  = "composer:"
)

type clarifyExecFunc func(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(event string, payload any)) (parsed any, raw string, repoInfo map[string]any, err error)

type clarifyJobEvent struct {
	Seq     int
	Type    string
	Payload any
}

type clarifyJob struct {
	issueID   string
	startedAt time.Time
	mu        stdsync.Mutex
	seq       int
	events    []clarifyJobEvent
	subs      map[chan clarifyJobEvent]struct{}
	cancel    context.CancelFunc
	done      chan struct{}
	err       string
	result    map[string]any
}

type clarifyHub struct {
	h      *streamHandlers
	mu     stdsync.Mutex
	jobs   map[string]*clarifyJob
	tokens chan struct{}
	exec   clarifyExecFunc
}

var (
	clarifyExecMu       stdsync.Mutex
	clarifyExecOverride clarifyExecFunc
)

func setClarifyExecForTest(fn clarifyExecFunc) {
	clarifyExecMu.Lock()
	clarifyExecOverride = fn
	clarifyExecMu.Unlock()
}

func testClarifyExec(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(event string, payload any)) (any, string, map[string]any, error) {
	clarifyExecMu.Lock()
	override := clarifyExecOverride
	clarifyExecMu.Unlock()
	if override != nil {
		return override(ctx, issue, request, emit)
	}
	if emit != nil {
		emit("thought", map[string]string{"kind": "thinking", "text": "test-analyze " + issue.Title})
	}
	parsed := map[string]any{
		"summary": "需要补充可验证的验收标准",
		"questions": []any{
			map[string]any{"id": "q1", "question": "验收怎么确认？", "options": []any{"接口回归", "手工验收"}, "multiSelect": false},
		},
		"ready": false,
	}
	raw, _ := json.Marshal(parsed)
	return parsed, string(raw), map[string]any{"analyzed": false, "reason": "test"}, nil
}

func newClarifyHub(h *streamHandlers) *clarifyHub {
	return &clarifyHub{
		h:      h,
		jobs:   map[string]*clarifyJob{},
		tokens: make(chan struct{}, clarifyMaxInflight),
	}
}

func (hub *clarifyHub) runner() clarifyExecFunc {
	if hub.exec != nil {
		return hub.exec
	}
	if hub.h != nil && hub.h.clarifyRun != nil {
		return hub.h.clarifyRun
	}
	return hub.h.executeClarify
}

func (h *streamHandlers) jobRunning(issueID string) bool {
	if h == nil || h.jobs == nil || issueID == "" {
		return false
	}
	h.jobs.mu.Lock()
	defer h.jobs.mu.Unlock()
	job := h.jobs.jobs[issueID]
	return job != nil && !job.finished()
}

func (j *clarifyJob) finished() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

func (h *streamHandlers) autoClarifyIssue(issueID string) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" || h == nil || h.repo == nil {
		return
	}
	issue, ok := h.repo.Issue(issueID)
	if !ok || !domain.ShouldAutoStartClarification(issue) {
		return
	}
	if h.jobRunning(issueID) {
		return
	}
	req := chatClarifyRequest{Message: domain.ClarificationKickoff, History: historyToClarify(issue.Clarification.History)}
	if _, err := h.ensureClarifyJob(issue, req); err != nil {
		log.Printf("auto clarify %s: %v", issueID, err)
	}
}

func (h *streamHandlers) recoverClarifyJobs() {
	if h == nil || h.repo == nil {
		return
	}
	for _, issue := range h.repo.Issues() {
		if issue.Clarification.Status == domain.ClarificationAnalyzing {
			h.autoClarifyIssue(issue.ID)
		}
	}
}

func historyToClarify(history []domain.ClarificationMessage) []struct {
	Role    string `json:"role"`
	Content string `json:"content"`
} {
	out := make([]struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}, 0, len(history))
	for _, item := range history {
		out = append(out, struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{Role: item.Role, Content: item.Content})
	}
	return out
}

func (h *streamHandlers) ensureClarifyJob(issue domain.Issue, request chatClarifyRequest) (*clarifyJob, error) {
	if strings.TrimSpace(issue.ID) == "" {
		return nil, fmt.Errorf("issue id is required")
	}
	h.jobs.mu.Lock()
	if existing := h.jobs.jobs[issue.ID]; existing != nil && !existing.finished() {
		h.jobs.mu.Unlock()
		return existing, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), clarifyJobTimeout)
	job := &clarifyJob{
		issueID:   issue.ID,
		startedAt: time.Now().UTC(),
		subs:      map[chan clarifyJobEvent]struct{}{},
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	h.jobs.jobs[issue.ID] = job
	h.jobs.mu.Unlock()

	message := strings.TrimSpace(request.Message)
	if message == "" {
		message = domain.ClarificationKickoff
		request.Message = message
	}
	if err := h.markIssueAnalyzing(issue.ID, message, request); err != nil {
		cancel()
		h.jobs.mu.Lock()
		delete(h.jobs.jobs, issue.ID)
		h.jobs.mu.Unlock()
		return nil, err
	}
	go h.runClarifyJob(ctx, job, issue.ID, request)
	return job, nil
}

func composerJobKey(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, composerJobPrefix)
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		out = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return composerJobPrefix + out
}

func composerPublicJobID(jobID string) string {
	return strings.TrimPrefix(jobID, composerJobPrefix)
}

func (h *streamHandlers) ensureComposerJob(issue domain.Issue, request chatClarifyRequest) (*clarifyJob, error) {
	if h == nil || h.jobs == nil {
		return nil, fmt.Errorf("clarify hub not ready")
	}
	jobID := composerJobKey(request.JobID)
	h.jobs.mu.Lock()
	if existing := h.jobs.jobs[jobID]; existing != nil && !existing.finished() {
		h.jobs.mu.Unlock()
		return existing, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), clarifyJobTimeout)
	job := &clarifyJob{
		issueID:   jobID,
		startedAt: time.Now().UTC(),
		subs:      map[chan clarifyJobEvent]struct{}{},
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	h.jobs.jobs[jobID] = job
	h.jobs.mu.Unlock()
	go h.runComposerClarifyJob(ctx, job, issue, request)
	job.publish("thought", map[string]string{"kind": "status", "text": "正在准备分析…"})
	return job, nil
}

func (h *streamHandlers) runComposerClarifyJob(ctx context.Context, job *clarifyJob, issue domain.Issue, request chatClarifyRequest) {
	defer func() {
		job.cancel()
		close(job.done)
		time.AfterFunc(2*time.Minute, func() {
			h.jobs.mu.Lock()
			if current := h.jobs.jobs[job.issueID]; current == job {
				delete(h.jobs.jobs, job.issueID)
			}
			h.jobs.mu.Unlock()
		})
	}()
	select {
	case h.jobs.tokens <- struct{}{}:
	default:
		job.publish("thought", map[string]string{"kind": "status", "text": "分析排队中…"})
		select {
		case h.jobs.tokens <- struct{}{}:
		case <-ctx.Done():
			job.fail("分析已取消")
			return
		}
	}
	defer func() { <-h.jobs.tokens }()
	emit := func(event string, payload any) {
		if event == "thought" || event == "open" {
			job.publish(event, payload)
		}
	}
	parsed, raw, repoInfo, err := h.jobs.runner()(ctx, issue, request, emit)
	if err != nil {
		msg := "AI 分析失败: " + err.Error()
		if ctx.Err() == context.DeadlineExceeded {
			msg = "AI 分析超时（10 分钟），请重试或简化需求描述"
		} else if ctx.Err() != nil {
			msg = "分析已取消"
		}
		job.fail(msg)
		job.publish("error", map[string]string{"error": msg})
		return
	}
	sessionID := ""
	if repoInfo != nil {
		if sid, ok := repoInfo["sessionId"].(string); ok {
			sessionID = sid
		}
	}
	result := map[string]any{
		"result":     raw,
		"parsed":     parsed,
		"repository": repoInfo,
		"sessionId":  sessionID,
		"jobId":      composerPublicJobID(job.issueID),
		"startedAt":  job.startedAt,
	}
	job.mu.Lock()
	job.result = result
	job.mu.Unlock()
	job.publish("done", result)
}

func (h *streamHandlers) handleComposerClarify(c *gin.Context, issue domain.Issue, request chatClarifyRequest) {
	job, err := h.ensureComposerJob(issue, request)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	streaming := strings.Contains(strings.ToLower(c.GetHeader("Accept")), "text/event-stream")
	if streaming {
		h.subscribeClarifySSE(c, job)
		return
	}
	select {
	case <-job.done:
		job.mu.Lock()
		errMsg, result := job.err, job.result
		job.mu.Unlock()
		if errMsg != "" {
			status := http.StatusInternalServerError
			if strings.Contains(errMsg, "超时") {
				status = http.StatusGatewayTimeout
			}
			c.JSON(status, map[string]string{"error": errMsg})
			return
		}
		c.JSON(http.StatusOK, result)
	case <-c.Request.Context().Done():
		return
	}
}

func (h *streamHandlers) attachChatClarify(c *gin.Context) {
	rawID := strings.TrimSpace(c.Query("jobId"))
	if rawID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "jobId is required"})
		return
	}
	if h.jobs == nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "没有正在进行的分析"})
		return
	}
	jobID := composerJobKey(rawID)
	h.jobs.mu.Lock()
	job := h.jobs.jobs[jobID]
	h.jobs.mu.Unlock()
	if job == nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "没有正在进行的分析"})
		return
	}
	h.subscribeClarifySSE(c, job)
}

func (h *streamHandlers) markIssueAnalyzing(issueID, message string, request chatClarifyRequest) error {
	_, err := h.repo.PatchIssue(issueID, func(issue *domain.Issue) error {
		if issue.Clarification.StartedAt.IsZero() {
			issue.Clarification.StartedAt = time.Now().UTC()
		}
		history := issue.Clarification.History
		if len(request.History) > 0 {
			history = make([]domain.ClarificationMessage, 0, len(request.History))
			for _, item := range request.History {
				history = append(history, domain.ClarificationMessage{Role: item.Role, Content: item.Content})
			}
		}
		if message != "" && (len(history) == 0 || !strings.EqualFold(history[len(history)-1].Role, "user") || history[len(history)-1].Content != message) {
			history = append(history, domain.ClarificationMessage{Role: "user", Content: message})
		}
		issue.Clarification.History = history
		issue.Clarification.Status = domain.ClarificationAnalyzing
		return nil
	})
	return err
}

func (h *streamHandlers) runClarifyJob(ctx context.Context, job *clarifyJob, issueID string, request chatClarifyRequest) {
	defer func() {
		job.cancel()
		close(job.done)
		time.AfterFunc(2*time.Minute, func() {
			h.jobs.mu.Lock()
			if current := h.jobs.jobs[issueID]; current == job {
				delete(h.jobs.jobs, issueID)
			}
			h.jobs.mu.Unlock()
		})
	}()
	select {
	case h.jobs.tokens <- struct{}{}:
		defer func() { <-h.jobs.tokens }()
	case <-ctx.Done():
		job.fail("澄清已取消")
		h.revertAnalyzing(issueID, ctx.Err())
		return
	}
	issue, ok := h.repo.Issue(issueID)
	if !ok {
		job.fail("issue not found")
		return
	}
	emit := func(event string, payload any) {
		if event == "thought" || event == "open" {
			job.publish(event, payload)
		}
	}
	parsed, raw, repoInfo, err := h.jobs.runner()(ctx, issue, request, emit)
	if err != nil {
		msg := err.Error()
		if ctx.Err() == context.DeadlineExceeded {
			msg = "AI 分析超时（10 分钟），请重试或简化需求描述"
		} else if ctx.Err() != nil {
			msg = "澄清已取消"
		}
		job.fail(msg)
		h.revertAnalyzing(issueID, err)
		job.publish("error", map[string]string{"error": msg})
		return
	}
	stored, persistErr := h.persistClarifyOutcome(issueID, parsed, raw)
	if persistErr != nil {
		job.fail(persistErr.Error())
		job.publish("error", map[string]string{"error": persistErr.Error()})
		return
	}
	result := map[string]any{"result": raw, "parsed": clarificationAPIPayload(stored.Clarification), "repository": repoInfo, "issue": stored}
	job.mu.Lock()
	job.result = result
	job.mu.Unlock()
	job.publish("done", result)
}

func (j *clarifyJob) fail(msg string) {
	j.mu.Lock()
	j.err = msg
	j.mu.Unlock()
}

func (j *clarifyJob) publish(event string, payload any) {
	j.mu.Lock()
	j.seq++
	item := clarifyJobEvent{Seq: j.seq, Type: event, Payload: payload}
	if event == "thought" || event == "open" {
		j.events = append(j.events, item)
		if len(j.events) > clarifyThoughtCap {
			j.events = j.events[len(j.events)-clarifyThoughtCap:]
		}
	}
	subs := make([]chan clarifyJobEvent, 0, len(j.subs))
	for ch := range j.subs {
		subs = append(subs, ch)
	}
	j.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- item:
		default:
		}
	}
}

func (j *clarifyJob) subscribe() (chan clarifyJobEvent, []clarifyJobEvent, map[string]any, string, bool) {
	ch := make(chan clarifyJobEvent, 16)
	j.mu.Lock()
	replay := append([]clarifyJobEvent(nil), j.events...)
	result := j.result
	errMsg := j.err
	done := j.finished()
	if !done {
		j.subs[ch] = struct{}{}
	}
	j.mu.Unlock()
	return ch, replay, result, errMsg, done
}

func (j *clarifyJob) unsubscribe(ch chan clarifyJobEvent) {
	j.mu.Lock()
	delete(j.subs, ch)
	j.mu.Unlock()
}

func (h *streamHandlers) cancelClarifyJob(issueID string) bool {
	h.jobs.mu.Lock()
	job := h.jobs.jobs[issueID]
	h.jobs.mu.Unlock()
	if job == nil || job.finished() {
		return false
	}
	job.cancel()
	return true
}

func (h *streamHandlers) revertAnalyzing(issueID string, cause error) {
	_, _ = h.repo.PatchIssue(issueID, func(issue *domain.Issue) error {
		if issue.Clarification.Status != domain.ClarificationAnalyzing {
			return nil
		}
		if len(issue.Clarification.Questions) > 0 {
			issue.Clarification.Status = domain.ClarificationInProgress
		} else {
			issue.Clarification.Status = domain.ClarificationNeedsUser
		}
		if cause != nil && cause != context.Canceled && cause != context.DeadlineExceeded {
			issue.Clarification.WriteBackError = cause.Error()
		}
		return nil
	})
}

func (h *streamHandlers) persistClarifyOutcome(issueID string, parsed any, raw string) (domain.Issue, error) {
	model := parseClarifyModel(parsed, raw)
	if err := model.userFacingOK(); err != nil {
		return domain.Issue{}, fmt.Errorf("澄清结果无效，未保存原文: %w", err)
	}
	stored, err := h.saveClarificationAssessment(issueID, model)
	if err != nil {
		return stored, err
	}
	if stored.Clarification.Status != domain.ClarificationReady {
		return stored, nil
	}
	if stored.ExternalRef == nil || (stored.Source != "gongfeng" && stored.Source != "tapd") || h.sync == nil {
		if stored.Clarification.WriteBackStatus == "pending" {
			return h.repo.PatchIssue(issueID, func(issue *domain.Issue) error {
				issue.Clarification.WriteBackStatus = "skipped"
				return nil
			})
		}
	}
	return stored, nil
}

type clarifyModelResult struct {
	Summary    string                         `json:"summary"`
	Questions  []domain.ClarificationQuestion `json:"questions"`
	Ready      bool                           `json:"ready"`
	Assessment struct {
		Dimensions map[string]int `json:"dimensions"`
		Missing    []string       `json:"missing"`
	} `json:"assessment"`
	raw map[string]any
}

func parseClarifyModel(parsed any, raw string) clarifyModelResult {
	var result clarifyModelResult
	if parsed != nil {
		b, _ := json.Marshal(parsed)
		_ = json.Unmarshal(b, &result)
		_ = json.Unmarshal(b, &result.raw)
	}
	if result.raw == nil {
		if extracted, extractedRaw := extractClarifyResultJSON(raw); extracted != nil && extractedRaw != "" {
			b, _ := json.Marshal(extracted)
			_ = json.Unmarshal(b, &result)
			_ = json.Unmarshal(b, &result.raw)
		}
	}
	result.Questions = coerceClarifyQuestions(result.Questions, result.raw, parsed)
	if proto := extractClarifyProtocolObject(result.Summary); proto != nil {
		if len(result.Questions) == 0 {
			result.Questions = coerceClarifyQuestions(nil, proto, proto)
		}
		if s, ok := proto["summary"].(string); ok && strings.TrimSpace(s) != "" && !clarifyThoughtShouldHide(s) {
			result.Summary = strings.TrimSpace(s)
		}
	}
	if clarifyThoughtShouldHide(result.Summary) {
		result.Summary = ""
	}
	if result.raw == nil {
		return result
	}
	result.raw["summary"] = result.Summary
	result.raw["questions"] = result.Questions
	result.raw["ready"] = result.Ready
	return result
}

func (r clarifyModelResult) userFacingOK() error {
	if r.raw == nil {
		return fmt.Errorf("模型未返回 JSON")
	}
	if _, hasCalls := r.raw["mcpCalls"]; hasCalls && !r.Ready && len(r.Questions) == 0 {
		return fmt.Errorf("既没有 questions 也没有结论")
	}
	if r.Ready {
		if strings.TrimSpace(r.Summary) == "" {
			return fmt.Errorf("ready 结论缺少 summary")
		}
		return nil
	}
	return validateSubmitClarification(r)
}

func clarificationAPIPayload(c domain.ClarificationAssessment) map[string]any {
	return map[string]any{
		"summary":   c.Summary,
		"questions": c.Questions,
		"ready":     c.Status == domain.ClarificationReady,
		"assessment": map[string]any{
			"dimensions": c.Dimensions,
			"missing":    c.Missing,
		},
	}
}

func coerceClarifyQuestions(typed []domain.ClarificationQuestion, raw map[string]any, parsed any) []domain.ClarificationQuestion {
	var src any
	if raw != nil {
		src = raw["questions"]
	}
	if src == nil {
		if m, ok := parsed.(map[string]any); ok {
			src = m["questions"]
		}
	}
	if src == nil && len(typed) > 0 {
		out := make([]domain.ClarificationQuestion, 0, len(typed))
		for _, q := range typed {
			q.Options = coerceClarifyOptions(q.Options)
			if strings.TrimSpace(q.Question) == "" && len(q.Options) == 0 {
				continue
			}
			out = append(out, q)
		}
		return out
	}
	return coerceClarifyQuestionList(src)
}

func coerceClarifyQuestionList(src any) []domain.ClarificationQuestion {
	items, ok := anySlice(src)
	if !ok {
		return nil
	}
	out := make([]domain.ClarificationQuestion, 0, len(items))
	for i, item := range items {
		q := domain.ClarificationQuestion{ID: fmt.Sprintf("q%d", i+1)}
		switch v := item.(type) {
		case string:
			q.Question = strings.TrimSpace(v)
		case map[string]any:
			q.ID = firstNonEmpty(stringValueOf(v["id"]), q.ID)
			q.Question = firstNonEmpty(stringValueOf(v["question"]), stringValueOf(v["prompt"]), stringValueOf(v["text"]))
			q.Options = coerceClarifyOptions(v["options"])
			if multi, ok := v["multiSelect"].(bool); ok {
				q.MultiSelect = multi
			}
			if freeText, ok := v["freeText"].(bool); ok {
				q.FreeText = freeText
			}
		default:
			continue
		}
		if strings.TrimSpace(q.Question) == "" && len(q.Options) == 0 {
			continue
		}
		out = append(out, q)
	}
	return out
}

func coerceClarifyOptions(src any) []string {
	switch v := src.(type) {
	case nil:
		return nil
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil
		}
		var arr []any
		if json.Unmarshal([]byte(s), &arr) == nil {
			return coerceClarifyOptions(arr)
		}
		if label := clarifyOptionLabel(s); label != "" {
			return []string{label}
		}
		return nil
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, coerceClarifyOptions(item)...)
		}
		return out
	default:
		items, ok := anySlice(v)
		if !ok {
			if label := clarifyOptionLabel(v); label != "" {
				return []string{label}
			}
			return nil
		}
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, coerceClarifyOptions(item)...)
		}
		return out
	}
}

func clarifyOptionLabel(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return ""
		}
		if looksLikeJSONDump(s) {
			var inner any
			if json.Unmarshal([]byte(s), &inner) == nil {
				if label := clarifyOptionLabel(inner); label != "" {
					return label
				}
			}
			return ""
		}
		return s
	case map[string]any:
		for _, key := range []string{"label", "text", "value", "title", "question", "name"} {
			if label := clarifyOptionLabel(t[key]); label != "" {
				return label
			}
		}
		return ""
	case []any, []string:
		return ""
	default:
		s := strings.TrimSpace(fmt.Sprint(t))
		if looksLikeJSONDump(s) {
			return ""
		}
		return s
	}
}

func looksLikeJSONDump(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return false
	}
	switch s[0] {
	case '{', '[':
		var v any
		return json.Unmarshal([]byte(s), &v) == nil
	default:
		return false
	}
}

func anySlice(v any) ([]any, bool) {
	switch t := v.(type) {
	case []any:
		return t, true
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out, true
	default:
		return nil, false
	}
}

func stringValueOf(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	default:
		s := strings.TrimSpace(fmt.Sprint(t))
		if s == "" || s == "<nil>" {
			return ""
		}
		return s
	}
}

func (h *streamHandlers) saveClarificationAssessment(issueID string, result clarifyModelResult) (domain.Issue, error) {
	encoded, _ := json.Marshal(stripClarifyMCPCalls(result.raw))
	stored, err := h.repo.PatchIssue(issueID, func(issue *domain.Issue) error {
		history := append([]domain.ClarificationMessage(nil), issue.Clarification.History...)
		history = append(history, domain.ClarificationMessage{Role: "assistant", Content: string(encoded)})
		assessment := issue.Clarification
		assessment.Summary = strings.TrimSpace(result.Summary)
		assessment.History = history
		assessment.Questions = result.Questions
		if result.Ready {
			if assessment.Summary == "" {
				assessment.Summary = strings.TrimSpace(rawSummaryFallback(result.raw))
			}
			if assessment.Summary == "" {
				return fmt.Errorf("clarification summary is required")
			}
			assessment.Status = domain.ClarificationReady
			issue.Description = appendClarificationDescription(issue.Description, assessment.Summary)
			project, found := h.repo.Project(issue.ProjectID)
			dimensions := clarificationRatingDimensions(*issue, project, found)
			score, rated := scoreClarificationDimensions(dimensions, result.Assessment.Dimensions)
			assessment.Score = score
			assessment.Dimensions = rated
			assessment.Missing = trimClarificationMissing(result.Assessment.Missing)
			if len(assessment.Missing) == 0 {
				for _, dim := range dimensions {
					if rated[dim.ID] < 100 {
						assessment.Missing = append(assessment.Missing, dim.ID)
					}
				}
			}
			if isBugStyleWorkItem(*issue) {
				assessment.Passed = true
			} else {
				policy := domain.DefaultClarificationPolicy()
				if found {
					policy = domain.EffectiveClarificationPolicy(project.Clarification)
				}
				assessment.Passed = domain.ClarificationPassed(policy, score)
			}
			assessment.AssessedAt = time.Now().UTC()
			if !isBugStyleWorkItem(*issue) {
				assessment.Gated = true
			}
			promoteClarifiedIssueTodo(issue)
			fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(assessment.Summary)))
			if assessment.WriteBackStatus != "succeeded" || assessment.WriteBackFingerprint != fingerprint {
				assessment.WriteBackStatus = "pending"
				assessment.WriteBackError = ""
				assessment.WriteBackFingerprint = fingerprint
			}
		} else {
			assessment.Status = domain.ClarificationInProgress
			if len(result.Assessment.Dimensions) > 0 {
				project, found := h.repo.Project(issue.ProjectID)
				dimensions := clarificationRatingDimensions(*issue, project, found)
				score, rated := scoreClarificationDimensions(dimensions, result.Assessment.Dimensions)
				assessment.Score = score
				assessment.Dimensions = rated
				assessment.Missing = trimClarificationMissing(result.Assessment.Missing)
			}
		}
		issue.Clarification = assessment
		return nil
	})
	if err != nil {
		return stored, err
	}
	return stored, nil
}

func rawSummaryFallback(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	if s, ok := raw["summary"].(string); ok {
		return s
	}
	return ""
}

func (h *streamHandlers) handleIssueClarify(c *gin.Context, issue domain.Issue, request chatClarifyRequest) {
	job, err := h.ensureClarifyJob(issue, request)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "项目未配置") || strings.Contains(err.Error(), "责任人") {
			status = http.StatusConflict
		}
		c.JSON(status, map[string]string{"error": err.Error()})
		return
	}
	streaming := strings.Contains(strings.ToLower(c.GetHeader("Accept")), "text/event-stream")
	if streaming {
		h.subscribeClarifySSE(c, job)
		return
	}
	select {
	case <-job.done:
		job.mu.Lock()
		errMsg, result := job.err, job.result
		job.mu.Unlock()
		if errMsg != "" {
			status := http.StatusInternalServerError
			if strings.Contains(errMsg, "超时") {
				status = http.StatusGatewayTimeout
			}
			c.JSON(status, map[string]string{"error": errMsg})
			return
		}
		c.JSON(http.StatusOK, result)
	case <-c.Request.Context().Done():
		return
	}
}

func (h *streamHandlers) attachIssueClarify(c *gin.Context, issue domain.Issue) {
	h.jobs.mu.Lock()
	job := h.jobs.jobs[issue.ID]
	h.jobs.mu.Unlock()
	if job == nil || job.finished() {
		if issue.Clarification.Status == domain.ClarificationAnalyzing {
			req := chatClarifyRequest{Message: domain.ClarificationKickoff, History: historyToClarify(issue.Clarification.History)}
			var err error
			job, err = h.ensureClarifyJob(issue, req)
			if err != nil {
				c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
		} else {
			c.JSON(http.StatusNotFound, map[string]string{"error": "没有正在进行的澄清"})
			return
		}
	}
	h.subscribeClarifySSE(c, job)
}

func (h *streamHandlers) subscribeClarifySSE(c *gin.Context, job *clarifyJob) {
	w := c.Writer
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	after, _ := strconv.Atoi(c.Query("after"))
	ch, replay, result, errMsg, done := job.subscribe()
	if ch != nil {
		defer job.unsubscribe(ch)
	}
	send := func(event string, payload any) {
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		c.Writer.Flush()
	}
	if strings.HasPrefix(job.issueID, composerJobPrefix) {
		send("job", map[string]string{"jobId": composerPublicJobID(job.issueID), "startedAt": job.startedAt.Format(time.RFC3339Nano)})
	}
	for _, item := range replay {
		if item.Seq <= after {
			continue
		}
		send(item.Type, item.Payload)
	}
	if done {
		if errMsg != "" {
			send("error", map[string]string{"error": errMsg})
			return
		}
		if result != nil {
			send("done", result)
		}
		return
	}
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-job.done:
			job.mu.Lock()
			errMsg, result = job.err, job.result
			job.mu.Unlock()
			if errMsg != "" {
				send("error", map[string]string{"error": errMsg})
				return
			}
			if result != nil {
				send("done", result)
			}
			return
		case item := <-ch:
			if item.Seq <= after {
				continue
			}
			send(item.Type, item.Payload)
			if item.Type == "done" || item.Type == "error" {
				return
			}
		case <-ping.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			c.Writer.Flush()
		}
	}
}
