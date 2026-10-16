package preview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestFailDoesNotOverwriteReadyPreview(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	svc := NewService(store, NewDBStore(store), Config{
		PublicScheme: "http", FinalizationTimeout: time.Minute,
		DefaultRetention: time.Hour, MaxFiles: 20, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20,
	})
	preview := domain.RunPreview{
		ID: "preview-ready-cas", RunID: "run-ready-cas", Kind: domain.PreviewKindStatic,
		Status: domain.PreviewReady, SourcePath: "dist", EntryFile: "index.html",
		StorageKey: "abc", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := store.PutPreview(preview); err != nil {
		t.Fatal(err)
	}
	svc.Fail(preview, errors.New("stale archiver"))
	got, ok := store.Preview(preview.ID)
	if !ok || got.Status != domain.PreviewReady {
		t.Fatalf("ready preview overwritten: %+v", got)
	}
}

func TestConcurrentArchiveLocalKeepsReady(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	svc := NewService(store, NewDBStore(store), Config{
		PublicScheme: "http", FinalizationTimeout: time.Minute,
		DefaultRetention: time.Hour, MaxFiles: 20, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20,
	})
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "index.html"), []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "run-archive-cas", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist"}},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	preview := domain.RunPreview{
		ID: repo.NewPreviewID(run.ID), RunID: run.ID, Kind: domain.PreviewKindStatic,
		Status: domain.PreviewPending, SourcePath: "dist", EntryFile: "index.html",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := store.PutPreview(preview); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := svc.ArchiveLocal(context.Background(), run, preview, workspace)
			if err != nil {
				svc.Fail(preview, err)
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	got, ok := store.PreviewByRun(run.ID)
	if !ok || got.Status != domain.PreviewReady {
		t.Fatalf("concurrent archive lost ready state: %+v", got)
	}
	for err := range errs {
		if err != nil && err != ErrPreviewBusy && err != ErrPreviewGone {
			t.Fatalf("unexpected archive error: %v", err)
		}
	}
}
