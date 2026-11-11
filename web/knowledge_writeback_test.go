package web

import (
	"os/exec"
	"strings"
	"testing"
)

// The review drawer renders the writeback branch and merge request. Keep the
// rendering (and URL scheme filtering) covered by the same node harness the
// rest of the knowledge center uses.
func TestKnowledgeWritebackReviewRendering(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "knowledge_writeback_test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("knowledge_writeback_test.js failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "knowledge writeback ok") {
		t.Fatalf("unexpected output: %s", out)
	}
}
