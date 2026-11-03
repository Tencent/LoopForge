package web

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestTopbarHarnessStylesheet(t *testing.T) {
	read := func(name string) string {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	html, css := read("index.html"), read("app/topbar-harness.css")

	var links []string
	for _, m := range regexp.MustCompile(`<link rel="stylesheet" href="([^"?]+)`).FindAllStringSubmatch(html, -1) {
		links = append(links, m[1])
	}
	at := slices.Index(links, "/app/topbar-harness.css")
	if at < 0 {
		t.Fatalf("index.html must load /app/topbar-harness.css, got %v", links)
	}
	// Only the #approvalsView-scoped sheet, which never touches .topbar, may follow it.
	if tail := links[at+1:]; len(tail) > 1 || (len(tail) == 1 && tail[0] != "/app/approvals-harness.css") {
		t.Fatalf("/app/topbar-harness.css must load after every stylesheet that styles the topbar, got %v", links)
	}

	body := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
	for _, m := range regexp.MustCompile(`([^{}]+)\{`).FindAllStringSubmatch(body, -1) {
		prelude := strings.TrimSpace(m[1])
		if strings.HasPrefix(prelude, "@") {
			continue
		}
		for _, part := range splitSelectorList(prelude) {
			if !strings.HasPrefix(part, ".topbar") && !regexp.MustCompile(`^html\[data-theme(="dark")?\] \.topbar\b`).MatchString(part) {
				t.Fatalf("topbar-harness.css rule %q escapes .topbar", part)
			}
		}
	}

	compact := regexp.MustCompile(`\s+`).ReplaceAllString(body, " ")
	for _, want := range []string{
		"background: color-mix(in srgb, var(--panel) 94%, transparent);",
		"backdrop-filter: blur(22px);",
		"height: 56px;",
		".topbar .command-trigger { height: 34px !important;",
		"border-radius: 9px !important;",
		".topbar .theme-toggle { box-sizing: border-box; width: 32px; height: 32px;",
		"border: 0; border-radius: 6px; background: transparent;",
		"--topbar-hover: color-mix(in srgb, var(--text) 5%, transparent);",
		".topbar .theme-toggle:is(:hover, :focus-visible) { border-color: transparent; background: var(--topbar-hover); color: var(--text); }",
		".topbar .theme-toggle b { display: none; }",
		".topbar :is(.button, .icon-button, .theme-toggle, .command-trigger, .ai-launch):hover { box-shadow: none !important; }",
		".topbar .hui-select-native { display: none !important; }",
		".topbar .hui-select-trigger[aria-expanded=\"true\"] .hui-select-chevron { transform: rotate(180deg); }",
		".topbar .hui-select-trigger.is-filtered:not([aria-expanded=\"true\"]) { border-color: var(--line); background: var(--bg-soft); color: var(--text); }",
		"@media (max-width: 1000px)",
		"@media (max-width: 760px)",
		"@media (prefers-reduced-motion: reduce)",
	} {
		if !strings.Contains(compact, want) {
			t.Fatalf("topbar-harness.css is missing %q", want)
		}
	}
	// No theme-switch animation was requested for the theme button.
	if strings.Contains(body, "view-transition") || regexp.MustCompile(`\.theme-toggle[^{]*\{[^}]*(?:animation|transform):\s*(?:[^n\s;]|n[^o])`).MatchString(body) {
		t.Fatal("topbar-harness.css must not animate the theme switch")
	}
	// Visibility at each width stays with collaboration.css / product-v4.css.
	for _, m := range regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(body, -1) {
		if regexp.MustCompile(`display:\s*(?:flex|inline-flex|grid|block)[^;]*!important`).MatchString(m[2]) {
			t.Fatalf("topbar-harness.css rule %q forces a visible display and would override the responsive hide rules", strings.TrimSpace(m[1]))
		}
	}
}

func TestTopbarProjectSwitcherUsesHarnessSelector(t *testing.T) {
	read := func(name string) string {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	html, projects := read("index.html"), read("app/projects.js")
	if !regexp.MustCompile(`<select id="projectSwitcher"[^>]*\baria-label="切换项目"`).MatchString(html) {
		t.Fatal(`#projectSwitcher must carry aria-label="切换项目" so the Harness trigger is named`)
	}
	render := regexp.MustCompile(`(?s)function renderProjectSwitcher\(\)\{.*?\n\}`).FindString(projects)
	if render == "" {
		t.Fatal("renderProjectSwitcher not found")
	}
	fill, enhance := strings.Index(render, "switcher.innerHTML="), strings.Index(render, "window.enhanceSelect?.(switcher)")
	if fill < 0 || enhance < fill {
		t.Fatal("renderProjectSwitcher must call window.enhanceSelect?.(switcher) after writing the options")
	}
}
