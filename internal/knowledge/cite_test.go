package knowledge_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	kbengine "agentflow/internal/knowledge/engine"
	"agentflow/internal/repo"
)

func TestCitationFreshnessCompareCurrentDocument(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutKnowledgeBase(domain.KnowledgeBase{ID: "local", ProjectID: "p", Layer: "project", Name: "本项目"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutKnowledgeSource(domain.KnowledgeSource{ID: "docs", BaseID: "local", Kind: "hosted", Name: "托管"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutKnowledgeBase(domain.KnowledgeBase{ID: "other", ProjectID: "q", Layer: "project", Name: "别人"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutKnowledgeSource(domain.KnowledgeSource{ID: "foreign", BaseID: "other", Kind: "hosted"}); err != nil {
		t.Fatal(err)
	}
	eng := kbengine.NewEngine(st, st, kbengine.NewMySQLKeywordIndex(st))
	if _, err := eng.WriteDocument(context.Background(), "docs", "guide.md", "# 指南\n\n版本一\n"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutKnowledgeDocument(domain.KnowledgeDocument{SourceID: "foreign", Path: "secret.md", Body: "别人的正文", Commit: "1"}); err != nil {
		t.Fatal(err)
	}
	svc := knowledge.NewService(st, func() string { return "id" }, eng)
	current := svc.JudgeFreshness("p", "docs", "guide.md", "1")
	if current.Status != knowledge.FreshnessCurrent || current.Current != "1" {
		t.Fatalf("current = %+v", current)
	}
	if _, err := eng.WriteDocument(context.Background(), "docs", "guide.md", "# 指南\n\n版本二\n"); err != nil {
		t.Fatal(err)
	}
	changed := svc.JudgeFreshness("p", "docs", "guide.md", "1")
	if changed.Status != knowledge.FreshnessChanged || changed.Current != "2" {
		t.Fatalf("changed = %+v", changed)
	}
	if err := st.DeleteKnowledgeDocument("docs", "guide.md"); err != nil {
		t.Fatal(err)
	}
	deleted := svc.JudgeFreshness("p", "docs", "guide.md", "2")
	if deleted.Status != knowledge.FreshnessDeleted || deleted.Current != "" {
		t.Fatalf("deleted = %+v", deleted)
	}
	hidden := svc.JudgeFreshness("p", "foreign", "secret.md", "1")
	if hidden.Status != knowledge.FreshnessDeleted || hidden.Current != "" {
		t.Fatalf("other project must look deleted, not reveal current: %+v", hidden)
	}
}

func TestCitationUsageCountsAndUncitedStayInsideProject(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, base := range []domain.KnowledgeBase{
		{ID: "local", ProjectID: "p", Layer: "project", Name: "本项目"},
		{ID: "common", Layer: "shared", Name: "公共"},
		{ID: "other", ProjectID: "q", Layer: "project", Name: "别人"},
	} {
		if err := st.PutKnowledgeBase(base); err != nil {
			t.Fatal(err)
		}
	}
	for _, src := range []domain.KnowledgeSource{
		{ID: "docs", BaseID: "local", Kind: "hosted"},
		{ID: "shared", BaseID: "common", Kind: "hosted"},
		{ID: "foreign", BaseID: "other", Kind: "hosted"},
	} {
		if err := st.PutKnowledgeSource(src); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "p", BaseID: "common", Permission: "read"}); err != nil {
		t.Fatal(err)
	}
	docs := []domain.KnowledgeDocument{
		{SourceID: "docs", Path: "used.md", Title: "用过", Body: "正文", Commit: "1"},
		{SourceID: "docs", Path: "idle.md", Title: "闲置", Body: "还没人引用", Commit: "1"},
		{SourceID: "docs", Path: "empty.md", Body: "   ", Commit: "1"},
		{SourceID: "shared", Path: "norm.md", Title: "规范", Body: "公共正文", Commit: "4"},
		{SourceID: "foreign", Path: "secret.md", Body: "别人的", Commit: "1"},
	}
	for _, doc := range docs {
		if err := st.PutKnowledgeDocument(doc); err != nil {
			t.Fatal(err)
		}
	}
	svc := knowledge.NewService(st, func() string { return "id" }, nil)
	caller := knowledge.Caller{ProjectID: "p", AgentID: "dev", UserID: 7, RunID: "run-1", SessionID: "chat-1"}
	items := knowledge.CitationsForSearch(caller, time.Now().UTC(), knowledge.SearchResult{Results: []knowledge.SearchHit{
		{BaseOrigin: knowledge.BaseOrigin{BaseID: "local", Layer: "project"}, Source: "docs", Path: "used.md", Commit: "1", StartLine: 1, EndLine: 2},
		{BaseOrigin: knowledge.BaseOrigin{BaseID: "local", Layer: "project"}, Source: "docs", Path: "used.md", Commit: "1", StartLine: 3, EndLine: 4},
	}})
	items = append(items, knowledge.CitationsForGrep(caller, time.Now().UTC(), knowledge.GrepResult{Hits: []knowledge.GrepHit{
		{BaseOrigin: knowledge.BaseOrigin{BaseID: "common", Layer: "shared"}, Source: "shared", Path: "norm.md", Commit: "4", Line: 2},
	}})...)
	items = append(items, domain.KnowledgeCitation{
		At:        time.Now().UTC().Add(-domain.KnowledgeCitationWindow - time.Hour),
		ProjectID: "p", Tool: knowledge.ToolFetch, SourceID: "docs", Path: "idle.md", Commit: "1", StartLine: 1, EndLine: 1,
	})
	items = append(items, domain.KnowledgeCitation{
		At: time.Now().UTC(), ProjectID: "q", Tool: knowledge.ToolSearch, SourceID: "foreign", Path: "secret.md", Commit: "1",
	})
	if err := svc.RecordCitations(items); err != nil {
		t.Fatal(err)
	}
	usage, ok := svc.CitationUsage("p")
	if !ok {
		t.Fatal("citation store must be available")
	}
	got := map[string]int{}
	for _, item := range usage.Cited {
		got[item.SourceID+"/"+item.Path] = item.Count
	}
	if got["docs/used.md"] != 2 || got["shared/norm.md"] != 1 || got["docs/idle.md"] != 0 || got["foreign/secret.md"] != 0 {
		t.Fatalf("counts = %+v cited=%+v", got, usage.Cited)
	}
	uncited := map[string]bool{}
	for _, item := range usage.Uncited {
		uncited[item.SourceID+"/"+item.Path] = true
		if item.Path == "idle.md" && (item.Title != "闲置" || item.Layer != "project") {
			t.Fatalf("uncited metadata = %+v", item)
		}
	}
	if !uncited["docs/idle.md"] || uncited["docs/used.md"] || uncited["docs/empty.md"] || uncited["foreign/secret.md"] || uncited["shared/norm.md"] {
		t.Fatalf("uncited = %+v", usage.Uncited)
	}
	other, ok := svc.CitationUsage("q")
	if !ok {
		t.Fatal(ok)
	}
	for _, item := range other.Cited {
		if item.Path == "used.md" || item.SourceID == "docs" {
			t.Fatalf("project q saw project p citations: %+v", other.Cited)
		}
	}
	if len(svc.Citations("q", 10)) != 1 || svc.Citations("q", 10)[0].Path != "secret.md" {
		t.Fatalf("q citations = %+v", svc.Citations("q", 10))
	}
	if len(svc.Citations("", 10)) != 0 {
		t.Fatal("empty project must not read citations")
	}
}

func TestGrepReadsProjectAndBoundSharedBodiesOnly(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, base := range []domain.KnowledgeBase{
		{ID: "local", ProjectID: "p", Layer: "project", Name: "本项目"},
		{ID: "common", Layer: "shared", Name: "公共"},
		{ID: "hidden", Layer: "shared", Name: "未加载"},
		{ID: "other", ProjectID: "q", Layer: "project", Name: "别人"},
	} {
		if err := st.PutKnowledgeBase(base); err != nil {
			t.Fatal(err)
		}
	}
	for _, src := range []domain.KnowledgeSource{
		{ID: "local-src", BaseID: "local", Kind: "hosted"},
		{ID: "shared-src", BaseID: "common", Kind: "hosted"},
		{ID: "hidden-src", BaseID: "hidden", Kind: "hosted"},
		{ID: "foreign-src", BaseID: "other", Kind: "hosted"},
	} {
		if err := st.PutKnowledgeSource(src); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "p", BaseID: "common", Permission: "read"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "p", BaseID: "other", Permission: "read"}); err != nil {
		t.Fatal(err)
	}
	for _, doc := range []domain.KnowledgeDocument{
		{SourceID: "local-src", Path: "a.md", Body: "TOKEN-LOCAL\n", Commit: "1"},
		{SourceID: "shared-src", Path: "b.md", Body: "TOKEN-SHARED\n", Commit: "1"},
		{SourceID: "hidden-src", Path: "c.md", Body: "TOKEN-HIDDEN\n", Commit: "1"},
		{SourceID: "foreign-src", Path: "d.md", Body: "TOKEN-FOREIGN\n", Commit: "1"},
	} {
		if err := st.PutKnowledgeDocument(doc); err != nil {
			t.Fatal(err)
		}
	}
	svc := knowledge.NewService(st, func() string { return "id" }, nil)
	out, err := svc.Grep(context.Background(), "p", knowledge.GrepQuery{Pattern: "TOKEN-"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, hit := range out.Hits {
		got[hit.Text] = true
	}
	if !got["TOKEN-LOCAL"] || !got["TOKEN-SHARED"] || got["TOKEN-HIDDEN"] || got["TOKEN-FOREIGN"] {
		t.Fatalf("visible hits = %+v", out.Hits)
	}
	if _, err := svc.Grep(context.Background(), "p", knowledge.GrepQuery{Pattern: "TOKEN-FOREIGN", Sources: []string{"foreign-src"}}); err == nil || !strings.Contains(err.Error(), "授权范围") {
		t.Fatalf("foreign source must stay outside the project: %v", err)
	}
}
