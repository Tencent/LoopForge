package web

import (
	"os"
	"strings"
	"testing"
)

func cssRuleBody(t *testing.T, css, selector string) string {
	t.Helper()
	for _, line := range strings.Split(css, "\n") {
		if strings.HasPrefix(line, selector+"{") {
			return strings.TrimSuffix(strings.TrimPrefix(line, selector+"{"), "}")
		}
	}
	t.Fatalf("product-v4.css missing rule %q", selector)
	return ""
}

func TestRunsRowSpacing(t *testing.T) {
	data, err := os.ReadFile("product-v4.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(data)
	for selector, wants := range map[string][]string{
		"#runsView .run-row": {
			"grid-template-columns:28px minmax(0,1fr) 104px 148px 64px 64px;",
			"gap:16px;",
			"min-height:72px;",
			"padding:14px 16px;",
			"box-shadow:inset 0 -1px 0 var(--rl-line-soft);",
		},
		"#runsView .run-row-meta":  {"margin-top:6px;", "gap:6px;", "font-size:12px;"},
		"#runsView .run-row-title": {"font-size:14px;", "text-overflow:ellipsis;", "white-space:nowrap"},
	} {
		body := cssRuleBody(t, css, selector)
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", selector, want)
			}
		}
	}
	// A real bottom border adds 1px under border-box and pushes the row past 72px.
	if body := cssRuleBody(t, css, "#runsView .run-row"); strings.Contains(body, "border-bottom:") {
		t.Error("#runsView .run-row must draw its divider with an inset box-shadow, not border-bottom")
	}
	if !strings.Contains(css, "#runsView .run-row{grid-template-columns:28px minmax(0,1fr) auto}") {
		t.Error("narrow #runsView .run-row column rule changed")
	}
}
