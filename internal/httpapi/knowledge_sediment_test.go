package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	kbengine "agentflow/internal/knowledge/engine"
	"agentflow/internal/repo"
)

// chunkKeywordIndex 是 sqlite 测试用的关键词适配器：直接读内容面的索引块，
// 让检索结果随撤回后的块删除而变化。
type chunkKeywordIndex struct{ store *repo.Store }

func (i chunkKeywordIndex) Search(_ context.Context, query string, sources []string, _ kbengine.SearchOptions) ([]kbengine.Hit, error) {
	hits := []kbengine.Hit{}
	for _, source := range sources {
		for _, doc := range i.store.KnowledgeDocuments(source) {
			for _, chunk := range i.store.KnowledgeChunks(source, doc.Path) {
				if !strings.Contains(chunk.Text, query) {
					continue
				}
				hits = append(hits, kbengine.Hit{
					Source: source, Commit: doc.Commit, Path: doc.Path, Title: doc.Title,
					Heading: chunk.Heading, StartLine: chunk.StartLine, EndLine: chunk.EndLine,
					Text: chunk.Text, Score: 1,
				})
			}
		}
	}
	return hits, nil
}

func putSedimentRun(t *testing.T, s *repo.Store, projectID, runID, artifactID, content string) {
	t.Helper()
	agentID := "agent-" + projectID
	node := domain.WorkflowNode{ID: "write", Name: "写知识", Type: "agent", AgentID: agentID}
	workflow := domain.WorkflowDefinition{ID: "wf-" + projectID, Name: "知识流程", Nodes: []domain.WorkflowNode{node}}
	if _, ok := s.Agent(agentID); !ok {
		if err := s.PutAgent(domain.AgentDefinition{ID: agentID, Name: "知识角色", ProjectID: projectID, KnowledgeBase: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := s.Workflow(workflow.ID); !ok {
		if err := s.PutWorkflow(workflow); err != nil {
			t.Fatal(err)
		}
	}
	run := domain.Run{
		ID: runID, WorkflowID: workflow.ID, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: projectID},
		WorkflowSnapshot: workflow,
		Nodes:            map[string]*domain.NodeRun{"write": {NodeID: "write", Status: "succeeded", Attempt: 1}},
		Artifacts: []domain.Artifact{{
			ID: artifactID, RunID: runID, NodeID: "write", Name: "knowledge.md", Path: "knowledge.md",
			MediaType: "text/markdown", Content: content,
		}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
}

func putSedimentBase(t *testing.T, s *repo.Store, projectID, mode string) {
	t.Helper()
	baseID, sourceID := "kb-"+projectID, knowledge.HostedSourceID("kb-"+projectID)
	if err := s.PutKnowledgeBase(domain.KnowledgeBase{ID: baseID, ProjectID: projectID, Name: "项目知识库", Layer: "project", SedimentPublishMode: mode}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: sourceID, BaseID: baseID, Kind: "hosted", Name: "托管文档", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: projectID, BaseID: baseID, Permission: "submit"}); err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgeSedimentHTTPFlowAndRetraction(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putSedimentBase(t, s, "p1", domain.KnowledgeSedimentReview)
	putSedimentRun(t, s, "p1", "run-1", "art-1", "# 青鸾排障\n\n## 验证\n\n执行验证命令通过，青鸾标记可复用。\n")
	engine := kbengine.NewEngine(s, s, chunkKeywordIndex{store: s})
	svc := knowledge.NewService(s, knowledge.NewID, engine)
	if report, err := svc.SedimentOnce(context.Background()); err != nil || report.Queued != 1 {
		t.Fatalf("sediment = %+v %v", report, err)
	}
	handler := newKnowledgeTestHandlerWithUpstream(t, s, engine)

	rec := doJSON(t, handler, http.MethodGet, "/api/knowledge/submissions?projectId=p1", "", nil)
	var items []domain.KnowledgeSubmission
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil || len(items) != 1 {
		t.Fatalf("submissions = %s (%v)", rec.Body.String(), err)
	}
	if len(items[0].GateChecks) != 4 || !items[0].GateChecks[0].OK || len(items[0].SedimentSignals) == 0 {
		t.Fatalf("review gates = %s", rec.Body.String())
	}
	id := items[0].ID

	// 人工通过后写回托管库，检索能命中。
	rec = doJSON(t, handler, http.MethodPost, "/api/knowledge/submissions/"+id+"/publish", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"published"`) {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, handler, http.MethodPost, "/api/knowledge/search", `{"projectId":"p1","query":"青鸾"}`, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "青鸾排障") {
		t.Fatalf("search after publish = %d %s", rec.Code, rec.Body.String())
	}

	// 撤回：墓碑、删原文、去索引，随后检索不到。
	rec = doJSON(t, handler, http.MethodPost, "/api/knowledge/submissions/"+id+"/retract", `{"reasonCategory":"内容错误","reasonDetail":"e2e 撤回"}`, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"retracted"`) {
		t.Fatalf("retract = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, handler, http.MethodPost, "/api/knowledge/search", `{"projectId":"p1","query":"青鸾"}`, nil)
	if strings.Contains(rec.Body.String(), "青鸾排障") {
		t.Fatalf("retracted text still searchable: %s", rec.Body.String())
	}

	// 同内容再次产出被墓碑拦截，自动发布模式也不会写回。
	putSedimentBaseMode(t, s, "p1", domain.KnowledgeSedimentAuto)
	putSedimentRun(t, s, "p1", "run-2", "art-2", "# 青鸾排障\n\n## 验证\n\n执行验证命令通过，青鸾标记可复用。\n")
	report, err := knowledge.NewService(s, knowledge.NewID, engine).SedimentOnce(context.Background())
	if err != nil || report.Queued != 1 || report.AutoPublished != 0 {
		t.Fatalf("re-sediment = %+v %v", report, err)
	}
	rec = doJSON(t, handler, http.MethodGet, "/api/knowledge/submissions?projectId=p1", "", nil)
	if !strings.Contains(rec.Body.String(), `"key":"tombstone","name":"墓碑检查","ok":false`) {
		t.Fatalf("review page must show tombstone gate: %s", rec.Body.String())
	}
	docs := doJSON(t, handler, http.MethodGet, "/api/knowledge/bases/documents?projectId=p1&source=kb-hosted-c4b3147b9250b03a56cf4b06a349cce8", "", nil)
	if strings.Contains(docs.Body.String(), "青鸾排障") {
		t.Fatalf("tombstoned content was written back: %s", docs.Body.String())
	}
}

func putSedimentBaseMode(t *testing.T, s *repo.Store, projectID, mode string) {
	t.Helper()
	base, ok := s.KnowledgeBase("kb-" + projectID)
	if !ok {
		t.Fatal("base missing")
	}
	base.SedimentPublishMode = mode
	if err := s.PutKnowledgeBase(base); err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgeSedimentAutoPublishModeAndConfigAPI(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putSedimentBase(t, s, "p2", domain.KnowledgeSedimentReview)
	putSedimentRun(t, s, "p2", "run-3", "art-3", "# 自动发布\n\n## 验证\n\n门禁全过后直接写回，朱雀标记可检索。\n")
	engine := kbengine.NewEngine(s, s, chunkKeywordIndex{store: s})
	handler := newKnowledgeTestHandlerWithUpstream(t, s, engine)

	rec := doJSON(t, handler, http.MethodPut, "/api/knowledge/bases/sediment", `{"projectId":"p2","baseId":"kb-p2","mode":"auto","rules":[{"name":"邮箱","pattern":"[A-Z0-9._%+-]+@[A-Z0-9.-]+\\.[A-Z]{2,}","enabled":true}]}`, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sedimentPublishMode":"auto"`) {
		t.Fatalf("config = %d %s", rec.Code, rec.Body.String())
	}
	report, err := knowledge.NewService(s, knowledge.NewID, engine).SedimentOnce(context.Background())
	if err != nil || report.Queued != 1 || report.AutoPublished != 1 {
		t.Fatalf("auto publish = %+v %v", report, err)
	}
	rec = doJSON(t, handler, http.MethodGet, "/api/knowledge/submissions?projectId=p2&status=published", "", nil)
	if !strings.Contains(rec.Body.String(), `"status":"published"`) || !strings.Contains(rec.Body.String(), `"sedimentSignals":["run_succeeded"]`) {
		t.Fatalf("published submission = %s", rec.Body.String())
	}
	rec = doJSON(t, handler, http.MethodPost, "/api/knowledge/search", `{"projectId":"p2","query":"朱雀"}`, nil)
	if !strings.Contains(rec.Body.String(), "自动发布") {
		t.Fatalf("auto-published search = %s", rec.Body.String())
	}
	rec = doJSON(t, handler, http.MethodGet, "/api/knowledge/bases/sediment?projectId=p2&baseId=kb-p2", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"mode":"auto"`) || !strings.Contains(rec.Body.String(), `"name":"邮箱"`) {
		t.Fatalf("read config = %d %s", rec.Code, rec.Body.String())
	}
}
