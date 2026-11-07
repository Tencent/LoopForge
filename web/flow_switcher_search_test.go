package web

import (
	"os"
	"strings"
	"testing"
)

// .fd-pop is a flex column, so the shared .fd-search flex:1 collapses the
// switcher search field below its 32px height unless the popover resets it.
func TestFlowSwitcherSearchKeepsHeight(t *testing.T) {
	data, err := os.ReadFile("app/flow-canvas.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(data)

	const selector = "#flowView .fd-pop .fd-search{"
	start := strings.Index(css, selector)
	if start < 0 {
		t.Fatalf("missing %q rule", selector)
	}
	end := strings.Index(css[start:], "}")
	if end < 0 {
		t.Fatalf("unterminated %q rule", selector)
	}
	body := strings.ReplaceAll(css[start+len(selector):start+end], " ", "")
	if !strings.Contains(body, "flex:none") {
		t.Errorf("%s} must set flex:none, got %q", selector, body)
	}
}
