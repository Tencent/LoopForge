package web

import (
	"os"
	"regexp"
	"testing"
)

// lastCSSValue returns the property's value from the last top-level rule for
// selector that declares it, mirroring the cascade for same-specificity rules.
func lastCSSValue(css, selector, property string) string {
	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(selector) + `\s*\{([^}]*)\}`)
	value := ""
	for _, m := range pattern.FindAllStringSubmatch(css, -1) {
		if v := sidebarCSSValue(m[1], property); v != "" {
			value = v
		}
	}
	return value
}

func TestChatShellRailMatchesHostRailSurface(t *testing.T) {
	host, err := os.ReadFile("product-v4.css")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := os.ReadFile("../chatbot/src/navigation.css")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		theme        string
		hostSelector string
		chatSelector string
	}{
		{"light", ":root", ".chat-shell"},
		{"dark", `html[data-theme="dark"]`, `[data-theme="dark"] .chat-shell`},
	}
	for _, c := range cases {
		want := lastCSSValue(string(host), c.hostSelector, "--rail-surface")
		if want == "" {
			t.Fatalf("%s: %s missing --rail-surface", c.theme, c.hostSelector)
		}
		got := lastCSSValue(string(chat), c.chatSelector, "--navigation-rail")
		if got != want {
			t.Errorf("%s: %s { --navigation-rail } = %q, want host --rail-surface %q", c.theme, c.chatSelector, got, want)
		}
	}
}
