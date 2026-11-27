package web

import (
	"os"
	"strings"
	"testing"
)

// 编辑需求弹窗内容很长时，底部操作区必须固定可见，避免只露出按钮上沿。
func TestIssueModalActionsStaySticky(t *testing.T) {
	css, err := os.ReadFile("platform.css")
	if err != nil {
		t.Fatal(err)
	}
	const selector = ".issue-modal-card>.modal-actions{"
	start := strings.Index(string(css), selector)
	if start < 0 {
		t.Fatalf("platform.css is missing %q", selector)
	}
	rule := string(css[start:])
	rule = rule[:strings.Index(rule, "}")]
	for _, want := range []string{"position:sticky", "bottom:", "background:"} {
		if !strings.Contains(rule, want) {
			t.Fatalf("issue modal footer should stay pinned, missing %q in %s", want, rule)
		}
	}
}
