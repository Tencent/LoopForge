package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/attachment"
	"agentflow/internal/domain"
)

func TestRequirementAttachmentPreservesBinaryAndRejectsLinks(t *testing.T) {
	data := []byte{0, 255, 128, 10, 1, 0}
	a := domain.AssistantAttachment{ID: strings.Repeat("b", 32), Name: "../原始图片.png", Size: int64(len(data))}
	root := t.TempDir()
	if err := WriteRequirementAttachment(root, a, data); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, filepath.FromSlash(attachment.WorkspacePath(a)))
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("binary changed: %v %v", got, err)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0444 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	if err := WriteRequirementAttachment(root, a, data); err != nil {
		t.Fatal("retry failed:", err)
	}
	os.Remove(target)
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("private"), 0600)
	os.Symlink(outside, target)
	if err := WriteRequirementAttachment(root, a, data); err == nil {
		t.Fatal("symlink accepted")
	}
	got, _ = os.ReadFile(outside)
	if string(got) != "private" {
		t.Fatal("outside modified")
	}
	if err := WriteRequirementAttachment(t.TempDir(), domain.AssistantAttachment{ID: "../escape", Name: "x", Size: 1}, []byte("x")); err == nil {
		t.Fatal("unsafe id accepted")
	}
}
