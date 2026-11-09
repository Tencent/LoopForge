package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func supportsBlock(t *testing.T, css, query string) string {
	t.Helper()
	start := strings.Index(css, query)
	if start < 0 {
		t.Fatalf("missing %q block", query)
	}
	depth := 0
	for i := start + strings.Index(css[start:], "{"); i < len(css); i++ {
		switch css[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return css[start : i+1]
			}
		}
	}
	t.Fatalf("unterminated %q block", query)
	return ""
}

// Chrome 121+ ignores ::-webkit-scrollbar once scrollbar-width/color is not auto,
// which turns the 4px sidebar scrollbar into the ~10px thin one.
func TestPlatformSidebarScrollbarKeepsWebkitWidth(t *testing.T) {
	data, err := os.ReadFile("app/navigation.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(data)

	webkit := supportsBlock(t, css, "@supports selector(::-webkit-scrollbar)")
	reset := sidebarCSSBlock(t, strings.TrimSpace(webkit[strings.Index(webkit, "{")+1:]), ".platform-sidebar nav")
	for _, property := range []string{"scrollbar-width", "scrollbar-color"} {
		if got := sidebarCSSValue(reset, property); got != "auto" {
			t.Errorf("WebKit .platform-sidebar nav { %s } = %q, want auto", property, got)
		}
	}

	fallback := supportsBlock(t, css, "@supports not selector(::-webkit-scrollbar)")
	if !regexp.MustCompile(`\.platform-sidebar nav\s*\{[^}]*scrollbar-width:\s*thin`).MatchString(fallback) {
		t.Error("non-WebKit fallback must keep scrollbar-width: thin on .platform-sidebar nav")
	}

	outside := strings.Replace(strings.Replace(css, webkit, "", 1), fallback, "", 1)
	for _, m := range regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(outside, -1) {
		if strings.Contains(m[1], ".platform-sidebar") && strings.Contains(m[1], "nav") &&
			regexp.MustCompile(`scrollbar-(?:width|color)\s*:`).MatchString(m[2]) {
			t.Errorf("%s sets standard scrollbar properties outside @supports", strings.TrimSpace(m[1]))
		}
	}

	width := sidebarCSSBlock(t, css, ".platform-sidebar nav::-webkit-scrollbar")
	if got := sidebarCSSValue(width, "width"); got != "4px" {
		t.Errorf(".platform-sidebar nav::-webkit-scrollbar { width } = %q, want 4px", got)
	}
}
