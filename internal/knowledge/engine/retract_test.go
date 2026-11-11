package engine

import (
	"context"
	"testing"

	"agentflow/internal/domain"
)

type retractContent struct {
	*fakeContent
	deleted []string
}

func (f *retractContent) DeleteKnowledgeDocument(source, path string) error {
	f.deleted = append(f.deleted, docKey(source, path))
	delete(f.docs, docKey(source, path))
	delete(f.chunks, docKey(source, path))
	return nil
}

func TestRetractDocumentDeletesBodyChunksAndCallsVectorHook(t *testing.T) {
	control := newFakeControl()
	control.sources["src"] = domain.KnowledgeSource{ID: "src", Kind: "hosted", Name: "托管文档"}
	content := &retractContent{fakeContent: newFakeContent()}
	e := newTestEngine(control, content.fakeContent, &fakeIndex{})
	// Swap the content store while keeping the same test clock/ids.
	e.content = content
	var deindexed []string
	e.SetVectorDeindexer(func(_ context.Context, source, path string) error {
		deindexed = append(deindexed, docKey(source, path))
		return nil
	})
	if _, err := e.WriteDocument(context.Background(), "src", "knowledge/a.md", "# A\n\n正文"); err != nil {
		t.Fatal(err)
	}
	if err := e.RetractDocument(context.Background(), "src", "knowledge/a.md"); err != nil {
		t.Fatal(err)
	}
	if len(content.deleted) != 1 || content.deleted[0] != docKey("src", "knowledge/a.md") {
		t.Fatalf("content deletion = %+v", content.deleted)
	}
	if len(deindexed) != 1 || deindexed[0] != docKey("src", "knowledge/a.md") {
		t.Fatalf("vector deindex hook = %+v", deindexed)
	}
	if _, ok := content.KnowledgeDocument("src", "knowledge/a.md"); ok {
		t.Fatal("document survived retraction")
	}
	if err := e.RetractDocument(context.Background(), "missing", "knowledge/a.md"); err == nil {
		t.Fatal("retracting an unknown source must fail")
	}
}

func TestRetractDocumentRejectsGitSource(t *testing.T) {
	control := newFakeControl()
	control.sources["git"] = domain.KnowledgeSource{ID: "git", Kind: "git", Name: "Git 来源"}
	content := &retractContent{fakeContent: newFakeContent()}
	e := newTestEngine(control, content.fakeContent, &fakeIndex{})
	e.content = content
	if err := e.RetractDocument(context.Background(), "git", "knowledge/a.md"); err == nil {
		t.Fatal("Git sources must be retracted through a merge request, not direct deletion")
	}
}
