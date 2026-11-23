package evolution

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func testWorkflow() domain.WorkflowDefinition {
	return domain.WorkflowDefinition{ID: "wf", Nodes: []domain.WorkflowNode{
		{ID: "plan", AgentID: "planner"},
		{ID: "code", AgentID: "coder"},
		{ID: "gate", Type: "approval"},
		{ID: "build", AgentID: "preview-build"},
	}}
}

func testRun(id, status string, nodes map[string]*domain.NodeRun) domain.Run {
	return domain.Run{ID: id, WorkflowID: "wf", Status: status, WorkflowSnapshot: testWorkflow(), Nodes: nodes, UpdatedAt: time.Now()}
}

func testInput(runs []domain.Run) DiagnoseInput {
	agents := map[string]domain.AgentDefinition{
		"planner": {ID: "planner", Name: "规划师", Rules: []string{"r1"}},
		"coder":   {ID: "coder", Name: "开发"},
	}
	return DiagnoseInput{
		ProjectID: "p1",
		Runs:      runs,
		Agent: func(_ domain.WorkflowDefinition, id string) (domain.AgentDefinition, bool) {
			a, ok := agents[id]
			return a, ok
		},
		Rule: func(id string) (domain.RuleDefinition, bool) {
			if id == "r1" {
				return domain.RuleDefinition{ID: "r1", Name: "提交规范"}, true
			}
			return domain.RuleDefinition{}, false
		},
		Level: func(string, string) int { return domain.EvoLevelPropose },
	}
}

func TestDiagnoseAttributesFailuresToAgents(t *testing.T) {
	runs := []domain.Run{
		testRun("r-a", "failed", map[string]*domain.NodeRun{"plan": {Status: "succeeded"}, "code": {Status: "failed", Error: "编译失败"}}),
		testRun("r-b", "failed", map[string]*domain.NodeRun{"plan": {Status: "succeeded"}, "code": {Status: "failed", Error: "测试失败"}}),
		testRun("r-c", "succeeded", map[string]*domain.NodeRun{"plan": {Status: "succeeded", ReworkCount: 1, Feedback: "计划太粗"}, "code": {Status: "succeeded"}}),
	}
	found := Diagnose(testInput(runs))
	if len(found) != 1 {
		t.Fatalf("want 1 finding (coder failed twice; planner single rework dropped), got %d: %+v", len(found), found)
	}
	f := found[0]
	if f.AssetKind != domain.EvoAssetAgent || f.AssetID != "coder" || f.Status != domain.EvoFindingOpen || f.ProjectID != "p1" {
		t.Fatalf("unexpected finding %+v", f)
	}
	if f.Severity != "medium" || len(f.SampleRunIDs) != 2 || !strings.Contains(f.Title, "2 次节点失败") {
		t.Fatalf("unexpected severity/samples/title %+v", f)
	}
	if !strings.Contains(f.Summary, "编译失败") {
		t.Fatalf("summary should quote details: %q", f.Summary)
	}
}

func TestDiagnoseEvalAndFeedbackSignals(t *testing.T) {
	run := testRun("r-a", "succeeded", map[string]*domain.NodeRun{
		"plan": {Status: "succeeded", FinishedAt: time.Now().Add(-time.Hour)},
		"code": {Status: "succeeded", FinishedAt: time.Now()},
	})
	in := testInput([]domain.Run{run})
	in.Evals = map[string]domain.RunEvalRecord{"r-a": {Status: domain.RunEvalSucceeded, Passed: false, Result: domain.RunEvalResult{Findings: []domain.RunEvalFinding{
		{Severity: "high", RuleID: "r1", Message: "提交信息不符合规范"},
		{Severity: "medium", RuleID: "r1", Message: "缺少 issue 号"},
		{Severity: "info", NodeID: "plan", Message: "忽略"},
	}}}}
	in.Feedback = []domain.EvoSignal{
		{RunID: "r-a", Detail: "代码风格差"},
		{RunID: "r-a", NodeID: "code", Detail: "没有写测试"},
		{RunID: "other", Detail: "不在窗口内"},
	}
	found := Diagnose(in)
	byID := map[string]domain.EvoFinding{}
	for _, f := range found {
		byID[f.AssetKind+":"+f.AssetID] = f
	}
	rule, ok := byID[domain.EvoAssetRule+":r1"]
	if !ok || len(rule.Signals) != 2 || rule.Signals[0].Kind != SignalEvalLow {
		t.Fatalf("rule finding missing or wrong: %+v", found)
	}
	coder, ok := byID[domain.EvoAssetAgent+":coder"]
	if !ok || len(coder.Signals) != 2 {
		t.Fatalf("feedback without node should fall back to last finished agent: %+v", found)
	}
	if _, ok := byID[domain.EvoAssetAgent+":planner"]; ok {
		t.Fatalf("info finding must be ignored: %+v", found)
	}
}

func TestDiagnoseKeepsOneExplicitFeedbackOnSuccessfulRun(t *testing.T) {
	run := testRun("feedback-once", "succeeded", map[string]*domain.NodeRun{"code": {Status: "succeeded"}})
	in := testInput([]domain.Run{run})
	in.Feedback = []domain.EvoSignal{{ID: "human", RunID: run.ID, NodeID: "code", Kind: SignalUserFeedback, Detail: "预期定位第 4 行，实际选择第 1 行"}}
	found := Diagnose(in)
	if len(found) != 1 || found[0].AssetID != "coder" || len(found[0].Signals) != 1 || found[0].Signals[0].ID != "human" {
		t.Fatalf("one explicit feedback must form a visible finding: %+v", found)
	}
	run.Nodes["code"].ReworkCount = 1
	in = testInput([]domain.Run{run})
	if found := Diagnose(in); len(found) != 0 {
		t.Fatalf("one automatic rework signal still needs more evidence: %+v", found)
	}
}

func TestDiagnoseSkipsReplaysAndDisabledAssets(t *testing.T) {
	replay := testRun("r-replay", "failed", map[string]*domain.NodeRun{"code": {Status: "failed"}})
	replay.ExecutionContext.Purpose = domain.RunPurposeEvolutionReplay
	normal := testRun("r-a", "failed", map[string]*domain.NodeRun{"code": {Status: "failed"}, "build": {Status: "failed"}})
	in := testInput([]domain.Run{replay, normal})
	if got := Diagnose(in); len(got) != 1 || len(got[0].SampleRunIDs) != 1 || got[0].SampleRunIDs[0] != "r-a" {
		t.Fatalf("replay runs and preview-build must be ignored: %+v", got)
	}
	in.Level = func(string, string) int { return domain.EvoLevelOff }
	if got := Diagnose(in); len(got) != 0 {
		t.Fatalf("L0 assets must not produce findings: %+v", got)
	}
}

func TestRunScoreAndOutcome(t *testing.T) {
	cases := []struct {
		run  domain.Run
		want int
	}{
		{domain.Run{Status: "succeeded", Nodes: map[string]*domain.NodeRun{"a": {}}}, 2},
		{domain.Run{Status: "succeeded", Nodes: map[string]*domain.NodeRun{"a": {ReworkCount: 2}}}, 1},
		{domain.Run{Status: "failed"}, 0},
		{domain.Run{Status: "cancelled"}, 0},
		{domain.Run{Status: "running"}, -1},
	}
	for _, c := range cases {
		if got := RunScore(c.run); got != c.want {
			t.Fatalf("RunScore(%s)=%d want %d", c.run.Status, got, c.want)
		}
	}
	if Outcome(0, 2) != domain.EvoOutcomeFixed || Outcome(2, 1) != domain.EvoOutcomeRegressed || Outcome(1, 1) != domain.EvoOutcomeSame {
		t.Fatal("outcome mapping broken")
	}
	if !ReplayWaiting(domain.Run{Status: "running", Nodes: map[string]*domain.NodeRun{"g": {Status: "waiting_approval"}}}) {
		t.Fatal("approval wait must be detected")
	}
	if ReplayWaiting(domain.Run{Status: "running", Nodes: map[string]*domain.NodeRun{"g": {Status: "running"}}}) {
		t.Fatal("running node is not waiting")
	}
}

func replays(outcomes ...string) []domain.EvoReplay {
	out := make([]domain.EvoReplay, len(outcomes))
	for i, o := range outcomes {
		out[i] = domain.EvoReplay{Group: domain.EvoGroupDiagnosis, Outcome: o}
	}
	return out
}

func TestSummarizeFeedbackOnlyFinding(t *testing.T) {
	clean := func(group, outcome string) domain.EvoReplay {
		return domain.EvoReplay{Group: group, SourceScore: 2, Outcome: outcome}
	}
	s := Summarize([]domain.EvoReplay{clean(domain.EvoGroupDiagnosis, domain.EvoOutcomeSame), clean(domain.EvoGroupRegression, domain.EvoOutcomeSame)})
	if !s.Passed || !strings.Contains(s.Verdict, "反馈类问题") || !strings.Contains(s.Verdict, "结论也没有变化") {
		t.Fatalf("feedback-only finding without regressions should pass with a review hint: %+v", s)
	}
	s = Summarize([]domain.EvoReplay{clean(domain.EvoGroupDiagnosis, domain.EvoOutcomeChanged), clean(domain.EvoGroupRegression, domain.EvoOutcomeSame)})
	if !s.Passed || s.Changed != 1 || s.Unchanged != 1 || !strings.Contains(s.Verdict, "1 个回放按新正文改变了结论") {
		t.Fatalf("a changed verdict on a feedback sample should pass and be reported: %+v", s)
	}
	s = Summarize([]domain.EvoReplay{clean(domain.EvoGroupDiagnosis, domain.EvoOutcomeRegressed), clean(domain.EvoGroupRegression, domain.EvoOutcomeSame)})
	if s.Passed || !strings.Contains(s.Verdict, "1 个退化") {
		t.Fatalf("feedback-only regression must fail: %+v", s)
	}
	s = Summarize([]domain.EvoReplay{{Group: domain.EvoGroupDiagnosis, SourceScore: 0, Outcome: domain.EvoOutcomeSame}, clean(domain.EvoGroupDiagnosis, domain.EvoOutcomeSame)})
	if s.Passed {
		t.Fatalf("a real failing sample still has to be fixed: %+v", s)
	}
}

func TestFeedbackSample(t *testing.T) {
	ok := domain.Run{ID: "r1", Status: "succeeded"}
	finding := domain.EvoFinding{Signals: []domain.EvoSignal{{RunID: "r1", Kind: SignalUserFeedback}, {RunID: "r2", Kind: SignalNodeFailed}}}
	if !FeedbackSample(ok, finding) {
		t.Fatal("clean run with user feedback is a sample")
	}
	if FeedbackSample(domain.Run{ID: "r3", Status: "succeeded"}, finding) {
		t.Fatal("run without feedback is not a feedback sample")
	}
	if FeedbackSample(domain.Run{ID: "r1", Status: "running"}, finding) {
		t.Fatal("non-terminal run is not a sample")
	}
}

func TestSummarizeVerdict(t *testing.T) {
	cases := []struct {
		name   string
		in     []domain.EvoReplay
		passed bool
		prefix string
	}{
		{"empty", nil, false, "没有可回放"},
		{"in progress", replays(domain.EvoOutcomeFixed, ""), false, "回放进行中 1/2"},
		{"fixed", replays(domain.EvoOutcomeFixed, domain.EvoOutcomeSame), true, "通过"},
		{"fixed with one regression", replays(domain.EvoOutcomeFixed, domain.EvoOutcomeFixed, domain.EvoOutcomeRegressed), true, "通过"},
		{"tie", replays(domain.EvoOutcomeFixed, domain.EvoOutcomeRegressed), false, "未通过：修好 1"},
		{"two regressions", replays(domain.EvoOutcomeFixed, domain.EvoOutcomeFixed, domain.EvoOutcomeFixed, domain.EvoOutcomeRegressed, domain.EvoOutcomeRegressed), false, "未通过"},
		{"nothing fixed", replays(domain.EvoOutcomeSame), false, "未通过：没有修好"},
		{"all blocked", replays(domain.EvoOutcomeBlocked), false, "全部回放被"},
	}
	for _, c := range cases {
		s := Summarize(c.in)
		if s.Passed != c.passed || !strings.HasPrefix(s.Verdict, c.prefix) {
			t.Fatalf("%s: got passed=%v verdict=%q", c.name, s.Passed, s.Verdict)
		}
	}
	s := Summarize(replays(domain.EvoOutcomeFixed, domain.EvoOutcomeBlocked))
	if !s.Passed || s.Blocked != 1 || !strings.Contains(s.Verdict, "1 个样本被阻塞") {
		t.Fatalf("blocked samples should be reported but not fail: %+v", s)
	}
}

func TestPlanSamplesGroupsAndDedup(t *testing.T) {
	mk := func(ids ...string) []domain.Run {
		out := []domain.Run{}
		for _, id := range ids {
			out = append(out, domain.Run{ID: id, WorkflowID: "wf"})
		}
		return out
	}
	replay := domain.Run{ID: "x", WorkflowID: "wf"}
	replay.ExecutionContext.Purpose = domain.RunPurposeEvolutionReplay
	failing := append(mk("f1", "f2", "f3", "f4", "f5"), replay, domain.Run{ID: "noflow"})
	plans := PlanSamples(failing, mk("s1", "f1", "s2", "s3"), mk("s1", "p1"))
	got := map[string][]string{}
	for _, p := range plans {
		got[p.Group] = append(got[p.Group], p.Run.ID)
	}
	want := map[string]string{
		domain.EvoGroupDiagnosis:  "f1,f2",
		domain.EvoGroupHoldout:    "f3,f4",
		domain.EvoGroupPinned:     "s1,p1",
		domain.EvoGroupRegression: "s2,s3",
	}
	for group, ids := range want {
		if strings.Join(got[group], ",") != ids {
			t.Fatalf("group %s = %v, want %s (all: %v)", group, got[group], ids, got)
		}
	}
}

func TestReplayRepositoriesIsolatesBranches(t *testing.T) {
	source := domain.Run{Repositories: []domain.RepositoryContext{
		{RepositoryID: "app", WorkBranch: "feature/x", BaseBranch: "", DefaultBranch: "main", WorkspaceDir: "/tmp/ws", IntegrateBranch: "release", RequireExistingWorkBranch: true, CreateNewWorkBranch: true},
		{RepositoryID: "docs", ReadOnly: true, WorkBranch: "main", BaseBranch: "main"},
		{},
	}}
	out := ReplayRepositories(source, "evr-1")
	if len(out) != 2 {
		t.Fatalf("empty repository context must be dropped: %+v", out)
	}
	app := out[0]
	if app.WorkBranch != "evo/evr-1" || app.BaseBranch != "main" || app.WorkspaceDir != "" || app.IntegrateBranch != "" || app.RequireExistingWorkBranch || app.CreateNewWorkBranch {
		t.Fatalf("writable repo not isolated: %+v", app)
	}
	if out[1].WorkBranch != "" || out[1].BaseBranch != "main" {
		t.Fatalf("read-only repo should keep base and drop work branch: %+v", out[1])
	}
	if source.Repositories[0].WorkBranch != "feature/x" {
		t.Fatal("source run must not be mutated")
	}
}

func TestParseCandidate(t *testing.T) {
	c, err := ParseCandidate("好的，结果如下：\n```json\n{\"title\":\" 补充测试要求 \",\"rationale\":\"r\",\"content\":\" 新正文 \"}\n```")
	if err != nil || c.Title != "补充测试要求" || c.Content != "新正文" {
		t.Fatalf("fenced JSON: %+v %v", c, err)
	}
	if c, err := ParseCandidate(`{"content":"x"}`); err != nil || c.Title != "改进正文" {
		t.Fatalf("default title: %+v %v", c, err)
	}
	raw := "{\"title\":\"补充 e2e\",\"rationale\":\"第一行\n第二行\",\"content\":\"工作领域：评审。\n\t- 必须检查 \\\"端到端\\\" 用例\r\n- 缺失即 REJECT\"}"
	if c, err := ParseCandidate(raw); err != nil || c.Content != "工作领域：评审。\n\t- 必须检查 \"端到端\" 用例\r\n- 缺失即 REJECT" || c.Rationale != "第一行\n第二行" {
		t.Fatalf("raw newlines inside strings should be tolerated: %+v %v", c, err)
	}
	raw = `{"title":"修正定位策略","rationale":"原策略取第一条以 "ERROR/FAIL" 开头的行, 会误判","content":"取最后一条 "FAIL:" 行，再看 "ERROR"。","review":{"problem":"定位错行","changes":[{"title":"改为取末条","before":"","after":"取最后一条 "FAIL:" 行"}]}}`
	if c, err := ParseCandidate(raw); err != nil || c.Content != `取最后一条 "FAIL:" 行，再看 "ERROR"。` ||
		c.Rationale != `原策略取第一条以 "ERROR/FAIL" 开头的行, 会误判` || c.Review == nil || c.Review.Changes[0].After != `取最后一条 "FAIL:" 行` {
		t.Fatalf("unescaped quotes inside strings should be tolerated: %+v %v", c, err)
	}
	for _, raw := range []string{"", "no json", `{"title":"t","content":"  "}`, `{"title":`} {
		if _, err := ParseCandidate(raw); err == nil {
			t.Fatalf("expected error for %q", raw)
		}
	}
}

func TestStaticCheck(t *testing.T) {
	base := "请根据 {{input}} 完成任务。" + strings.Repeat("保持原有要求。", 40)
	if err := StaticCheck(base, base+"\n补充：必须写单元测试。"); err != nil {
		t.Fatalf("valid candidate rejected: %v", err)
	}
	bad := map[string]string{
		"empty":       "  ",
		"unchanged":   " " + base + " ",
		"oversized":   base + strings.Repeat("x", maxCandidateBytes),
		"stub":        "请根据 {{input}} 完成任务。",
		"placeholder": strings.ReplaceAll(base, "{{input}}", "输入") + "补充",
	}
	for name, candidate := range bad {
		if err := StaticCheck(base, candidate); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestBuildEvidenceAndPrompt(t *testing.T) {
	run := testRun("r-a", "failed", map[string]*domain.NodeRun{"code": {Status: "failed", Error: "节点错误", Output: "输出"}})
	run.Input = "做一个登录页"
	finding := domain.EvoFinding{AssetID: "coder", Title: "开发角色失败", Signals: []domain.EvoSignal{{RunID: "r-a", NodeID: "code", Kind: SignalNodeFailed, Detail: "编译失败"}}}
	ev := BuildEvidence(run, finding)
	if ev.NodeID != "code" || ev.Error != "编译失败" || ev.Output != "输出" || ev.Input != "做一个登录页" {
		t.Fatalf("evidence: %+v", ev)
	}
	system, prompt := BuildImproverPrompt(domain.EvoAssetRule, "提交规范", "旧正文", finding, []Evidence{ev}, []string{"上次补充了自测清单"})
	if !strings.Contains(system, "规则正文") || !strings.Contains(system, `"content"`) {
		t.Fatalf("system prompt: %s", system)
	}
	for _, want := range []string{"旧正文", "开发角色失败", "上次补充了自测清单", "编译失败"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
