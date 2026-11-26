package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

const evoTestCandidateMark = "EVO_CANDIDATE_MARK"

type evoTestEnv struct {
	store  *repo.Store
	engine *engine.Engine
	h      *evolutionHandlers
	router *gin.Engine
	user   domain.User
	agent  domain.AgentDefinition
	wf     domain.WorkflowDefinition
}

func newEvoTestEnv(t *testing.T) *evoTestEnv {
	t.Helper()
	t.Setenv("AGENTFLOW_SESSION_SECRET", "evolution-test-session-secret")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	e := engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	if err := s.PutUser(domain.User{StaffID: 3101, LoginName: "evo-owner", ChineseName: "进化"}); err != nil {
		t.Fatal(err)
	}
	user, _ := s.UserByLoginName("evo-owner")
	if _, err := s.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{CodeBuddyAPIKey: "ck_evo_test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProject(domain.Project{ID: "evo-p", Name: "进化测试"}); err != nil {
		t.Fatal(err)
	}
	agent := domain.AgentDefinition{ID: "evo-coder", Name: "进化开发", ProjectID: "evo-p", ExecutorID: "mock", Prompt: "原始正文：请完成开发任务并自测。"}
	if err := s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "evo-wf", Name: "进化流程", ProjectID: "evo-p", SandboxProfileID: repo.TestNativeSandboxID, Nodes: []domain.WorkflowNode{
		{ID: "code", Name: "开发", Type: "agent", AgentID: agent.ID, ExecutorID: "mock", MaxAttempts: 1},
	}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Workflow(wf.ID)
	d := &deps{repo: s, engine: e}
	h := newEvolutionHandlers(d)
	g := gin.New()
	registerEvolutionRoutes(g, h)
	t.Cleanup(func() {
		for _, p := range s.EvoProposals("", 50) {
			for _, r := range p.Replays {
				if r.RunID != "" {
					_ = e.Cancel(r.RunID)
				}
			}
		}
	})
	return &evoTestEnv{store: s, engine: e, h: h, router: g, user: user, agent: agent, wf: stored}
}

func (env *evoTestEnv) seedRun(t *testing.T, id, status, nodeStatus, nodeError string) domain.Run {
	t.Helper()
	now := time.Now().UTC()
	run := domain.Run{
		ID: id, WorkflowID: env.wf.ID, Status: status, Input: "实现登录页 " + id,
		WorkflowSnapshot: env.wf,
		ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
		Nodes:            map[string]*domain.NodeRun{"code": {NodeID: "code", Status: nodeStatus, Error: nodeError, FinishedAt: now}},
		CreatedAt:        now, UpdatedAt: now,
	}
	if err := env.store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return run
}

func (env *evoTestEnv) waitProposal(t *testing.T, id string, want ...string) domain.EvoProposal {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var p domain.EvoProposal
	for time.Now().Before(deadline) {
		env.h.sweepReplays(time.Now().UTC())
		p, _ = env.store.EvoProposal(id)
		for _, status := range want {
			if p.Status == status {
				return p
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	var states []string
	for _, r := range env.store.EvoReplays(id) {
		state := fmt.Sprintf("%s/%s src=%s run=%s status=%s outcome=%s err=%q", r.ID, r.Group, r.SourceRunID, r.RunID, r.Status, r.Outcome, r.Error)
		if run, ok := env.store.RunState(r.RunID); ok {
			nodes := []string{}
			for nid, n := range run.Nodes {
				nodes = append(nodes, nid+":"+n.Status+":"+n.Error)
			}
			state += fmt.Sprintf(" runStatus=%s nodes=%v", run.Status, nodes)
		}
		states = append(states, state)
	}
	t.Fatalf("proposal %s status=%s want %v; error=%q summary=%+v replays:\n%s", id, p.Status, want, p.Error, p.Summary, strings.Join(states, "\n"))
	return p
}

func TestEvolutionDiagnoseMergesSnoozesAndArchives(t *testing.T) {
	env := newEvoTestEnv(t)
	env.seedRun(t, "evo-m-1", "failed", "failed", "编译失败")
	env.seedRun(t, "evo-m-2", "failed", "failed", "编译失败")
	type diagResp struct {
		Findings []domain.EvoFinding `json:"findings"`
		Archived int                 `json:"archived"`
	}
	diagnose := func() diagResp {
		return requestJSON[diagResp](t, env.router, http.MethodPost, "/api/evolution/diagnose", map[string]any{"projectId": "evo-p"}, http.StatusOK)
	}
	first := diagnose()
	if len(first.Findings) != 1 || first.Findings[0].Triage == nil || first.Findings[0].Triage.Runs != 2 {
		t.Fatalf("diagnose with triage = %+v", first)
	}
	f := first.Findings[0]

	snoozed := requestJSON[domain.EvoFinding](t, env.router, http.MethodPost, "/api/evolution/findings/"+f.ID+"/snooze", map[string]any{"days": 7}, http.StatusOK)
	if !snoozed.SnoozedUntil.After(time.Now().AddDate(0, 0, 6)) {
		t.Fatalf("snooze until = %v", snoozed.SnoozedUntil)
	}
	again := diagnose()
	if len(again.Findings) != 1 || again.Findings[0].ID != f.ID || again.Findings[0].SnoozedUntil.IsZero() {
		t.Fatalf("re-diagnosis must update the same finding and keep the snooze: %+v", again.Findings)
	}

	proposed, _ := env.store.EvoFinding(f.ID)
	proposed.Status, proposed.ProposalID = domain.EvoFindingProposed, "evp-x"
	if err := env.store.PutEvoFinding(proposed); err != nil {
		t.Fatal(err)
	}
	ghost := domain.EvoFinding{ID: "evf-ghost", ProjectID: "evo-p", AssetKind: domain.EvoAssetRule, AssetID: "gone", Status: domain.EvoFindingOpen, Title: "旧问题"}
	if err := env.store.PutEvoFinding(ghost); err != nil {
		t.Fatal(err)
	}
	third := diagnose()
	if len(third.Findings) != 1 || third.Findings[0].ID != f.ID || third.Findings[0].Status != domain.EvoFindingProposed || third.Findings[0].ProposalID != "evp-x" {
		t.Fatalf("a proposed finding must not be duplicated: %+v", third.Findings)
	}
	if open := env.store.EvoFindings("evo-p", domain.EvoFindingOpen); len(open) != 0 {
		t.Fatalf("no open duplicates expected, got %+v", open)
	}
	if g, _ := env.store.EvoFinding("evf-ghost"); g.Status != domain.EvoFindingResolved || g.CloseNote == "" || third.Archived != 1 {
		t.Fatalf("finding without new signals should be archived: %+v archived=%d", g, third.Archived)
	}

	ghost.ID, ghost.Status = "evf-ghost-2", domain.EvoFindingOpen
	_ = env.store.PutEvoFinding(ghost)
	dismissed := requestJSON[domain.EvoFinding](t, env.router, http.MethodPost, "/api/evolution/findings/evf-ghost-2/dismiss", map[string]any{"reason": "外部依赖故障"}, http.StatusOK)
	if dismissed.Status != domain.EvoFindingDismissed || dismissed.CloseNote != "外部依赖故障" {
		t.Fatalf("dismiss reason = %+v", dismissed)
	}
	requestJSON[map[string]any](t, env.router, http.MethodPost, "/api/evolution/findings/evf-ghost-2/snooze", map[string]any{"days": 3}, http.StatusConflict)
}

func TestEvolutionProposalReplayReleaseRollback(t *testing.T) {
	env := newEvoTestEnv(t)
	env.seedRun(t, "evo-src-fail-1", "failed", "failed", "单元测试失败：缺少边界校验")
	env.seedRun(t, "evo-src-fail-2", "failed", "failed", "编译失败")
	env.seedRun(t, "evo-src-ok", "succeeded", "succeeded", "")

	var mu sync.Mutex
	var seenPrompt string
	setEvoImproveForTest(func(_ context.Context, _ domain.EvoProposal, system, prompt string, user domain.User) (string, error) {
		mu.Lock()
		seenPrompt = prompt
		mu.Unlock()
		if user.CodeBuddyAPIKey == "" {
			t.Errorf("improver must run with the requester's own key")
		}
		return `{"title":"补充自测要求","rationale":"失败都缺少自测","content":"原始正文：请完成开发任务并自测。\n提交前必须运行单元测试。` + evoTestCandidateMark + `","review":{"problem":"提交前缺少自测","reason":"增加测试步骤","changes":[{"title":"提交前执行测试","before":"","after":"提交前必须运行单元测试。","sourceRunIds":["evo-src-fail-1","evo-src-ok"]}]}}`, nil
	})
	t.Cleanup(func() { setEvoImproveForTest(nil) })

	diag := requestJSON[struct {
		Findings []domain.EvoFinding `json:"findings"`
		Runs     int                 `json:"runs"`
	}](t, env.router, http.MethodPost, "/api/evolution/diagnose", map[string]any{"projectId": "evo-p"}, http.StatusOK)
	if len(diag.Findings) != 1 || diag.Findings[0].AssetID != env.agent.ID || len(diag.Findings[0].SampleRunIDs) != 2 {
		t.Fatalf("diagnose = %+v", diag)
	}
	finding := diag.Findings[0]

	created := requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals", map[string]any{"findingId": finding.ID}, http.StatusAccepted)
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals", map[string]any{"findingId": finding.ID}, http.StatusOK)

	p := env.waitProposal(t, created.ID, domain.EvoProposalPending, domain.EvoProposalFailed)
	if p.Status != domain.EvoProposalPending || !p.Summary.Passed || p.Summary.Fixed != 2 || p.Summary.Unchanged != 1 {
		t.Fatalf("proposal should pass replay: status=%s error=%q summary=%+v", p.Status, p.Error, p.Summary)
	}
	if p.Review == nil || len(p.Review.Changes) != 1 || p.Review.Changes[0].After != "提交前必须运行单元测试。" {
		t.Fatalf("grounded review must survive proposal storage: %+v", p.Review)
	}
	for _, id := range p.Review.Changes[0].SourceRunIDs {
		if id == "evo-src-ok" {
			t.Fatal("review must not link a regression sample the improver never saw")
		}
	}
	mu.Lock()
	prompt := seenPrompt
	mu.Unlock()
	if !strings.Contains(prompt, "缺少边界校验") || strings.Contains(prompt, "evo-src-ok") {
		t.Fatalf("improver must see diagnosis failures only:\n%s", prompt)
	}

	groups := map[string]int{}
	for _, r := range p.Replays {
		groups[r.Group]++
		run, ok := env.store.RunState(r.RunID)
		if !ok {
			t.Fatalf("replay run %s missing", r.RunID)
		}
		if !run.ExecutionContext.IsEvolutionReplay() || run.ExecutionContext.EvolutionReplayID != r.ID {
			t.Fatalf("replay run not marked: %+v", run.ExecutionContext)
		}
		if out := run.Nodes["code"].Output; !strings.Contains(out, evoTestCandidateMark) {
			t.Fatalf("replay must use the candidate prompt, output=%q", out)
		}
		pinned := false
		for _, pin := range env.store.RunAssetPins(run.ID) {
			if pin.AssetKind == domain.EvoAssetAgent && pin.AssetID == env.agent.ID && pin.Hash == domain.ContentHash(p.CandidateContent) {
				pinned = true
			}
		}
		if !pinned {
			t.Fatalf("replay run should pin the candidate body: %+v", env.store.RunAssetPins(run.ID))
		}
	}
	if groups[domain.EvoGroupDiagnosis] != 2 || groups[domain.EvoGroupRegression] != 1 {
		t.Fatalf("replay groups = %v", groups)
	}
	if live, _ := env.store.Agent(env.agent.ID); live.Prompt != env.agent.Prompt {
		t.Fatalf("catalog must not change before release: %q", live.Prompt)
	}
	summaries, err := env.store.RunSummariesFor(domain.AnalyticsFilter{ProjectID: "evo-p"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 3 {
		t.Fatalf("replay runs must be hidden from run lists, got %d", len(summaries))
	}
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/runs/"+p.Replays[0].RunID+"/pin-sample", map[string]any{}, http.StatusConflict)

	released := requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+p.ID+"/approve", map[string]any{"note": "ok"}, http.StatusOK)
	if released.Status != domain.EvoProposalReleased || released.ReleasedVersion != 2 {
		t.Fatalf("released = %+v", released)
	}
	live, _ := env.store.Agent(env.agent.ID)
	if !strings.Contains(live.Prompt, evoTestCandidateMark) {
		t.Fatalf("release must write the candidate: %q", live.Prompt)
	}
	if f, _ := env.store.EvoFinding(finding.ID); f.Status != domain.EvoFindingResolved {
		t.Fatalf("finding should be resolved: %+v", f)
	}

	versions := requestJSON[struct {
		CurrentVersion int                   `json:"currentVersion"`
		Versions       []domain.AssetVersion `json:"versions"`
	}](t, env.router, http.MethodGet, "/api/evolution/assets/agent/"+env.agent.ID+"/versions", nil, http.StatusOK)
	if versions.CurrentVersion != 2 || len(versions.Versions) != 2 || versions.Versions[1].Source != domain.EvoVersionBaseline || versions.Versions[0].Source != domain.EvoVersionEvolution {
		t.Fatalf("versions = %+v", versions)
	}

	rolled := requestJSONAuthed[domain.AssetVersion](t, env.router, env.user, http.MethodPost, "/api/evolution/assets/agent/"+env.agent.ID+"/rollback", map[string]any{"version": 1}, http.StatusOK)
	if rolled.Version != 3 || rolled.Source != domain.EvoVersionRollback {
		t.Fatalf("rollback version = %+v", rolled)
	}
	if live, _ := env.store.Agent(env.agent.ID); live.Prompt != env.agent.Prompt {
		t.Fatalf("rollback must restore the baseline: %q", live.Prompt)
	}
	if after, _ := env.store.EvoProposal(p.ID); after.Status != domain.EvoProposalRolledBack {
		t.Fatalf("released proposal should be marked rolled back: %s", after.Status)
	}
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/evolution/assets/agent/"+env.agent.ID+"/rollback", map[string]any{"version": 1}, http.StatusConflict)
}

func TestEvolutionProposalFailsStaticCheckAndReopensFinding(t *testing.T) {
	env := newEvoTestEnv(t)
	env.seedRun(t, "evo-src-fail-a", "failed", "failed", "缺少测试")
	setEvoImproveForTest(func(context.Context, domain.EvoProposal, string, string, domain.User) (string, error) {
		return `{"title":"原样","content":"原始正文：请完成开发任务并自测。"}`, nil
	})
	t.Cleanup(func() { setEvoImproveForTest(nil) })

	created := requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals", map[string]any{"projectId": "evo-p", "assetKind": "agent", "assetId": env.agent.ID}, http.StatusAccepted)
	p := env.waitProposal(t, created.ID, domain.EvoProposalFailed)
	if !strings.Contains(p.Error, "静态检查") {
		t.Fatalf("expected static check failure, got %q", p.Error)
	}
	if f, ok := env.store.EvoFinding(p.FindingID); !ok || f.Status != domain.EvoFindingOpen || f.ProposalID != "" {
		t.Fatalf("finding should reopen: %+v", f)
	}
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+p.ID+"/approve", map[string]any{}, http.StatusConflict)
	if live, _ := env.store.Agent(env.agent.ID); live.Prompt != env.agent.Prompt {
		t.Fatalf("failed proposal must not touch the catalog: %q", live.Prompt)
	}
}

func TestEvolutionQueueAPIStartsFreshAndReauthorizes(t *testing.T) {
	env := newEvoTestEnv(t)
	env.seedRun(t, "queue-source", "failed", "failed", "缺少单测")
	current := env.seedPendingProposal(t, "queue-current")
	f := domain.EvoFinding{ID: "queue-finding", ProjectID: "evo-p", AssetKind: "agent", AssetID: env.agent.ID, AssetName: env.agent.Name,
		Title: "另一个问题", Status: domain.EvoFindingOpen, SampleRunIDs: []string{"queue-source"}}
	if err := env.store.PutEvoFinding(f); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	seenBase := ""
	setEvoImproveForTest(func(_ context.Context, p domain.EvoProposal, _, _ string, _ domain.User) (string, error) {
		mu.Lock()
		calls++
		seenBase = p.BaseContent
		mu.Unlock()
		return fmt.Sprintf(`{"title":"队列改进","content":%q}`, p.BaseContent+"\n补充验证。"+evoTestCandidateMark), nil
	})
	t.Cleanup(func() { setEvoImproveForTest(nil) })
	type queuedResponse struct {
		Status  string              `json:"status"`
		QueueID string              `json:"queueId"`
		Queue   domain.EvoQueueItem `json:"queue"`
	}
	queued := requestJSONAuthed[queuedResponse](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals", map[string]any{"findingId": f.ID}, http.StatusAccepted)
	duplicate := requestJSONAuthed[queuedResponse](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals", map[string]any{"findingId": f.ID}, http.StatusAccepted)
	if queued.Status != "queued" || queued.QueueID == "" || duplicate.QueueID != queued.QueueID || queued.Queue.Title != f.Title {
		t.Fatalf("persistent idempotent queue response: %+v %+v", queued, duplicate)
	}
	mu.Lock()
	if calls != 0 {
		t.Fatal("queued intent must not invoke generator")
	}
	mu.Unlock()
	overview := requestJSON[domain.EvoOverview](t, env.router, http.MethodGet, "/api/evolution/overview?projectId=evo-p", nil, http.StatusOK)
	if len(overview.Queue) != 1 || len(env.store.EvoProposals("evo-p", 100)) != 1 {
		t.Fatalf("overview queue or candidate count: %+v", overview.Queue)
	}
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/evolution/queue/"+queued.QueueID+"/start", nil, http.StatusConflict)
	requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+current.ID+"/reject", nil, http.StatusOK)
	if len(env.store.EvoProposals("evo-p", 100)) != 1 {
		t.Fatal("reject must not automatically start next")
	}
	if _, err := env.store.UpdateUserCredentials(env.user.ID, repo.UserCredentialUpdate{CodeBuddyAPIKeyClear: true}); err != nil {
		t.Fatal(err)
	}
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/evolution/queue/"+queued.QueueID+"/start", nil, http.StatusBadRequest)
	if _, err := env.store.UpdateUserCredentials(env.user.ID, repo.UserCredentialUpdate{CodeBuddyAPIKey: "ck_evo_test"}); err != nil {
		t.Fatal(err)
	}
	live, _ := env.store.Agent(env.agent.ID)
	live.Prompt = "最新正文：请开发、完整校验并自测。"
	if err := env.store.PutAgent(live); err != nil {
		t.Fatal(err)
	}
	started := requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/queue/"+queued.QueueID+"/start", nil, http.StatusAccepted)
	if started.BaseContent != live.Prompt || started.BaseVersion != 1 || started.CandidateContent != "" {
		t.Fatalf("start must snapshot latest live body, no old candidate: %+v", started)
	}
	p := env.waitProposal(t, started.ID, domain.EvoProposalPending, domain.EvoProposalFailed)
	if p.Status != domain.EvoProposalPending || !p.Summary.Passed {
		t.Fatalf("queue start must regenerate and replay: %+v", p)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 || seenBase != live.Prompt {
		t.Fatalf("generator calls=%d base=%q", calls, seenBase)
	}
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/evolution/queue/"+queued.QueueID+"/reject", nil, http.StatusConflict)
}

func TestEvolutionQueuePermissionsAndCrossProjectIsolation(t *testing.T) {
	env := newEvoTestEnv(t)
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	if err := env.store.PutUser(domain.User{StaffID: 3102, LoginName: "queue-viewer"}); err != nil {
		t.Fatal(err)
	}
	viewer, _ := env.store.UserByLoginName("queue-viewer")
	project, _ := env.store.Project("evo-p")
	project.OwnerID = env.user.ID
	project.Members = []domain.ProjectMember{{UserID: viewer.ID, Role: "viewer"}}
	if err := env.store.PutProject(project); err != nil {
		t.Fatal(err)
	}
	shared, _ := env.store.Agent(env.agent.ID)
	shared.Shared = true
	if err := env.store.PutAgent(shared); err != nil {
		t.Fatal(err)
	}
	current := domain.EvoProposal{ID: "private-current", ProjectID: "private-project", AssetKind: "agent", AssetID: env.agent.ID,
		Title: "其他项目私有改进", Status: domain.EvoProposalPending, BaseContent: shared.Prompt, BaseHash: domain.ContentHash(shared.Prompt),
		CandidateContent: shared.Prompt + "\n私有候选", Summary: domain.EvoReplaySummary{Passed: true}}
	if err := env.store.PutEvoProposal(current); err != nil {
		t.Fatal(err)
	}
	f := domain.EvoFinding{ID: "permission-finding", ProjectID: "evo-p", AssetKind: "agent", AssetID: env.agent.ID, Title: "本项目问题", Status: domain.EvoFindingOpen}
	if err := env.store.PutEvoFinding(f); err != nil {
		t.Fatal(err)
	}
	requestJSONAuthed[map[string]any](t, env.router, viewer, http.MethodPost, "/api/evolution/proposals", map[string]any{"findingId": f.ID}, http.StatusForbidden)
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals", map[string]any{"findingId": f.ID}, http.StatusConflict)
	// Existing shared-asset queues remain cancellable after the scope is
	// narrowed; new API requests must no longer add shared-asset work.
	_, legacy, _, err := env.store.SubmitEvoProposal(domain.EvoProposal{ID: "legacy-shared", ProjectID: "evo-p", FindingID: f.ID, AssetKind: "agent", AssetID: env.agent.ID}, "legacy-queue", "")
	if err != nil || legacy == nil {
		t.Fatalf("legacy queue: %+v %v", legacy, err)
	}
	queued := struct{ QueueID string }{legacy.ID}
	requestJSONAuthed[map[string]any](t, env.router, viewer, http.MethodPost, "/api/evolution/queue/"+queued.QueueID+"/start", nil, http.StatusForbidden)
	requestJSONAuthed[map[string]any](t, env.router, viewer, http.MethodPost, "/api/evolution/queue/"+queued.QueueID+"/reject", nil, http.StatusForbidden)
	overview := requestJSONAuthed[domain.EvoOverview](t, env.router, env.user, http.MethodGet, "/api/evolution/overview?projectId=evo-p", nil, http.StatusOK)
	if len(overview.Queue) != 1 || len(overview.Pending) != 0 || len(overview.Trial) != 0 {
		t.Fatalf("other project's current must not leak through overview: %+v", overview)
	}
	cancelled := requestJSONAuthed[domain.EvoQueueItem](t, env.router, env.user, http.MethodPost, "/api/evolution/queue/"+queued.QueueID+"/reject", nil, http.StatusOK)
	if cancelled.Status != "rejected" {
		t.Fatalf("queue cancellation = %+v", cancelled)
	}
}

func TestEvolutionDiagnosisPreservesCurrentEvidenceAndQueuesNewIntent(t *testing.T) {
	env := newEvoTestEnv(t)
	env.seedRun(t, "intent-old", "failed", "failed", "编译失败")
	type diagnosis struct {
		Findings []domain.EvoFinding `json:"findings"`
	}
	first := requestJSON[diagnosis](t, env.router, http.MethodPost, "/api/evolution/diagnose", map[string]any{"projectId": "evo-p"}, http.StatusOK)
	if len(first.Findings) != 1 {
		t.Fatalf("initial findings: %+v", first.Findings)
	}
	p := env.seedPendingProposal(t, "intent-current")
	p.FindingID = first.Findings[0].ID
	if err := env.store.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	env.seedRun(t, "intent-new", "failed", "failed", "缺少端到端测试")
	second := requestJSON[diagnosis](t, env.router, http.MethodPost, "/api/evolution/diagnose", map[string]any{"projectId": "evo-p"}, http.StatusOK)
	if len(second.Findings) != 1 || second.Findings[0].ID == first.Findings[0].ID || second.Findings[0].ProposalID != "" {
		t.Fatalf("new problem fingerprint must not merge into validated coverage: %+v", second.Findings)
	}
	frozen, _ := env.store.EvoFinding(first.Findings[0].ID)
	if len(frozen.SampleRunIDs) != 1 || frozen.SampleRunIDs[0] != "intent-old" {
		t.Fatalf("current evidence was overwritten: %+v", frozen)
	}
	queued := requestJSONAuthed[struct {
		QueueID string `json:"queueId"`
	}](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals", map[string]any{"findingId": second.Findings[0].ID}, http.StatusAccepted)
	third := requestJSON[diagnosis](t, env.router, http.MethodPost, "/api/evolution/diagnose", map[string]any{"projectId": "evo-p"}, http.StatusOK)
	if len(third.Findings) != 1 || third.Findings[0].ID != second.Findings[0].ID || queued.QueueID == "" {
		t.Fatalf("queued intent must persist across diagnosis: %+v", third.Findings)
	}
	requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+p.ID+"/approve", nil, http.StatusOK)
	stale := first.Findings[0]
	stale.Status, stale.ProposalID = domain.EvoFindingProposed, p.ID
	if err := env.store.PutEvoFinding(stale); err != nil {
		t.Fatal(err)
	}
	resolved, _ := env.store.EvoFinding(stale.ID)
	if resolved.Status != domain.EvoFindingResolved {
		t.Fatal("late diagnosis must not resurrect released finding")
	}
	if later, _ := env.store.EvoFinding(second.Findings[0].ID); later.Status != domain.EvoFindingOpen {
		t.Fatal("release must not resolve queued, unvalidated problem")
	}
}

func TestEvolutionLateGenerationCannotReviveRejectedProposal(t *testing.T) {
	env := newEvoTestEnv(t)
	p := env.seedPendingProposal(t, "late-current")
	requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+p.ID+"/reject", nil, http.StatusOK)
	env.h.failProposal(p, "late failure")
	got, _ := env.store.EvoProposal(p.ID)
	if got.Status != domain.EvoProposalRejected || got.Error != "" {
		t.Fatalf("late failure revived or overwrote terminal state: %+v", got)
	}
}

func TestEvolutionPolicyFeedbackAndSamples(t *testing.T) {
	env := newEvoTestEnv(t)
	ok := env.seedRun(t, "evo-src-good", "succeeded", "succeeded", "")

	requestJSON[domain.EvoPolicy](t, env.router, http.MethodPut, "/api/evolution/policies/agent/"+env.agent.ID, map[string]any{"level": domain.EvoLevelOff}, http.StatusOK)
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals", map[string]any{"projectId": "evo-p", "assetKind": "agent", "assetId": env.agent.ID}, http.StatusConflict)

	requestJSON[map[string]any](t, env.router, http.MethodPost, "/api/runs/"+ok.ID+"/feedback", map[string]any{"detail": " "}, http.StatusBadRequest)
	sig := requestJSON[domain.EvoSignal](t, env.router, http.MethodPost, "/api/runs/"+ok.ID+"/feedback", map[string]any{"detail": "命名不规范"}, http.StatusCreated)
	if sig.ProjectID != "evo-p" || sig.Kind != "user_feedback" {
		t.Fatalf("signal = %+v", sig)
	}
	requestJSON[domain.EvoSample](t, env.router, http.MethodPost, "/api/runs/"+ok.ID+"/pin-sample", map[string]any{"note": "典型"}, http.StatusCreated)
	samples := requestJSON[[]domain.EvoSample](t, env.router, http.MethodGet, "/api/evolution/samples?projectId=evo-p", nil, http.StatusOK)
	if len(samples) != 1 || samples[0].RunID != ok.ID {
		t.Fatalf("samples = %+v", samples)
	}
	requestJSON[map[string]any](t, env.router, http.MethodDelete, "/api/runs/"+ok.ID+"/pin-sample", nil, http.StatusOK)
	overview := requestJSON[domain.EvoOverview](t, env.router, http.MethodGet, "/api/evolution/overview?projectId=evo-p", nil, http.StatusOK)
	if len(overview.Policies) != 1 || overview.Policies[0].Level != domain.EvoLevelOff {
		t.Fatalf("overview policies = %+v", overview.Policies)
	}
	requestJSON[map[string]any](t, env.router, http.MethodGet, "/api/evolution/overview", nil, http.StatusBadRequest)
}

func (env *evoTestEnv) seedReplay(t *testing.T, proposalID, sourceID string, replayRun domain.Run) {
	t.Helper()
	replayRun.ExecutionContext = domain.TelemetryContext{ProjectID: "evo-p", Purpose: domain.RunPurposeEvolutionReplay, EvolutionReplayID: "evr-" + replayRun.ID}
	if replayRun.CreatedAt.IsZero() {
		replayRun.CreatedAt, replayRun.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	}
	if err := env.store.PutRun(replayRun); err != nil {
		t.Fatal(err)
	}
	if err := env.store.PutEvoReplay(domain.EvoReplay{
		ID: "evr-" + replayRun.ID, ProposalID: proposalID, ProjectID: "evo-p", Group: domain.EvoGroupDiagnosis,
		SourceRunID: sourceID, SourceStatus: "succeeded", SourceScore: 2, RunID: replayRun.ID, Status: replayRun.Status, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

func (env *evoTestEnv) seedValidatingProposal(t *testing.T, id string) {
	t.Helper()
	if err := env.store.PutEvoProposal(domain.EvoProposal{
		ID: id, ProjectID: "evo-p", AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, AssetName: env.agent.Name,
		Status: domain.EvoProposalValidating, CandidateContent: env.agent.Prompt + "\n补充 e2e 检查", BaseHash: domain.ContentHash(env.agent.Prompt),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEvolutionFeedbackReplayDeliberateRejectIsNotRegression(t *testing.T) {
	env := newEvoTestEnv(t)
	env.seedRun(t, "evo-fb-src", "succeeded", "succeeded", "")
	env.seedValidatingProposal(t, "evp-fb")
	now := time.Now().UTC()
	env.seedReplay(t, "evp-fb", "evo-fb-src", domain.Run{
		ID: "evo-fb-replay", WorkflowID: env.wf.ID, Status: "failed", WorkflowSnapshot: env.wf,
		Nodes: map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "failed", FinishedAt: now,
			Error:      "agent submitted terminal failure: 缺少端到端验收用例",
			GateResult: &domain.GateResult{Decision: "fail", Reason: "缺少端到端验收用例"}}},
	})
	p := env.h.sweepProposal(domain.EvoProposal{ID: "evp-fb"}, now)
	if len(p.Replays) != 1 || p.Replays[0].Outcome != domain.EvoOutcomeChanged || !strings.Contains(p.Replays[0].Error, "阻断结论") {
		t.Fatalf("deliberate reject on a feedback sample should not regress: %+v", p.Replays)
	}
	if p.Status != domain.EvoProposalPending || !p.Summary.Passed || p.Summary.Changed != 1 || !strings.Contains(p.Summary.Verdict, "改变了结论") {
		t.Fatalf("proposal should await review: status=%s summary=%+v", p.Status, p.Summary)
	}

	requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/evp-fb/reject", nil, http.StatusOK)
	env.seedValidatingProposal(t, "evp-crash")
	env.seedReplay(t, "evp-crash", "evo-fb-src", domain.Run{
		ID: "evo-crash-replay", WorkflowID: env.wf.ID, Status: "failed", WorkflowSnapshot: env.wf,
		Nodes: map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "failed", Error: "executor reported an error", FinishedAt: now}},
	})
	p = env.h.sweepProposal(domain.EvoProposal{ID: "evp-crash"}, now)
	if p.Replays[0].Outcome != domain.EvoOutcomeRegressed || p.Status != domain.EvoProposalFailed {
		t.Fatalf("an executor error is still a regression: status=%s replays=%+v", p.Status, p.Replays)
	}
}

func TestEvolutionFeedbackReplayReworkIsAChangedVerdict(t *testing.T) {
	env := newEvoTestEnv(t)
	env.seedRun(t, "evo-rw-src", "succeeded", "succeeded", "")
	wf := env.wf
	wf.Nodes = append([]domain.WorkflowNode{{ID: "design", Name: "方案设计", Type: "agent"}}, wf.Nodes...)
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == "code" {
			wf.Nodes[i].Name, wf.Nodes[i].RejectTo, wf.Nodes[i].MaxReworks = "独立评审", "design", 1
		}
	}
	now := time.Now().UTC()

	env.seedValidatingProposal(t, "evp-rw")
	env.seedReplay(t, "evp-rw", "evo-rw-src", domain.Run{ID: "evo-rw-replay", WorkflowID: wf.ID, Status: "succeeded", WorkflowSnapshot: wf,
		Nodes: map[string]*domain.NodeRun{"design": {NodeID: "design", Status: "succeeded", ReworkCount: 1}, "code": {NodeID: "code", Status: "succeeded"}}})
	p := env.h.sweepProposal(domain.EvoProposal{ID: "evp-rw"}, now)
	if p.Replays[0].Outcome != domain.EvoOutcomeChanged || !strings.Contains(p.Replays[0].Error, "打回「方案设计」返工 1 次后通过") || !p.Summary.Passed {
		t.Fatalf("a rework sent by the improved node is the change under test: %+v summary=%+v", p.Replays, p.Summary)
	}

	requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/evp-rw/reject", nil, http.StatusOK)
	env.seedValidatingProposal(t, "evp-budget")
	env.seedReplay(t, "evp-budget", "evo-rw-src", domain.Run{ID: "evo-budget-replay", WorkflowID: wf.ID, Status: "waiting", WorkflowSnapshot: wf,
		Nodes: map[string]*domain.NodeRun{"design": {NodeID: "design", Status: "succeeded", ReworkCount: 1},
			"code": {NodeID: "code", Status: "waiting_approval", GateResult: &domain.GateResult{Decision: "rework", Reason: "仍缺少 e2e 用例"}}}})
	p = env.h.sweepProposal(domain.EvoProposal{ID: "evp-budget"}, now)
	if p.Replays[0].Outcome != domain.EvoOutcomeChanged || !strings.Contains(p.Replays[0].Error, "返工后仍给出阻断结论") || p.Status != domain.EvoProposalPending {
		t.Fatalf("a spent rework budget with a standing reject should settle as changed: status=%s %+v", p.Status, p.Replays)
	}
}

func TestEvolutionReplayAutoApprovesGatesTheSourcePassed(t *testing.T) {
	env := newEvoTestEnv(t)
	wf := env.wf
	wf.Nodes = append([]domain.WorkflowNode{{ID: "accept", Name: "验收", Type: "approval"}}, wf.Nodes...)
	now := time.Now().UTC()
	source := domain.Run{ID: "evo-gate-src", WorkflowID: wf.ID, Status: "succeeded", WorkflowSnapshot: wf, ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
		Nodes:     map[string]*domain.NodeRun{"accept": {NodeID: "accept", Status: "succeeded"}, "code": {NodeID: "code", Status: "succeeded"}},
		CreatedAt: now, UpdatedAt: now}
	if err := env.store.PutRun(source); err != nil {
		t.Fatal(err)
	}

	env.seedValidatingProposal(t, "evp-own-gate")
	env.seedReplay(t, "evp-own-gate", source.ID, domain.Run{ID: "evo-own-gate", WorkflowID: wf.ID, Status: "waiting", WorkflowSnapshot: wf,
		Nodes: map[string]*domain.NodeRun{"accept": {NodeID: "accept", Status: "pending"}, "code": {NodeID: "code", Status: "waiting_approval"}}})
	p := env.h.sweepProposal(domain.EvoProposal{ID: "evp-own-gate"}, now)
	if run, _ := env.store.RunState("evo-own-gate"); run.Nodes["code"].Status != "waiting_approval" || p.Replays[0].WaitingSince.IsZero() {
		t.Fatalf("a gate raised by the improved node must stay open: node=%s replay=%+v", run.Nodes["code"].Status, p.Replays[0])
	}

	requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/evp-own-gate/reject", nil, http.StatusOK)
	env.seedValidatingProposal(t, "evp-approval")
	env.seedReplay(t, "evp-approval", source.ID, domain.Run{ID: "evo-approval", WorkflowID: wf.ID, Status: "waiting", WorkflowSnapshot: wf,
		Nodes: map[string]*domain.NodeRun{"accept": {NodeID: "accept", Status: "waiting_approval"}, "code": {NodeID: "code", Status: "succeeded"}}})
	p = env.h.sweepProposal(domain.EvoProposal{ID: "evp-approval"}, now)
	run, _ := env.store.RunState("evo-approval")
	if run.Nodes["accept"].Status != "succeeded" || p.Replays[0].Status == "waiting" || !p.Replays[0].WaitingSince.IsZero() {
		t.Fatalf("an approval the source passed should be auto-approved: node=%s replay=%+v", run.Nodes["accept"].Status, p.Replays[0])
	}
}
