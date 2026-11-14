package engine

import (
	"agentflow/internal/domain"
	"reflect"
	"testing"
)

func TestExpectedDeliveryFiles(t *testing.T) {
	t.Parallel()
	newFile := func(path, body string) string {
		return "diff --git a/" + path + " b/" + path + "\nnew file mode 100644\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1 @@\n+" + body + "\n"
	}
	artifacts := []domain.Artifact{
		{Name: "summary", NodeID: "dev", Content: newFile("ignored.go", "x")},
		{Name: "repository-patch", NodeID: "dev", Content: "   "},
		{Name: "repository-patch", NodeID: "dev", Content: newFile("a.go", "dev")},
		{Name: "repository-patch", NodeID: "test", Content: newFile("a.go", "test") + newFile("b.go", "test")},
		{Name: "repository-patch", NodeID: "mr", Content: newFile("b.go", "mr")},
		{Name: "repository-patch", NodeID: "dev", RepositoryDir: " web ", Content: "A\tsrc/app.ts\nM\tsrc/old.ts"},
	}
	got := expectedDeliveryFiles(artifacts)
	want := map[string]map[string]string{
		"":    {"a.go": "dev", "b.go": "mr"},
		"web": {"src/app.ts": "dev"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expectedDeliveryFiles = %v, want %v", got, want)
	}
	if got := expectedDeliveryFiles(nil); len(got) != 0 {
		t.Fatalf("expectedDeliveryFiles(nil) = %v, want empty", got)
	}
}

func TestCommittedFiles(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	gitInit(t, ws)
	gitCommitFile(t, ws, "a.go", "package a\n")
	gitCommitFile(t, ws, "dir/b.go", "package b\n")
	got, err := committedFiles(ws)
	if err != nil {
		t.Fatalf("committedFiles: %v", err)
	}
	if want := map[string]bool{"a.go": true, "dir/b.go": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("committedFiles = %v, want %v", got, want)
	}
	if _, err := committedFiles(t.TempDir()); err == nil {
		t.Fatal("committedFiles on a non-git directory should fail")
	}
}
