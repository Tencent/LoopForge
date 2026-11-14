package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestStageArchivedArtifactWritesPrivateTempFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0755); err != nil {
		t.Fatal(err)
	}
	base, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	temp, err := stageArchivedArtifact(base, "out", "hello\n")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(temp) != "out" {
		t.Fatalf("temp %q not staged under parent", temp)
	}
	data, err := os.ReadFile(filepath.Join(dir, temp))
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("content = %q, %v", data, err)
	}
	st, err := os.Stat(filepath.Join(dir, temp))
	if err != nil || st.Mode().Perm() != 0644 {
		t.Fatalf("mode = %v, %v", st, err)
	}
}

func TestStageArchivedArtifactGivesUpAfterCollisions(t *testing.T) {
	dir := t.TempDir()
	base, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	next := artifactTempSequence.Load()
	for i := uint64(1); i <= 10; i++ {
		name := fmt.Sprintf(".agentflow-artifact-%d-%d", os.Getpid(), next+i)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := stageArchivedArtifact(base, ".", "x"); err == nil || err.Error() != "cannot stage archived artifact" {
		t.Fatalf("err = %v", err)
	}
	if _, err := stageArchivedArtifact(base, ".", "x"); err != nil {
		t.Fatalf("staging after collisions: %v", err)
	}
}
