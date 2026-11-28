package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestEvolutionSingleFeedbackIsVisibleWithoutSeparateDiagnosis(t *testing.T) {
	env := newEvoTestEnv(t)
	run := env.seedRun(t, "feedback-successful-run", "succeeded", "succeeded", "")
	other := domain.EvoFinding{ID: "unrelated", ProjectID: "evo-p", AssetKind: "agent", AssetID: "other-role", Status: domain.EvoFindingOpen}
	if err := env.store.PutEvoFinding(other); err != nil {
		t.Fatal(err)
	}
	const detail = "预期定位第 4 行的测试失败，实际选择第 1 行已恢复错误。"
	post := func() evoFeedbackResponse {
		return requestJSON[evoFeedbackResponse](t, env.router, http.MethodPost, "/api/runs/"+run.ID+"/feedback", map[string]any{"nodeId": "code", "detail": detail}, http.StatusCreated)
	}
	result := post()
	if result.FindingID == "" || result.ClusterID == "" || result.Kind != "user_feedback" {
		t.Fatalf("feedback must return its visible problem: %+v", result)
	}
	finding, ok := env.store.EvoFinding(result.FindingID)
	if !ok || len(finding.Signals) != 1 || finding.Signals[0].Detail != detail {
		t.Fatalf("feedback was not preserved: %+v", finding)
	}
	view := requestJSON[domain.EvoOverview](t, env.router, http.MethodGet, "/api/evolution/overview?projectId=evo-p", nil, http.StatusOK)
	found := false
	for _, item := range view.Findings {
		found = found || item.ID == result.FindingID
	}
	if !found {
		t.Fatal("feedback is absent from the first overview read")
	}
	if again := post(); again.FindingID != result.FindingID {
		t.Fatalf("repeated feedback created another current problem: %+v", again)
	}
	if existing, _ := env.store.EvoFinding(other.ID); existing.Status != domain.EvoFindingOpen {
		t.Fatal("feedback submission archived an unrelated problem")
	}
	if len(env.store.EvoProposals("evo-p", 20)) != 0 {
		t.Fatal("feedback submission must not generate or publish a proposal")
	}
	if agent, _ := env.store.Agent(env.agent.ID); agent.Prompt != env.agent.Prompt {
		t.Fatal("feedback submission rewrote the role")
	}
}

func TestEvolutionFeedbackExplainsUnavailableTargets(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, *evoTestEnv, *domain.Run)
	}{
		{"disabled", "关闭进化", func(t *testing.T, env *evoTestEnv, _ *domain.Run) {
			if err := env.store.PutEvoPolicy(domain.EvoPolicy{AssetKind: "agent", AssetID: env.agent.ID, Level: domain.EvoLevelOff}); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing-role", "已保存角色", func(_ *testing.T, _ *evoTestEnv, run *domain.Run) {
			run.WorkflowSnapshot.Nodes[0].AgentID = "not-stored"
		}},
		{"workspace", "工作区对话", func(_ *testing.T, _ *evoTestEnv, run *domain.Run) { run.Kind = domain.RunKindWorkspace }},
		{"old-run", "检查范围", func(_ *testing.T, _ *evoTestEnv, run *domain.Run) {
			run.ID += "-old"
			run.CreatedAt = time.Now().AddDate(0, 0, -30)
		}},
		{"old-version", "旧版角色正文", func(t *testing.T, env *evoTestEnv, run *domain.Run) {
			if err := env.store.PutRunAssetPins([]domain.RunAssetPin{{RunID: run.ID, AssetKind: "agent", AssetID: env.agent.ID, Hash: "old-hash"}}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newEvoTestEnv(t)
			run := env.seedRun(t, "recorded-feedback", "succeeded", "succeeded", "")
			tc.change(t, env, &run)
			if err := env.store.PutRun(run); err != nil {
				t.Fatal(err)
			}
			result := requestJSON[evoFeedbackResponse](t, env.router, http.MethodPost, "/api/runs/"+run.ID+"/feedback", map[string]any{"detail": "实际输出不符合预期"}, http.StatusCreated)
			if result.FindingID != "" || !strings.Contains(result.Message, tc.want) {
				t.Fatalf("unavailable target must explain why: %+v", result)
			}
			if len(env.store.EvoSignals("evo-p", time.Time{})) != 1 {
				t.Fatal("feedback must still be retained")
			}
		})
	}
}

func TestEvolutionViewerFeedbackDoesNotGrantProposalPermission(t *testing.T) {
	env := newEvoTestEnv(t)
	run := env.seedRun(t, "viewer-feedback", "succeeded", "succeeded", "")
	project, _ := env.store.Project("evo-p")
	project.OwnerID = env.user.ID + 1
	project.Members = []domain.ProjectMember{{UserID: env.user.ID, Role: "viewer"}}
	if err := env.store.PutProject(project); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	result := requestJSONAuthed[evoFeedbackResponse](t, env.router, env.user, http.MethodPost, "/api/runs/"+run.ID+"/feedback", map[string]any{"detail": "产物缺少验收步骤"}, http.StatusCreated)
	if result.FindingID == "" {
		t.Fatalf("viewer feedback should be visible: %+v", result)
	}
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals", map[string]any{"projectId": "evo-p", "findingId": result.FindingID}, http.StatusForbidden)
}

func TestEvolutionNewFeedbackPreservesCurrentProposalEvidence(t *testing.T) {
	env := newEvoTestEnv(t)
	run := env.seedRun(t, "active-feedback", "failed", "failed", "原有失败")
	diag := requestJSON[struct {
		Findings []domain.EvoFinding `json:"findings"`
	}](t, env.router, http.MethodPost, "/api/evolution/diagnose", map[string]any{"projectId": "evo-p"}, http.StatusOK)
	old := diag.Findings[0]
	p := domain.EvoProposal{ID: "current-feedback-proposal", ProjectID: "evo-p", FindingID: old.ID, AssetKind: "agent", AssetID: env.agent.ID, Status: domain.EvoProposalPending, BaseContent: env.agent.Prompt, BaseHash: domain.ContentHash(env.agent.Prompt), CandidateContent: env.agent.Prompt + "\n执行自测。"}
	if err := env.store.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	old.Status, old.ProposalID = domain.EvoFindingProposed, p.ID
	if err := env.store.PutEvoFinding(old); err != nil {
		t.Fatal(err)
	}
	result := requestJSON[evoFeedbackResponse](t, env.router, http.MethodPost, "/api/runs/"+run.ID+"/feedback", map[string]any{"detail": "另一个改进：产物需要 reason 解释字段"}, http.StatusCreated)
	if result.FindingID == "" || result.FindingID == old.ID {
		t.Fatalf("new feedback needs its own problem: %+v", result)
	}
	current, _ := env.store.EvoFinding(old.ID)
	if len(current.Signals) != len(old.Signals) || current.ProposalID != p.ID {
		t.Fatal("new feedback rewrote the active proposal's evidence")
	}
	proposal, _ := env.store.EvoProposal(p.ID)
	if proposal.CandidateContent != p.CandidateContent || proposal.Status != p.Status {
		t.Fatal("feedback changed the active proposal")
	}
}
