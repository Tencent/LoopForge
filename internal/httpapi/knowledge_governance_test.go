package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
)

func governanceOverview(t *testing.T, h http.Handler, projectID string) knowledge.GovernanceOverview {
	t.Helper()
	rec := doJSON(t, h, http.MethodGet, "/api/knowledge/governance?projectId="+projectID, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("governance overview: %d %s", rec.Code, rec.Body.String())
	}
	var out knowledge.GovernanceOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func writeGovernanceDoc(t *testing.T, h http.Handler, projectID, sourceID, path, content string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"projectId": projectID, "sourceId": sourceID, "path": path, "content": content,
	})
	rec := doJSON(t, h, http.MethodPost, "/api/knowledge/bases/documents", string(body), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("write %s: %d %s", path, rec.Code, rec.Body.String())
	}
}

func TestKnowledgeGovernancePanelScanFeedbackGapsAndDisposition(t *testing.T) {
	s, h := newKBMgmtServer(t)
	createProject(t, h, "pGov")
	bases := s.KnowledgeBases("pGov")
	if len(bases) != 1 {
		t.Fatalf("bases = %+v", bases)
	}
	sourceID := "kb-hosted-pGov"

	writeGovernanceDoc(t, h, "pGov", sourceID, "stale.md", "---\ntitle: 过期文档\nowner: alice\nstatus: verified\nupdated: 2020-01-01\n---\n# 过期文档\n")
	writeGovernanceDoc(t, h, "pGov", sourceID, "owner.md", "---\ntitle: 缺负责人\nstatus: draft\nupdated: 2026-11-01\n---\n# 缺负责人\n")
	writeGovernanceDoc(t, h, "pGov", sourceID, "link.md", "---\ntitle: 死链文档\nowner: alice\nstatus: verified\nupdated: 2026-11-01\n---\n# 死链文档\n\n[缺失](missing.md)\n")
	writeGovernanceDoc(t, h, "pGov", sourceID, "dup-a.md", "---\ntitle: 重复文档\nowner: alice\nstatus: verified\nupdated: 2026-11-01\n---\n# 重复文档\n")
	writeGovernanceDoc(t, h, "pGov", sourceID, "dup-b.md", "---\ntitle: 重复文档\nowner: bob\nstatus: draft\nupdated: 2026-11-01\n---\n# 重复文档\n")

	rec := doJSON(t, h, http.MethodPost, "/api/knowledge/governance/scan", `{"projectId":"pGov"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("scan: %d %s", rec.Code, rec.Body.String())
	}
	overview := governanceOverview(t, h, "pGov")
	for _, kind := range []string{"stale", "missing_owner", "dead_link", "duplicate"} {
		if overview.Counts[kind] == 0 {
			t.Errorf("missing health issue %s: counts=%+v issues=%+v", kind, overview.Counts, overview.Issues)
		}
	}
	if overview.LastScanAt == nil || overview.Placeholders.CitationCounts || overview.Placeholders.NeverHit {
		t.Fatalf("overview contract = %+v", overview)
	}

	// 知识中心试搜/读原文反馈。
	feedbackBody := `{"projectId":"pGov","source":"` + sourceID + `","path":"stale.md","kind":"helpful","surface":"knowledge_center"}`
	if rec := doJSON(t, h, http.MethodPost, "/api/knowledge/feedback", feedbackBody, nil); rec.Code != http.StatusOK {
		t.Fatalf("feedback: %d %s", rec.Code, rec.Body.String())
	}
	overview = governanceOverview(t, h, "pGov")
	if overview.FeedbackCounts[domain.KnowledgeFeedbackHelpful] != 1 || len(overview.Feedback) != 1 {
		t.Fatalf("feedback overview = %+v", overview)
	}

	// 搜不到结果的查询进入知识缺口清单。
	_, result := searchWord(t, h, "pGov", "完全不存在的问题", nil)
	if len(result.Result.Results) != 0 {
		t.Fatalf("unexpected hits: %+v", result.Result.Results)
	}
	overview = governanceOverview(t, h, "pGov")
	if len(overview.Gaps) != 1 || overview.Gaps[0].Query != "完全不存在的问题" || overview.Gaps[0].Count != 1 {
		t.Fatalf("gaps = %+v", overview.Gaps)
	}

	// 从面板发起下线：进入审核队列而不是直接删正文。
	disposeBody := `{"projectId":"pGov","source":"` + sourceID + `","path":"stale.md","action":"retract","note":"内容已经过时"}`
	dispose := doJSON(t, h, http.MethodPost, "/api/knowledge/governance/dispose", disposeBody, nil)
	if dispose.Code != http.StatusOK {
		t.Fatalf("dispose: %d %s", dispose.Code, dispose.Body.String())
	}
	var item domain.KnowledgeSubmission
	if err := json.Unmarshal(dispose.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.KnowledgeSubmissionPending || item.Action != domain.KnowledgeSubmissionActionRetract {
		t.Fatalf("dispose submission = %+v", item)
	}
	if _, ok := s.KnowledgeDocument(sourceID, "stale.md"); !ok {
		t.Fatal("disposition must not delete content before review")
	}

	published := doJSON(t, h, http.MethodPost, "/api/knowledge/submissions/"+item.ID+"/publish", "", nil)
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), `"status":"retracted"`) {
		t.Fatalf("publish retraction: %d %s", published.Code, published.Body.String())
	}
	if _, ok := s.KnowledgeDocument(sourceID, "stale.md"); ok {
		t.Fatal("publishing the retraction must remove the hosted document")
	}
}

func TestKnowledgeGovernanceScanIsProjectScoped(t *testing.T) {
	s, h := newKBMgmtServer(t)
	createProject(t, h, "pA")
	createProject(t, h, "pB")
	writeGovernanceDoc(t, h, "pA", "kb-hosted-pA", "a.md", "# A\n")
	if rec := doJSON(t, h, http.MethodPost, "/api/knowledge/governance/scan", `{"projectId":"pA"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("scan A: %d %s", rec.Code, rec.Body.String())
	}
	overview := governanceOverview(t, h, "pB")
	if overview.Counts["total"] != 0 || len(overview.Issues) != 0 {
		t.Fatalf("project B saw project A issues: %+v", overview)
	}
	if _, ok := s.KnowledgeBase("kb-pB"); !ok {
		t.Fatal("project B should have its own base")
	}
}
