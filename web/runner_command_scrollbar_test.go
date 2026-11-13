package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The install / uninstall command boxes on the runners page used to hide their
// horizontal scrollbar and switch it on with :hover. With classic (always
// visible) scrollbars that made the box grow under the pointer and the page
// jump (#193). Hovering may only recolour the scrollbar, never resize it.
func TestRunnerCommandScrollbarDoesNotResizeOnHover(t *testing.T) {
	data, err := os.ReadFile("product-v4.css")
	if err != nil {
		t.Fatal(err)
	}
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(data), "")

	rule := regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`)
	sizing := regexp.MustCompile(`(?:^|;)\s*(?:scrollbar-width|height|width|display|padding[a-z-]*)\s*:`)
	var webkitTrack, webkitHoverThumb string
	for _, m := range rule.FindAllStringSubmatch(css, -1) {
		selector, body := strings.TrimSpace(m[1]), m[2]
		if i := strings.LastIndex(selector, "{"); i >= 0 {
			selector = strings.TrimSpace(selector[i+1:])
		}
		if !strings.Contains(selector, ".runner-command-box") {
			continue
		}
		if strings.Contains(selector, ":hover") && strings.Contains(selector, "pre") && sizing.MatchString(body) {
			t.Errorf("%s { %s } changes the command scrollbar size on hover", selector, strings.TrimSpace(body))
		}
		if strings.Contains(selector, "pre") && regexp.MustCompile(`scrollbar-width\s*:\s*none`).MatchString(body) {
			t.Errorf("%s hides the command scrollbar, so it can only appear by resizing the box", selector)
		}
		switch selector {
		case "#runnersView .runner-command-box pre::-webkit-scrollbar":
			webkitTrack = body
		case "#runnersView .runner-command-box:hover pre::-webkit-scrollbar-thumb":
			webkitHoverThumb = body
		}
	}
	if !regexp.MustCompile(`height\s*:\s*\d+px`).MatchString(webkitTrack) || strings.Contains(webkitTrack, "display") {
		t.Errorf("WebKit command scrollbar must reserve a fixed height, got %q", webkitTrack)
	}
	if !strings.Contains(webkitHoverThumb, "background") {
		t.Errorf("hovering a command box should only reveal the WebKit thumb colour, got %q", webkitHoverThumb)
	}

	fallback := supportsBlock(t, css, "@supports not selector(::-webkit-scrollbar)")
	if !regexp.MustCompile(`#runnersView \.runner-command-box pre\s*\{[^}]*scrollbar-width:\s*thin`).MatchString(fallback) {
		t.Error("non-WebKit fallback must reserve scrollbar-width: thin on the command box without :hover")
	}
	if !regexp.MustCompile(`#runnersView \.runner-command-box:hover pre\s*\{[^}]*scrollbar-color:`).MatchString(fallback) {
		t.Error("non-WebKit fallback should reveal the command scrollbar by colour on hover")
	}
}
