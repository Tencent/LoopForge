package knowledge_test

import (
	"context"
	"testing"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	kbengine "agentflow/internal/knowledge/engine"
	"agentflow/internal/repo"
)

type scoredIndex struct{}

func (scoredIndex) Search(context.Context, string, []string, kbengine.SearchOptions) ([]kbengine.Hit, error) {
	return []kbengine.Hit{{Source: "shared", Path: "strong.md", Score: 2}, {Source: "shared", Path: "near.md", Score: 1.04}, {Source: "project", Path: "local.md", Score: 1}, {Source: "shared", Path: "equal.md", Score: 1}, {Source: "project", Path: "weak.md", Score: 0.5}}, nil
}
func TestSharedKnowledgeProjectPriorityAndOrigin(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, b := range []domain.KnowledgeBase{{ID: "local", ProjectID: "p", Layer: "project", Name: "本项目"}, {ID: "common", Layer: "shared", Name: "公共规范"}} {
		if err := st.PutKnowledgeBase(b); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []domain.KnowledgeSource{{ID: "project", BaseID: "local"}, {ID: "shared", BaseID: "common"}} {
		if err := st.PutKnowledgeSource(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "p", BaseID: "common", Permission: "read"}); err != nil {
		t.Fatal(err)
	}
	up := kbengine.NewEngine(st, st, scoredIndex{})
	svc := knowledge.NewService(st, func() string { return "id" }, up)
	for _, trial := range []bool{false, true} {
		var result knowledge.SearchResult
		if trial {
			out, e := svc.TrialSearch(context.Background(), up, "p", "规范", nil)
			err = e
			result = out.Result
		} else {
			out, e := svc.Search(context.Background(), "p", "规范", nil)
			err = e
			result = out.Result
		}
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"strong.md", "local.md", "near.md", "equal.md", "weak.md"}
		if len(result.Results) != len(want) {
			t.Fatalf("hits: %+v", result)
		}
		for i, path := range want {
			if result.Results[i].Path != path {
				t.Fatalf("project priority: %+v", result.Results)
			}
		}
		if result.Results[1].Layer != "project" || result.Results[1].BaseID != "local" || result.Results[1].BaseName != "本项目" {
			t.Fatalf("origin missing: %+v", result.Results[1])
		}
	}
}

func TestSharedKnowledgeBindingCannotPublishProjectSubmission(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutKnowledgeBase(domain.KnowledgeBase{ID: "common", Layer: "shared", Name: "Shared"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutKnowledgeSource(domain.KnowledgeSource{ID: "shared", BaseID: "common", Kind: "hosted"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "p", BaseID: "common", Permission: "submit"}); err != nil {
		t.Fatal(err)
	}
	up := kbengine.NewEngine(st, st, scoredIndex{})
	if _, err := up.WriteDocument(context.Background(), "shared", "rule.md", "# Original"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutKnowledgeSubmission(domain.KnowledgeSubmission{ID: "submission", ProjectID: "p", Source: "shared", Path: "rule.md", Content: "# Replaced", Status: domain.KnowledgeSubmissionPending}); err != nil {
		t.Fatal(err)
	}
	svc := knowledge.NewService(st, func() string { return "id" }, up)
	_, err = svc.Publish(context.Background(), "submission", "reviewer")
	if e, ok := apperr.As(err); !ok || e.Kind != apperr.Forbidden {
		t.Fatalf("shared publish must be forbidden: %v", err)
	}
	doc, err := svc.Document(context.Background(), "p", "shared", "rule.md", 0, 0)
	if err != nil || len(doc.Lines) != 1 || doc.Lines[0].Text != "# Original" {
		t.Fatalf("shared content changed: %+v %v", doc, err)
	}
}
