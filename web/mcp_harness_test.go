package web

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestMCPStylesheetIsScopedToMCPView(t *testing.T) {
	css, err := os.ReadFile("app/mcp.css")
	if err != nil {
		t.Fatal(err)
	}
	text := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(css), "")
	last := 0
	for i, r := range text {
		switch r {
		case '}', ';':
			last = i + 1
		case '{':
			prelude := strings.TrimSpace(text[last:i])
			last = i + 1
			if strings.HasPrefix(prelude, "@") {
				continue
			}
			for _, part := range splitSelectorList(prelude) {
				if !strings.Contains(part, "#mcpView") {
					t.Fatalf("mcp.css rule %q escapes the #mcpView scope", part)
				}
			}
		}
	}
	if regexp.MustCompile(`--hui-[a-z-]+:`).MatchString(text) {
		t.Fatal("mcp.css must read the shared --hui-* tokens instead of declaring them")
	}
	shared, err := os.ReadFile("app/harness-ui.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shared), `html[data-theme="dark"] {`) {
		t.Fatal("harness-ui.css must define dark theme tokens used by #mcpView")
	}
}

func TestMCPStylesheetLoadsBeforeIssuesList(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	var links []string
	for _, m := range regexp.MustCompile(`<link rel="stylesheet" href="([^"?]+)`).FindAllStringSubmatch(string(html), -1) {
		links = append(links, m[1])
	}
	mcpAt, issuesAt := slices.Index(links, "/app/mcp.css"), slices.Index(links, "/app/issues-list.css")
	if mcpAt < 0 || mcpAt > issuesAt {
		t.Fatalf("/app/mcp.css must load before /app/issues-list.css, got %v", links)
	}
}

func TestMCPViewUsesScopeSelect(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	section := regexp.MustCompile(`(?s)<section id="mcpView".*?</section>`).FindString(string(html))
	if section == "" {
		t.Fatal("#mcpView section not found")
	}
	for _, want := range []string{`<select id="mcpScopeSelect"`, `aria-label="范围"`, `<option value="project"`, `<option value="personal"`, `id="mcpInstalledCount"`, `data-asset-type-tab="mcp"`, `data-asset-type-tab="rules"`, `data-asset-type-tab="skills"`} {
		if !strings.Contains(section, want) {
			t.Fatalf("#mcpView is missing %q", want)
		}
	}
	for _, unwanted := range []string{"mcp-scope-switch", "data-mcp-scope", `class="mcp-toolbar panel"`, "data-mcp-tab", "mcpMarketCount"} {
		if strings.Contains(section, unwanted) {
			t.Fatalf("#mcpView still contains %q", unwanted)
		}
	}
	if !strings.Contains(string(html), `id="assetScopeSwitch" class="mcp-scope-switch`) {
		t.Fatal("environment assets page must keep its .mcp-scope-switch")
	}
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"window.enhanceSelect?.(mcpScopeSelect)", "scopeSelect.value=state.mcpScope", "mcpInstalledCount"} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("app bundle is missing %q", want)
		}
	}
}
