package repo

import (
	"agentflow/internal/domain"
	"testing"
)

func TestMCPFilesRoundTripRefreshesManifestAndDelete(t *testing.T) {
	store, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	server := domain.MCPServerDefinition{ID: "custom", Name: "Custom", Transport: "stdio", Command: "python3"}
	if err := store.PutMCPServer(server); err != nil {
		t.Fatal(err)
	}
	if err := store.PutMCPFiles(server.ID, []SkillFileInput{{Path: "server.py", Content: []byte("print('ok')\n"), Mode: 0o755}, {Path: "pkg/data.txt", Content: []byte("data")}}); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.MCPServer(server.ID)
	if len(stored.FileRefs) != 2 {
		t.Fatalf("fileRefs=%#v", stored.FileRefs)
	}
	if got, ok := store.MCPFileContent(server.ID, "server.py"); !ok || string(got) != "print('ok')\n" {
		t.Fatalf("content=%q ok=%v", got, ok)
	}
	stored.Description = "definition update"
	stored.FileRefs = nil
	if err := store.PutMCPServer(stored); err != nil {
		t.Fatal(err)
	}
	stored, _ = store.MCPServer(server.ID)
	if len(stored.FileRefs) != 2 {
		t.Fatalf("definition update erased fileRefs: %#v", stored.FileRefs)
	}
	if err := store.DeleteMCPFile(server.ID, "pkg/data.txt"); err != nil {
		t.Fatal(err)
	}
	stored, _ = store.MCPServer(server.ID)
	if len(stored.FileRefs) != 1 || stored.FileRefs[0].Path != "server.py" {
		t.Fatalf("fileRefs after delete=%#v", stored.FileRefs)
	}
	if err := store.DeleteMCPServer(server.ID); err != nil {
		t.Fatal(err)
	}
	if refs := store.MCPFileRefs(server.ID); len(refs) != 0 {
		t.Fatalf("orphan refs=%#v", refs)
	}
}
