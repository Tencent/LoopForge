package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestSOPsHarnessStylesheetIsScopedAndLoadsLast(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	tokens, sops := strings.Index(page, `href="/app/issues-list.css"`), strings.Index(page, `href="/app/sops-harness.css"`)
	if tokens < 0 || sops < 0 || sops < tokens {
		t.Fatalf("/app/sops-harness.css must load after /app/issues-list.css (got %d, %d)", tokens, sops)
	}
	if !strings.Contains(readHarnessUI(t), "html[data-theme=\"dark\"] {") {
		t.Fatalf("%s must declare the --hui-* tokens #sopsView inherits", harnessUICSS)
	}

	raw, err := os.ReadFile("app/sops-harness.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	for _, line := range strings.Split(css, "\n") {
		selector := strings.TrimSpace(line)
		if !strings.HasSuffix(selector, "{") || strings.HasPrefix(selector, "@") || strings.HasPrefix(selector, "from") || strings.HasPrefix(selector, "to") || strings.Contains(selector, "%") {
			continue
		}
		for _, part := range splitSelectorList(strings.TrimSuffix(selector, "{")) {
			if !strings.HasPrefix(part, "#sopsView") {
				t.Fatalf("sops-harness.css rule %q must start with #sopsView", part)
			}
		}
	}
	if regexp.MustCompile(`(?m)\bfont:[^;}]*\binherit\b`).MatchString(css) {
		t.Fatal("sops-harness.css must not use inherit inside the font shorthand; the whole declaration would be dropped")
	}
	for _, want := range []string{
		"#sopsView .overview-metric::before { display: none; }",
		"#sopsView .overview-metric.is-bad b { color: var(--hui-error); }",
		"#sopsView .sop-kpis .is-bad b { color: var(--hui-error); }",
		"#sopsView .button.primary { background: var(--hui-accent);",
		"#sopsView .page-heading > div:first-child { flex: 1 1 auto; min-width: 0;",
		"@media (max-width: 720px)",
		"#sopsView .page-heading { flex-direction: column;",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("sops-harness.css is missing %q", want)
		}
	}
	for _, rule := range []string{`#sopsView \.button \{`, `#sopsView \.page-heading > \.heading-actions \{`, `#sopsView \.scope-share\.danger \{`} {
		body := regexp.MustCompile(rule + `([^}]*)\}`).FindStringSubmatch(css)
		if body == nil {
			t.Fatalf("sops-harness.css is missing rule %s", rule)
		}
		if !strings.Contains(body[1], "flex: none;") {
			t.Fatalf("rule %s must not shrink (flex: none), got %q", rule, body[1])
		}
		if rule != `#sopsView \.page-heading > \.heading-actions \{` && !strings.Contains(body[1], "white-space: nowrap;") {
			t.Fatalf("rule %s must keep labels on one line (white-space: nowrap), got %q", rule, body[1])
		}
	}
	danger := regexp.MustCompile(`#sopsView \.scope-share\.danger:hover,[^{]*\{([^}]*)\}`).FindStringSubmatch(css)
	if danger == nil || !strings.Contains(danger[1], "color: var(--hui-error);") {
		t.Fatal("删除 must only turn red on hover")
	}
}

func TestSOPCatalogMarksFailures(t *testing.T) {
	src, err := os.ReadFile("app/sops.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<div class="overview-metric danger${failed>0?' is-bad':''}"><span>失败运行</span>`,
		`<span${m.failedRuns>0?' class="is-bad"':''}><b>${m.failedRuns||0}</b>失败</span>`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("renderSOPCatalog is missing failure marker %q", want)
		}
	}
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	section := string(html)
	section = section[strings.Index(section, `<section id="sopsView"`):]
	section = section[:strings.Index(section, "</section>")]
	if got := strings.Count(section, `class="button primary"`); got != 1 || !strings.Contains(section, `id="newSOP" class="button primary"`) {
		t.Fatalf("新建 SOP must be the only solid primary button on the SOP page, got %d", got)
	}
}
