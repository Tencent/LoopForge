package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func TestInflightTrackerWaitsForEveryTaskAndHonoursDeadline(t *testing.T) {
	tracker := newInflightTracker()
	if err := tracker.Wait(context.Background()); err != nil {
		t.Fatal("idle tracker must not block", err)
	}
	first, second := tracker.Add(), tracker.Add()
	if tracker.Count() != 2 {
		t.Fatal(tracker.Count())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := tracker.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("wait returned before tasks finished", err)
	}
	first()
	first() // completion is idempotent
	if tracker.Count() != 1 {
		t.Fatal("double completion miscounted", tracker.Count())
	}
	waited := make(chan error, 1)
	go func() { waited <- tracker.Wait(context.Background()) }()
	select {
	case <-waited:
		t.Fatal("wait returned with a task still running")
	case <-time.After(30 * time.Millisecond):
	}
	second()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not observe the last completion")
	}
}

func TestBackgroundGenerationIsTrackedUntilItFinishes(t *testing.T) {
	h, u := workbenchFixture(t)
	release := make(chan struct{})
	entered := make(chan struct{})
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		close(entered)
		select {
		case <-release:
			return "交接后完成", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	before := inflightTasks.Count()
	c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"drain-track","messages":[{"role":"user","content":"长任务"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	<-entered
	if got := inflightTasks.Count(); got != before+1 {
		t.Fatalf("generation not tracked: before=%d now=%d", before, got)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for inflightTasks.Count() != before {
		if time.Now().After(deadline) {
			t.Fatal("generation never left the tracker")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if g, _ := h.repo.AssistantGeneration(u.ID, "drain-track"); g.Status != "completed" {
		t.Fatal("tracked generation did not complete", g.Status, g.Error)
	}
}

func TestBackgroundControlsAreNilSafeAndRecordHandover(t *testing.T) {
	bg := &Background{}
	bg.StopClaiming()
	bg.StopClaiming()
	bg.Stop()
	var nilBG *Background
	nilBG.StopClaiming()
	nilBG.HandedOver()
	nilBG.Stop()
	if nilBG.handedOverToSuccessor() {
		t.Fatal("nil background reported a handover")
	}
	bg.HandedOver()
	if !bg.handedOverToSuccessor() {
		t.Fatal("handover not recorded")
	}
}

func TestAssistantSSEStreamsGenerationOwnedByAnotherProcess(t *testing.T) {
	old := assistantPersistedPollInterval
	assistantPersistedPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { assistantPersistedPollInterval = old })
	h, u := workbenchFixture(t)
	g := domain.AssistantGeneration{ID: "remote-gen", SessionID: "remote-session", UserID: u.ID, ActorID: u.ID, ProjectID: "workbench", Status: "running", StartedAt: time.Now().UTC()}
	pending := domain.AssistantSession{ID: "remote-session", ProjectID: "workbench", Messages: []domain.AssistantMessage{{Role: "user", Content: "长任务"}}}
	if err := h.repo.BeginAssistantGeneration(g, pending); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := gin.CreateTestContext(w)
		c.Request = r.WithContext(context.WithValue(r.Context(), requestUserContextKey{}, domainUserToAuth(u)))
		c.Params = gin.Params{{Key: "id", Value: "remote-session"}}
		h.generation(c)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Get(server.URL + "?stream=1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	read := func() (domain.AssistantGeneration, bool) {
		t.Helper()
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var frame domain.AssistantGeneration
				if err := json.Unmarshal([]byte(line[6:]), &frame); err != nil {
					t.Fatal(err)
				}
				return frame, true
			}
		}
		return domain.AssistantGeneration{}, false
	}
	first, ok := read()
	if !ok || first.ID != "remote-gen" || first.Status != "running" {
		t.Fatalf("first frame: %+v", first)
	}
	raw, _ := json.Marshal(assistantStreamEvent{Kind: "answer", Text: "进度"})
	g.Events = []domain.AssistantGenerationEvent{{Sequence: 1, Data: raw}}
	if err := h.repo.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	update, ok := read()
	if !ok || update.Status != "running" || len(update.Events) != 1 || update.Events[0].Sequence != 1 {
		t.Fatalf("checkpoint from the owning process was not pushed: %+v", update)
	}
	g.Status = "completed"
	g.Reply = json.RawMessage(`{"message":"完成"}`)
	if err := h.repo.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	final, ok := read()
	if !ok || final.Status != "completed" || len(final.Events) != 0 {
		t.Fatalf("terminal state not pushed: %+v", final)
	}
	if _, more := read(); more {
		t.Fatal("stream kept going after the terminal frame")
	}
}

func TestStopRequestFromAnotherProcessCancelsGeneration(t *testing.T) {
	owner, u := workbenchFixture(t)
	entered := make(chan struct{})
	owner.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}
	c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"cross-stop","messages":[{"role":"user","content":"长任务"}]}`))
	owner.startGeneration(c)
	if rec.Code != 202 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	<-entered
	running, _ := owner.repo.AssistantGeneration(u.ID, "cross-stop")
	successor := &assistantHandlers{deps: owner.deps}
	stop, stopRec := workbenchContext(u, "POST", "/api/assistant/sessions/cross-stop/generation?generationId="+running.ID, bytes.NewBufferString(`{}`))
	stop.Params = gin.Params{{Key: "id", Value: "cross-stop"}}
	successor.generation(stop)
	if stopRec.Code != 200 {
		t.Fatal(stopRec.Code, stopRec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, _ := owner.repo.AssistantGeneration(u.ID, "cross-stop")
		if g.Status == "cancelled" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("owner process ignored the persisted stop request", g.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAgentToolHandlerOnlyExposesAgentToolRoutes(t *testing.T) {
	var hits []string
	h := AgentToolHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits = append(hits, r.URL.Path) }))
	for path, want := range map[string]int{"/internal/agent-tools/tok/mcp": 200, "/api/health": 404, "/internal/workspace-checkpoint": 404} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, nil))
		if rec.Code != want {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	if len(hits) != 1 {
		t.Fatal(hits)
	}
}

func TestRunAfterGateDefersUntilClosed(t *testing.T) {
	ran := make(chan struct{}, 2)
	runAfterGate(nil, func() { ran <- struct{}{} })
	select {
	case <-ran:
	default:
		t.Fatal("nil gate must run synchronously")
	}
	gate := make(chan struct{})
	runAfterGate(gate, func() { ran <- struct{}{} })
	select {
	case <-ran:
		t.Fatal("ran before gate closed")
	case <-time.After(30 * time.Millisecond):
	}
	close(gate)
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("did not run after gate closed")
	}
}

func TestSuccessorFollowsPredecessorClarificationWithoutStartingAnother(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	gate := make(chan struct{})
	h := &streamHandlers{deps: &deps{repo: st}, clarifyCancels: map[string]context.CancelFunc{}}
	h.jobs = newClarifyHub(h)
	h.predecessor = gate
	calls := 0
	h.jobs.exec = func(context.Context, domain.Issue, chatClarifyRequest, func(string, any)) (any, string, map[string]any, error) {
		calls++
		return nil, "", nil, errors.New("must not run while predecessor is alive")
	}
	issue := domain.Issue{ID: "handover-clarify", Title: "交接中的澄清", Clarification: domain.ClarificationAssessment{Status: domain.ClarificationAnalyzing}}
	if err := st.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ensureClarifyJob(issue, chatClarifyRequest{Message: "继续"}); err == nil {
		t.Fatal("successor started a duplicate clarification")
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Request = httptest.NewRequest("GET", "/api/issues/handover-clarify/chat-clarify", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { defer close(done); h.attachIssueClarify(c, issue) }()
	time.Sleep(200 * time.Millisecond)
	if _, err := st.PatchIssue(issue.ID, func(i *domain.Issue) error {
		i.Clarification.Status = domain.ClarificationNeedsUser
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("follower did not observe the predecessor's result")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: done") || !strings.Contains(body, "handover-clarify") || calls != 0 {
		t.Fatalf("calls=%d body=%s", calls, body)
	}
}
