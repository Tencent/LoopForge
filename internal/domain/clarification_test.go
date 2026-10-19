package domain

import "testing"

func TestAssessRequirementTextNeverReturnsEmptyDimensions(t *testing.T) {
	got := AssessRequirementText(ClarificationPolicy{}, "短标题", "")
	if got.Score != 0 {
		t.Fatalf("empty body score=%d", got.Score)
	}
	if got.Passed {
		t.Fatal("empty body must not pass the default 80 threshold")
	}
	if got.Status != ClarificationNeedsUser {
		t.Fatalf("status=%q", got.Status)
	}
	policy := DefaultClarificationPolicy()
	if len(got.Dimensions) != len(policy.Dimensions) {
		t.Fatalf("dimensions=%v", got.Dimensions)
	}
	for _, dim := range policy.Dimensions {
		if _, ok := got.Dimensions[dim.ID]; !ok {
			t.Fatalf("missing dimension %s in %+v", dim.ID, got.Dimensions)
		}
	}
}

func TestAssessRequirementTextScoresCoveredDimensions(t *testing.T) {
	body := `<p>目标是解决国际站绑定套餐域名和国内站不一致的问题，预期产生对齐全站业务价值。</p>
<p>用户是国际站套餐管理员，在续费场景下使用。</p>
<p>本次范围包含取回绑定逻辑，不包含计费边界。</p>
<p>验收标准：国内站与国际站绑定结果一致，相关接口测试通过即可判断完成。</p>
<p>约束是必须兼容现有套餐依赖，本迭代截止前交付，注意回滚风险。</p>`
	got := AssessRequirementText(ClarificationPolicy{}, "国际站绑定对齐", body)
	if !got.Passed {
		t.Fatalf("complete TAPD description should pass: score=%d dims=%v missing=%v", got.Score, got.Dimensions, got.Missing)
	}
	if got.Score < 80 {
		t.Fatalf("score=%d", got.Score)
	}
	for id, v := range got.Dimensions {
		if v <= 0 {
			t.Fatalf("dimension %s scored %d, excerpts=%q", id, v, got.Answers[id])
		}
	}
}

func TestScoreClarificationDimensionsKeepsUnratedAsZero(t *testing.T) {
	dims := DefaultClarificationPolicy().Dimensions
	score, rated := ScoreClarificationDimensions(dims, map[string]int{"goal": 100})
	if rated["goal"] != 100 || rated["acceptance"] != 0 {
		t.Fatalf("rated=%v", rated)
	}
	if len(rated) != len(dims) {
		t.Fatalf("rated size=%d", len(rated))
	}
	if score <= 0 || score >= 80 {
		t.Fatalf("partial score=%d", score)
	}
	emptyScore, emptyRated := ScoreClarificationDimensions(dims, nil)
	if emptyScore != 0 || len(emptyRated) != len(dims) {
		t.Fatalf("empty ratings score=%d rated=%v", emptyScore, emptyRated)
	}
}

func TestShouldRefreshImportScore(t *testing.T) {
	if !ShouldRefreshImportScore(ClarificationAssessment{Score: 12, Status: ClarificationNeedsUser}) {
		t.Fatal("import score should refresh")
	}
	if ShouldRefreshImportScore(ClarificationAssessment{Status: ClarificationReady, Summary: "done"}) {
		t.Fatal("ready conversation must be kept")
	}
	if ShouldRefreshImportScore(ClarificationAssessment{Status: ClarificationAnalyzing}) {
		t.Fatal("analyzing round must be kept")
	}
	if ShouldRefreshImportScore(ClarificationAssessment{History: []ClarificationMessage{{Role: "user", Content: "x"}}}) {
		t.Fatal("history must be kept")
	}
}

func TestShouldAutoStartClarification(t *testing.T) {
	need := Issue{Kind: "requirement", Clarification: ClarificationAssessment{Status: ClarificationNeedsUser, Score: 12, Gated: true}}
	if !ShouldAutoStartClarification(need) {
		t.Fatal("import needs_user should auto-start")
	}
	if ShouldAutoStartClarification(Issue{Kind: "requirement", Clarification: ClarificationAssessment{Status: ClarificationNeedsUser, Score: 12}}) {
		t.Fatal("external sync tickets should not auto-start")
	}
	waiting := Issue{Kind: "requirement", Clarification: ClarificationAssessment{Status: ClarificationInProgress, Questions: []ClarificationQuestion{{ID: "q1", Question: "范围？"}}}}
	if ShouldAutoStartClarification(waiting) {
		t.Fatal("questions already asked wait for the user")
	}
	bug := Issue{Kind: "issue", Clarification: ClarificationAssessment{Status: ClarificationNeedsUser}}
	if ShouldAutoStartClarification(bug) {
		t.Fatal("non-requirements are not auto-clarified")
	}
}

func TestRequirementSOPBlocked(t *testing.T) {
	req := Issue{Kind: "requirement", Clarification: ClarificationAssessment{Score: 40}}
	if RequirementSOPBlocked(req, ClarificationPolicy{}) {
		t.Fatal("external sync tickets are not gated")
	}
	req.Clarification.Gated = true
	if !RequirementSOPBlocked(req, ClarificationPolicy{}) {
		t.Fatal("unmet manual requirement must block SOP")
	}
	req.Clarification.Passed = true
	req.Clarification.Score = 90
	if RequirementSOPBlocked(req, ClarificationPolicy{}) {
		t.Fatal("passed requirement must not block SOP")
	}
	ready := Issue{Kind: "requirement", Clarification: ClarificationAssessment{Gated: true, Score: 76, Status: ClarificationReady}}
	if RequirementSOPBlocked(ready, ClarificationPolicy{}) {
		t.Fatal("finished clarification must not keep the score gate")
	}
	bug := Issue{Kind: "issue", Clarification: ClarificationAssessment{Score: 0, Gated: true}}
	if RequirementSOPBlocked(bug, ClarificationPolicy{}) {
		t.Fatal("bugs are not gated by requirement clarification")
	}
}
