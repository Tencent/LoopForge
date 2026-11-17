package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClarificationCodeToolsAreReadOnlyAndRooted(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	s := &AgentToolSession{Mode: AgentToolClarify, ClarifyWorkspace: root}
	if got, err := callClarifyFiles(s, "clarify_read_file", map[string]any{"path": "source.go"}); err != nil || got != "package sample\n" {
		t.Fatalf("read=%q err=%v", got, err)
	}
	if got, err := callClarifyFiles(s, "clarify_list_files", nil); err != nil || !strings.Contains(got, "source.go") {
		t.Fatalf("list=%q err=%v", got, err)
	}
	for _, path := range []string{"../outside.txt", outside, "escape"} {
		if _, err := callClarifyFiles(s, "clarify_read_file", map[string]any{"path": path}); err == nil {
			t.Fatalf("escaped root: %s", path)
		}
	}
	if _, err := callClarifyFiles(s, "clarify_write_file", map[string]any{"path": "source.go"}); err == nil {
		t.Fatal("write tool accepted")
	}
	s.Mode = AgentToolAssistant
	if len(clarifyFileTools(s)) != 0 {
		t.Fatal("clarification files exposed to another mode")
	}
}
