package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/mcpclient"
	"agentflow/internal/repo"
)

type failingActivityRepo struct {
	*repo.Store
}

func (failingActivityRepo) PutKnowledgeActivity(domain.KnowledgeActivity) error {
	return errors.New("activity store down")
}

type staticKnowledgeUpstream struct{}

func (staticKnowledgeUpstream) Tools(context.Context) ([]mcpclient.Tool, error) {
	return nil, nil
}

func (staticKnowledgeUpstream) ListSources(context.Context) ([]knowledge.Source, error) {
	return []knowledge.Source{{ID: "a", Name: "A"}}, nil
}

func (staticKnowledgeUpstream) Search(_ context.Context, query string, sources []string) (knowledge.SearchResult, error) {
	return knowledge.SearchResult{Query: query, Mode: "lexical", Results: []knowledge.SearchHit{{Source: "a", Path: "x.md", Title: "X"}}}, nil
}

func (staticKnowledgeUpstream) Fetch(context.Context, string, string, int, int) (knowledge.Document, error) {
	return knowledge.Document{}, nil
}

func (staticKnowledgeUpstream) WriteDocument(context.Context, string, string, string) (knowledge.WriteResult, error) {
	return knowledge.WriteResult{}, nil
}

func TestKnowledgeActivityWriteFailureDoesNotAffectToolCall(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutKnowledgeBase(domain.KnowledgeBase{ID: "a", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: "a", BaseID: "a", Name: "a", Kind: "hosted"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "p1", BaseID: "a", Permission: "submit"}); err != nil {
		t.Fatal(err)
	}
	svc := knowledge.NewService(failingActivityRepo{Store: s}, func() string { return "id1" }, staticKnowledgeUpstream{})

	text, hits, err := callKnowledgeTool(context.Background(), svc, knowledge.Caller{ProjectID: "p1", RunID: "run-1"}, knowledge.ToolSearch, map[string]any{"query": "x"})
	if err != nil {
		t.Fatalf("activity write failure leaked into the tool call: %v", err)
	}
	if hits != 1 || !strings.Contains(text, `"path":"x.md"`) {
		t.Fatalf("tool result changed: hits=%d text=%s", hits, text)
	}
}
