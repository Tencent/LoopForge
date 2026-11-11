package knowledge_test

import (
	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/knowledge/engine"
	"agentflow/internal/repo"
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

type blockedPublisher struct {
	knowledge.Upstream
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockedPublisher) WriteDocument(ctx context.Context, source, path, content string) (knowledge.WriteResult, error) {
	if b.calls.Add(1) == 1 {
		close(b.entered)
	}
	<-b.release
	return b.Upstream.WriteDocument(ctx, source, path, content)
}
func TestConcurrentPublishRejectAndDelete(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := knowledge.NewService(st, func() string { return "concurrency" }, nil)
	base, err := svc.EnsureHostedBase("concurrency-project")
	if err != nil {
		t.Fatal(err)
	}
	sources := st.KnowledgeSources(base.ID)
	if len(sources) != 1 {
		t.Fatal(sources)
	}
	up := &blockedPublisher{Upstream: engine.NewEngine(st, st.KnowledgeContentRepo, nil), entered: make(chan struct{}), release: make(chan struct{})}
	svc = knowledge.NewService(st, func() string { return "concurrency" }, up)
	item, err := svc.Submit(knowledge.SubmissionInput{ProjectID: "concurrency-project", Source: sources[0].ID, Path: "test.md", Title: "Concurrent publication", Content: "Concurrent publication content."}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	defer st.DeleteKnowledgeSubmission(item.ID)
	done := make(chan error, 1)
	go func() { _, err := svc.Publish(context.Background(), item.ID, "one"); done <- err }()
	select {
	case <-up.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher did not enter")
	}
	second := make(chan error, 1)
	go func() { _, err := svc.Publish(context.Background(), item.ID, "two"); second <- err }()
	select {
	case err := <-second:
		if err == nil {
			t.Error("double publish accepted")
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("double publish performed external write")
	}
	if _, err := svc.Reject(item.ID, "reject", "three"); err == nil {
		t.Error("reject overwrote active publish")
	}
	if err := svc.Delete(item.ID); err == nil {
		t.Error("delete removed active publish")
	}
	close(up.release)
	if err := <-done; err != nil {
		t.Error(err)
	}
	if up.calls.Load() != 1 {
		t.Errorf("writes=%d", up.calls.Load())
	}
}

func (b *blockedPublisher) RetractDocument(ctx context.Context, source, path string) error {
	return b.Upstream.(knowledge.DocumentRetractor).RetractDocument(ctx, source, path)
}
func TestRetractDuringPublishCannotReviveDocument(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	project := "concurrency-retract-publish"
	svc := knowledge.NewService(st, nil, nil)
	base, err := svc.EnsureHostedBase(project)
	if err != nil {
		t.Fatal(err)
	}
	source := st.KnowledgeSources(base.ID)[0].ID
	defer func() {
		st.DeleteKnowledgeSourceContent(source)
		st.DB.Exec("DELETE FROM knowledge_tombstones WHERE project_id = ?", project)
	}()
	up := &blockedPublisher{Upstream: engine.NewEngine(st, st, nil), entered: make(chan struct{}), release: make(chan struct{})}
	svc = knowledge.NewService(st, nil, up)
	item, err := svc.Submit(knowledge.SubmissionInput{ProjectID: project, Source: source, Path: "race.md", Content: "# Race\n\nReusable content."}, "one")
	if err != nil {
		t.Fatal(err)
	}
	defer st.DeleteKnowledgeSubmission(item.ID)
	done := make(chan error, 1)
	go func() { _, err := svc.Publish(context.Background(), item.ID, "one"); done <- err }()
	<-up.entered
	if _, err := svc.Retract(context.Background(), item.ID, "wrong", "", "two"); err != nil {
		t.Error(err)
	}
	close(up.release)
	if err := <-done; err == nil {
		t.Error("publisher ignored concurrent retraction")
	}
	if _, ok := st.KnowledgeDocument(source, "race.md"); ok {
		t.Error("publisher revived retracted content")
	}
	got, _ := st.KnowledgeSubmission(item.ID)
	if got.Status != "retracted" {
		t.Errorf("status=%s", got.Status)
	}
	if _, ok := st.KnowledgeTombstone(project, knowledge.KnowledgeFingerprint(item.Content)); !ok {
		t.Error("tombstone missing")
	}
}

type simultaneousReads struct {
	engine.ContentStore
	reads atomic.Int32
	ready chan struct{}
}

func (c *simultaneousReads) KnowledgeDocument(source, path string) (domain.KnowledgeDocument, bool) {
	doc, ok := c.ContentStore.KnowledgeDocument(source, path)
	n := c.reads.Add(1)
	if n == 2 {
		close(c.ready)
	}
	if n <= 2 {
		<-c.ready
	}
	return doc, ok
}
func TestConcurrentHostedWritesPreserveBothRevisions(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	source := "concurrency-hosted-writes"
	if err := st.PutKnowledgeSource(domain.KnowledgeSource{ID: source, Kind: "hosted"}); err != nil {
		t.Fatal(err)
	}
	defer st.DeleteKnowledgeSource(source)
	defer st.DeleteKnowledgeSourceContent(source)
	content := &simultaneousReads{ContentStore: st, ready: make(chan struct{})}
	e := engine.NewEngine(st, content, nil)
	done := make(chan error, 2)
	for _, body := range []string{"# One\n\nFirst document.", "# Two\n\nSecond document."} {
		go func(body string) {
			_, err := e.WriteDocument(context.Background(), source, "same.md", body)
			done <- err
		}(body)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	doc, _ := st.KnowledgeDocument(source, "same.md")
	revs := st.KnowledgeRevisions(source, "same.md")
	if doc.Revision != 2 || len(revs) != 2 || revs[0].Body == revs[1].Body || doc.Body != revs[1].Body {
		t.Fatalf("document/history mismatch: %+v %+v", doc, revs)
	}
}

func TestCorrectedContentCanPublishAfterRetraction(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	project := "concurrency-corrected"
	svc := knowledge.NewService(st, nil, nil)
	base, err := svc.EnsureHostedBase(project)
	if err != nil {
		t.Fatal(err)
	}
	source := st.KnowledgeSources(base.ID)[0].ID
	defer st.DeleteKnowledgeSourceContent(source)
	defer st.DB.Exec("DELETE FROM knowledge_tombstones WHERE project_id=?", project)
	svc = knowledge.NewService(st, nil, engine.NewEngine(st, st, nil))
	first, err := svc.Submit(knowledge.SubmissionInput{ProjectID: project, Source: source, Path: "guide.md", Content: "# Original\n\nOld guidance."}, "one")
	if err != nil {
		t.Fatal(err)
	}
	defer st.DeleteKnowledgeSubmission(first.ID)
	if _, err := svc.Publish(context.Background(), first.ID, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Retract(context.Background(), first.ID, "wrong", "", "one"); err != nil {
		t.Fatal(err)
	}
	corrected, err := svc.Submit(knowledge.SubmissionInput{ProjectID: project, Source: source, Path: "guide.md", Content: "# Corrected\n\nCorrect guidance."}, "two")
	if err != nil {
		t.Fatal(err)
	}
	defer st.DeleteKnowledgeSubmission(corrected.ID)
	if _, err := svc.Publish(context.Background(), corrected.ID, "two"); err != nil {
		t.Fatalf("corrected content rejected: %v", err)
	}
}
