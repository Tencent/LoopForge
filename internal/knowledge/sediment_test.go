package knowledge

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

// sedimentStore 在 fakeStore 之上补自动沉淀需要的控制面能力（知识库配置、
// 来源、墓碑、需求）。方法挂在外层，覆盖 fakeStore 的最小实现。
type sedimentStore struct {
	*fakeStore
	bases      map[string]domain.KnowledgeBase
	sources    map[string]domain.KnowledgeSource
	bindings   map[string][]domain.KnowledgeBinding
	issues     map[string]domain.Issue
	tombstones map[string]domain.KnowledgeTombstone
}

func newSedimentStore() *sedimentStore {
	return &sedimentStore{
		fakeStore:  newFakeStore(),
		bases:      map[string]domain.KnowledgeBase{},
		sources:    map[string]domain.KnowledgeSource{},
		bindings:   map[string][]domain.KnowledgeBinding{},
		issues:     map[string]domain.Issue{},
		tombstones: map[string]domain.KnowledgeTombstone{},
	}
}

func (f *sedimentStore) KnowledgeBases(projectID string) []domain.KnowledgeBase {
	out := []domain.KnowledgeBase{}
	for _, base := range f.bases {
		if projectID == "" || base.ProjectID == projectID {
			out = append(out, base)
		}
	}
	return out
}
func (f *sedimentStore) KnowledgeBase(id string) (domain.KnowledgeBase, bool) {
	base, ok := f.bases[id]
	return base, ok
}
func (f *sedimentStore) PutKnowledgeBase(base domain.KnowledgeBase) error {
	f.bases[base.ID] = base
	return nil
}
func (f *sedimentStore) KnowledgeBindings(projectID string) []domain.KnowledgeBinding {
	return f.bindings[projectID]
}
func (f *sedimentStore) PutKnowledgeBinding(binding domain.KnowledgeBinding) error {
	f.bindings[binding.ProjectID] = append(f.bindings[binding.ProjectID], binding)
	return nil
}
func (f *sedimentStore) DeleteKnowledgeBinding(projectID, baseID string) error {
	return nil
}
func (f *sedimentStore) KnowledgeSources(baseID string) []domain.KnowledgeSource {
	out := []domain.KnowledgeSource{}
	for _, src := range f.sources {
		if baseID == "" || src.BaseID == baseID {
			out = append(out, src)
		}
	}
	return out
}
func (f *sedimentStore) KnowledgeSource(id string) (domain.KnowledgeSource, bool) {
	src, ok := f.sources[id]
	return src, ok
}
func (f *sedimentStore) PutKnowledgeSource(src domain.KnowledgeSource) error {
	f.sources[src.ID] = src
	return nil
}
func (f *sedimentStore) DeleteKnowledgeSource(id string) error {
	delete(f.sources, id)
	return nil
}
func (f *sedimentStore) DeleteKnowledgeSourceContent(string) error { return nil }
func (f *sedimentStore) KnowledgeDocuments(sourceID string) []domain.KnowledgeDocument {
	return nil
}
func (f *sedimentStore) Issue(id string) (domain.Issue, bool) {
	issue, ok := f.issues[id]
	return issue, ok
}
func (f *sedimentStore) KnowledgeTombstone(projectID, fingerprint string) (domain.KnowledgeTombstone, bool) {
	st, ok := f.tombstones[projectID+"\x00"+fingerprint]
	return st, ok
}
func (f *sedimentStore) PutKnowledgeTombstone(st domain.KnowledgeTombstone) error {
	f.tombstones[st.ProjectID+"\x00"+st.Fingerprint] = st
	return nil
}

type sedimentUpstream struct {
	fakeUpstream
	written   []string
	retracted []string
	writeErr  error
}

func (u *sedimentUpstream) WriteDocument(_ context.Context, source, path, content string) (WriteResult, error) {
	u.written = append(u.written, source+"\x00"+path)
	if u.writeErr != nil {
		return WriteResult{}, u.writeErr
	}
	return WriteResult{Source: source, Path: path, Commit: "7"}, nil
}
func (u *sedimentUpstream) RetractDocument(_ context.Context, source, path string) error {
	u.retracted = append(u.retracted, source+"\x00"+path)
	return nil
}
func (u *sedimentUpstream) Tools(context.Context) ([]mcpclient.Tool, error) { return nil, nil }

const sedimentBody = "# 排障经验\n\n## 验证\n\n执行 test -f marker 后通过，引用 run-1。\n"

func newSedimentFixture(t *testing.T) (*Service, *sedimentStore, *sedimentUpstream) {
	t.Helper()
	store := newSedimentStore()
	store.bases["kb-p1"] = domain.KnowledgeBase{ID: "kb-p1", ProjectID: "p1", Name: "项目知识库", Layer: "project", SedimentPublishMode: domain.KnowledgeSedimentReview}
	store.sources[hostedSourceID("p1")] = domain.KnowledgeSource{ID: hostedSourceID("p1"), BaseID: "kb-p1", Kind: "hosted", Name: "托管文档", Status: "ready"}
	store.bindings["p1"] = []domain.KnowledgeBinding{{ProjectID: "p1", BaseID: "kb-p1", Permission: "submit"}}
	store.agents = []domain.AgentDefinition{{ID: "agent-kb", Name: "知识角色", KnowledgeBase: true}}
	store.workflows = []domain.WorkflowDefinition{{ID: "wf", Nodes: []domain.WorkflowNode{{ID: "write", Type: "agent", AgentID: "agent-kb"}}}}
	store.runs["run-1"] = domain.Run{
		ID: "run-1", WorkflowID: "wf", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "p1"},
		WorkflowSnapshot: store.workflows[0],
		Nodes:            map[string]*domain.NodeRun{"write": {NodeID: "write", Status: "succeeded"}},
	}
	store.candidates = []domain.KnowledgeCandidate{sedimentCandidate("run-1", "art-1", "write")}
	store.artifacts["art-1"] = domain.Artifact{ID: "art-1", RunID: "run-1", NodeID: "write", Name: "knowledge.md", Path: "knowledge.md", Content: sedimentBody}
	up := &sedimentUpstream{}
	svc := NewService(store, func() string { return "id1" }, up)
	svc.now = func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) }
	return svc, store, up
}

func sedimentCandidate(runID, artifactID, nodeID string) domain.KnowledgeCandidate {
	return domain.KnowledgeCandidate{
		Artifact:  domain.Artifact{ID: artifactID, RunID: runID, NodeID: nodeID, Name: "knowledge.md", Path: "knowledge.md"},
		ProjectID: "p1", RunStatus: "succeeded", WorkflowID: "wf",
	}
}

func TestSedimentQueuesSucceededRunWithGatesAndStaysIdempotent(t *testing.T) {
	svc, store, _ := newSedimentFixture(t)
	report, err := svc.SedimentOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Queued != 1 || report.AutoPublished != 0 {
		t.Fatalf("report = %+v", report)
	}
	items := store.KnowledgeSubmissions("p1", "", 10)
	if len(items) != 1 {
		t.Fatalf("submissions = %+v", items)
	}
	item, _ := store.KnowledgeSubmission(items[0].ID)
	if item.Status != domain.KnowledgeSubmissionPending || item.Source != hostedSourceID("p1") || item.ProjectID != "p1" {
		t.Fatalf("item = %+v", item)
	}
	if !strings.HasPrefix(item.Path, "knowledge/") || !strings.HasSuffix(item.Path, ".md") {
		t.Fatalf("auto path = %q", item.Path)
	}
	if item.Fingerprint == "" || len(item.SedimentSignals) != 1 || item.SedimentSignals[0] != domain.KnowledgeSignalRunSucceeded {
		t.Fatalf("signals/fingerprint = %+v", item)
	}
	if len(item.GateChecks) != 4 {
		t.Fatalf("gate checks = %+v", item.GateChecks)
	}
	for _, check := range item.GateChecks {
		if !check.OK {
			t.Fatalf("gate %s should pass: %+v", check.Key, check)
		}
	}

	if report, err = svc.SedimentOnce(context.Background()); err != nil || report.Queued != 0 {
		t.Fatalf("second scan = %+v %v", report, err)
	}
	if items = store.KnowledgeSubmissions("p1", "", 10); len(items) != 1 {
		t.Fatalf("auto queue must be idempotent: %+v", items)
	}
}

func TestSedimentOnlyConsumesKnowledgeBaseRoles(t *testing.T) {
	svc, store, _ := newSedimentFixture(t)
	store.agents = []domain.AgentDefinition{{ID: "agent-kb", Name: "知识角色", KnowledgeBase: false}}
	report, err := svc.SedimentOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Queued != 0 || len(store.KnowledgeSubmissions("p1", "", 10)) != 0 {
		t.Fatalf("role without knowledge base must not sediment: %+v", report)
	}
}

func TestSedimentSignals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*sedimentStore)
		want   string
	}{
		{"human gate approved", func(s *sedimentStore) {
			run := s.runs["run-1"]
			run.Status = "running"
			run.Events = []domain.RunEvent{{Type: "gate.approved"}}
			s.runs["run-1"] = run
			s.candidates[0].RunStatus = "running"
		}, domain.KnowledgeSignalGateApproved},
		{"requirement completed", func(s *sedimentStore) {
			run := s.runs["run-1"]
			run.Status = "failed"
			s.runs["run-1"] = run
			s.candidates[0].RunStatus = "failed"
			s.candidates[0].IssueID = "issue-1"
			s.issues["issue-1"] = domain.Issue{ID: "issue-1", Status: "done"}
		}, domain.KnowledgeSignalIssueCompleted},
		{"merge request merged", func(s *sedimentStore) {
			run := s.runs["run-1"]
			run.Status = "failed"
			s.runs["run-1"] = run
			s.candidates[0].RunStatus = "failed"
			s.candidates[0].IssueID = "issue-1"
			s.issues["issue-1"] = domain.Issue{ID: "issue-1", Status: "in_review", DeliveryMRs: []domain.DeliveryMR{{State: "merged"}}}
		}, domain.KnowledgeSignalMRMerged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, _ := newSedimentFixture(t)
			tc.mutate(store)
			report, err := svc.SedimentOnce(context.Background())
			if err != nil || report.Queued != 1 {
				t.Fatalf("report = %+v err=%v", report, err)
			}
			items := store.KnowledgeSubmissions("p1", "", 10)
			item, _ := store.KnowledgeSubmission(items[0].ID)
			if len(item.SedimentSignals) != 1 || item.SedimentSignals[0] != tc.want {
				t.Fatalf("signals = %+v want %s", item.SedimentSignals, tc.want)
			}
		})
	}
}

func TestSedimentRequiresClosureSignal(t *testing.T) {
	svc, store, _ := newSedimentFixture(t)
	run := store.runs["run-1"]
	run.Status = "running"
	store.runs["run-1"] = run
	store.candidates[0].RunStatus = "running"
	if report, err := svc.SedimentOnce(context.Background()); err != nil || report.Queued != 0 {
		t.Fatalf("running run without gate must not queue: %+v %v", report, err)
	}
}

func TestSedimentAutoPublishUsesHostedWriteback(t *testing.T) {
	svc, store, up := newSedimentFixture(t)
	base := store.bases["kb-p1"]
	base.SedimentPublishMode = domain.KnowledgeSedimentAuto
	store.bases["kb-p1"] = base
	report, err := svc.SedimentOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Queued != 1 || report.AutoPublished != 1 {
		t.Fatalf("report = %+v", report)
	}
	items := store.KnowledgeSubmissions("p1", "", 10)
	item, _ := store.KnowledgeSubmission(items[0].ID)
	if item.Status != domain.KnowledgeSubmissionPublished || item.Commit != "7" || item.ReviewedBy == "" {
		t.Fatalf("auto published item = %+v", item)
	}
	if len(up.written) != 1 || up.written[0] != "kb-hosted-c4b3147b9250b03a56cf4b06a349cce8\x00"+item.Path {
		t.Fatalf("writeback = %+v path=%s", up.written, item.Path)
	}
}

func TestSedimentGateFailureDoesNotAutoPublish(t *testing.T) {
	svc, store, up := newSedimentFixture(t)
	base := store.bases["kb-p1"]
	base.SedimentPublishMode = domain.KnowledgeSedimentAuto
	store.bases["kb-p1"] = base
	store.artifacts["art-1"] = domain.Artifact{ID: "art-1", RunID: "run-1", NodeID: "write", Name: "knowledge.md", Content: "# 凭据\n\npassword: hunter2\n"}
	report, err := svc.SedimentOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Queued != 1 || report.AutoPublished != 0 || len(up.written) != 0 {
		t.Fatalf("report=%+v writes=%+v", report, up.written)
	}
	item, _ := store.KnowledgeSubmission(store.KnowledgeSubmissions("p1", "", 10)[0].ID)
	if item.Status != domain.KnowledgeSubmissionPending || gateCheck(item.GateChecks, "sensitive").OK {
		t.Fatalf("sensitive gate must block: %+v", item.GateChecks)
	}
}

func TestSensitiveRulesAreConfigurable(t *testing.T) {
	svc, store, _ := newSedimentFixture(t)
	base := store.bases["kb-p1"]
	base.SedimentRules = []domain.KnowledgeGateRule{
		{Name: "自定义标记", Pattern: "公开样例邮箱", Enabled: true},
	}
	store.bases["kb-p1"] = base
	store.artifacts["art-1"] = domain.Artifact{ID: "art-1", RunID: "run-1", NodeID: "write", Content: "# 安全\n\n公开样例邮箱不算敏感。\n"}
	if report, err := svc.SedimentOnce(context.Background()); err != nil || report.Queued != 1 {
		t.Fatalf("custom rule should match: %+v %v", report, err)
	}
	item, _ := store.KnowledgeSubmission(store.KnowledgeSubmissions("p1", "", 10)[0].ID)
	if gateCheck(item.GateChecks, "sensitive").OK {
		t.Fatal("custom sensitive rule was not evaluated")
	}

	if _, err := svc.SetSedimentConfig("p1", "kb-p1", domain.KnowledgeSedimentReview, []domain.KnowledgeGateRule{{Name: "坏正则", Pattern: "(", Enabled: true}}); err == nil {
		t.Fatal("invalid regex must be rejected")
	}
	if _, err := svc.SetSedimentConfig("p1", "kb-p1", "sometimes", nil); err == nil {
		t.Fatal("invalid mode must be rejected")
	}
}

func TestRetractWritesTombstoneAndBlocksResediment(t *testing.T) {
	svc, store, up := newSedimentFixture(t)
	base := store.bases["kb-p1"]
	base.SedimentPublishMode = domain.KnowledgeSedimentAuto
	store.bases["kb-p1"] = base
	if _, err := svc.SedimentOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	published := store.KnowledgeSubmissions("p1", "", 10)[0]
	item, err := svc.Retract(context.Background(), published.ID, "内容错误", "e2e 撤回", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.KnowledgeSubmissionRetracted || item.RetractedAt == nil {
		t.Fatalf("retracted item = %+v", item)
	}
	if len(up.retracted) != 1 || up.retracted[0] != "kb-hosted-c4b3147b9250b03a56cf4b06a349cce8\x00"+item.Path {
		t.Fatalf("retractor = %+v", up.retracted)
	}
	tombstone, ok := store.KnowledgeTombstone("p1", item.Fingerprint)
	if !ok || tombstone.SubmissionID != item.ID || !strings.HasPrefix(tombstone.Reason, "内容错误") {
		t.Fatalf("tombstone = %+v ok=%v", tombstone, ok)
	}

	// Same body from a later run is still enqueued for transparency, but the
	// tombstone gate fails and it is never auto-published.
	store.candidates = append(store.candidates, sedimentCandidate("run-2", "art-2", "write"))
	store.runs["run-2"] = domain.Run{
		ID: "run-2", WorkflowID: "wf", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "p1"},
		WorkflowSnapshot: store.workflows[0],
	}
	store.artifacts["art-2"] = domain.Artifact{ID: "art-2", RunID: "run-2", NodeID: "write", Name: "knowledge.md", Content: sedimentBody}
	report, err := svc.SedimentOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Queued != 1 || report.AutoPublished != 0 || len(up.written) != 1 {
		t.Fatalf("tombstoned content must not publish: %+v writes=%+v", report, up.written)
	}
	blocked := domain.KnowledgeSubmission{}
	for _, candidate := range store.KnowledgeSubmissions("p1", "", 10) {
		if candidate.ArtifactID == "art-2" {
			blocked, _ = store.KnowledgeSubmission(candidate.ID)
		}
	}
	if blocked.ID == "" || gateCheck(blocked.GateChecks, "tombstone").OK || blocked.Status == domain.KnowledgeSubmissionPublished {
		t.Fatalf("tombstone gate = %+v status=%s", blocked.GateChecks, blocked.Status)
	}
}

func TestPublishRechecksTombstoneAndSensitiveRules(t *testing.T) {
	svc, store, _ := newSedimentFixture(t)
	svc.now = func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) }
	item, err := svc.Submit(SubmissionInput{ProjectID: "p1", Title: "手工", Source: hostedSourceID("p1"), Path: "manual/a.md", Content: "手工正文，足够长可以发布。"}, "author")
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := KnowledgeFingerprint(item.Content)
	if err := store.PutKnowledgeTombstone(domain.KnowledgeTombstone{ProjectID: "p1", Fingerprint: fingerprint, Reason: "内容错误"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Publish(context.Background(), item.ID, "reviewer"); err == nil {
		t.Fatal("publishing tombstoned content must fail")
	}
	saved, _ := store.KnowledgeSubmission(item.ID)
	if saved.Status == domain.KnowledgeSubmissionPublished || gateCheck(saved.GateChecks, "tombstone").OK {
		t.Fatalf("publish must record tombstone gate: %+v", saved)
	}
}

func gateCheck(checks []domain.KnowledgeGateCheck, key string) domain.KnowledgeGateCheck {
	for _, check := range checks {
		if check.Key == key {
			return check
		}
	}
	return domain.KnowledgeGateCheck{Key: key, OK: false, Detail: "missing"}
}

func TestSedimentFingerprintGateBlocksDuplicateContent(t *testing.T) {
	svc, store, _ := newSedimentFixture(t)
	store.candidates = append(store.candidates, sedimentCandidate("run-2", "art-2", "write"))
	store.runs["run-2"] = domain.Run{
		ID: "run-2", WorkflowID: "wf", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "p1"},
		WorkflowSnapshot: store.workflows[0],
	}
	store.artifacts["art-2"] = domain.Artifact{ID: "art-2", RunID: "run-2", NodeID: "write", Name: "knowledge.md", Content: sedimentBody}
	report, err := svc.SedimentOnce(context.Background())
	if err != nil || report.Queued != 2 {
		t.Fatalf("report = %+v err=%v", report, err)
	}
	blocked := false
	for _, candidate := range store.KnowledgeSubmissions("p1", "", 10) {
		if candidate.ArtifactID != "art-2" {
			continue
		}
		item, _ := store.KnowledgeSubmission(candidate.ID)
		check := gateCheck(item.GateChecks, "fingerprint")
		if check.OK || !strings.Contains(check.Detail, "重复") {
			t.Fatalf("duplicate gate = %+v", check)
		}
		blocked = true
	}
	if !blocked {
		t.Fatal("duplicate artifact was not recorded")
	}
}

func TestSedimentEvidenceGateRejectsStubs(t *testing.T) {
	svc, store, _ := newSedimentFixture(t)
	store.artifacts["art-1"] = domain.Artifact{ID: "art-1", RunID: "run-1", NodeID: "write", Name: "knowledge.md", Content: "# 短\n\n太短。"}
	if report, err := svc.SedimentOnce(context.Background()); err != nil || report.Queued != 1 {
		t.Fatalf("report = %+v err=%v", report, err)
	}
	item, _ := store.KnowledgeSubmission(store.KnowledgeSubmissions("p1", "", 10)[0].ID)
	if gateCheck(item.GateChecks, "evidence").OK {
		t.Fatalf("short knowledge must fail evidence: %+v", item.GateChecks)
	}
}
