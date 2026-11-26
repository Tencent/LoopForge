package repo

import (
	"errors"
	"io"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

func TestArtifactBlobInterruptedUploadRestartsCleanly(t *testing.T) {
	store, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	const runID = "upload-run"
	id := domain.RunnerOutputBlobID("task", "plan.md")
	content := strings.Repeat("a", artifactBlobChunkBytes) + "\x00\xff"
	interrupted := io.MultiReader(strings.NewReader(content), interruptedArtifactBody{})
	if _, err := store.SaveArtifactBlob(t.Context(), runID, id, interrupted); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("interrupted upload returned %v", err)
	}
	if _, err := store.ArtifactBlob(t.Context(), runID, id); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("incomplete upload left a committed reference: %v", err)
	}
	blob, err := store.SaveArtifactBlob(t.Context(), runID, id, strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.SaveArtifactBlob(t.Context(), runID, id, strings.NewReader("another body"))
	if err != nil || replayed != blob || blob.Size != int64(len(content)) {
		t.Fatalf("blob=%+v replayed=%+v err=%v", blob, replayed, err)
	}
	_, body, err := store.OpenArtifactBlob(t.Context(), runID, id)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != content {
		t.Fatalf("read %d bytes, want %d: %v", len(got), len(content), err)
	}
}

type interruptedArtifactBody struct{}

func (interruptedArtifactBody) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
