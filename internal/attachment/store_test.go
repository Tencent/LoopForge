package attachment

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestAttachmentStoreUsesScopedCanonicalRecords(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	data := []byte{0, 255, 128, 10}
	a := domain.IssueAttachment{AssistantAttachment: domain.AssistantAttachment{ID: strings.Repeat("a", 32), Name: "requirement.pdf", Size: 4}, UploadedBy: 42}
	dir := OwnerDir(a.UploadedBy)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	record := Record{AssistantAttachment: a.AssistantAttachment, ProjectID: "p"}
	raw, _ := json.Marshal(record)
	os.WriteFile(filepath.Join(dir, a.ID+".json"), raw, 0600)
	os.WriteFile(filepath.Join(dir, a.ID+".bin"), data, 0600)
	got, err := Read(a, "p")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := Read(a, "other"); err == nil {
		t.Fatal("wrong project accepted")
	}
	altered := a
	altered.Name = "other.pdf"
	if _, err := Read(altered, "p"); err == nil {
		t.Fatal("name mismatch accepted")
	}
	altered = a
	altered.Size = 3
	if _, err := Read(altered, "p"); err == nil {
		t.Fatal("size mismatch accepted")
	}
	altered = a
	altered.UploadedBy = 43
	if _, err := Read(altered, "p"); err == nil {
		t.Fatal("wrong owner accepted")
	}
	for _, id := range []string{"../secret", strings.Repeat("g", 32), "", strings.Repeat("a", 31)} {
		if ValidID(id) {
			t.Fatal("invalid id accepted")
		}
		if _, f, err := Open(42, id); err == nil {
			f.Close()
			t.Fatal("invalid id opened")
		}
	}
	os.WriteFile(filepath.Join(dir, a.ID+".json"), []byte("invalid"), 0600)
	if _, _, err := Open(42, a.ID); err == nil {
		t.Fatal("bad metadata accepted")
	}
	os.WriteFile(filepath.Join(dir, a.ID+".json"), raw, 0600)
	os.Remove(filepath.Join(dir, a.ID+".bin"))
	outside := filepath.Join(t.TempDir(), "private")
	os.WriteFile(outside, data, 0600)
	os.Symlink(outside, filepath.Join(dir, a.ID+".bin"))
	if _, err := Read(a, "p"); err == nil {
		t.Fatal("out of root link accepted")
	}
}

func TestAttachmentWorkspacePathsKeepOriginalBaseName(t *testing.T) {
	id := strings.Repeat("b", 32)
	for name, want := range map[string]string{"../../需求.md": "需求.md", `..\..\image.png`: "image.png", "..": "attachment.bin", "": "attachment.bin", "/": "attachment.bin", "report.pdf": "report.pdf"} {
		got := WorkspacePath(domain.AssistantAttachment{ID: id, Name: name})
		if got != ".agentflow/inputs/requirement-attachments/"+id+"/"+want {
			t.Fatalf("%q -> %q", name, got)
		}
	}
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", " ")
	home, _ := os.UserHomeDir()
	if got := BaseDir(); got != filepath.Join(home, ".agentflow", "assistant") {
		t.Fatalf("base %q", got)
	}
}
