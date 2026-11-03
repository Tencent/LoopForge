package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const harnessUICSS = "app/harness-ui.css"

func readHarnessUI(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(harnessUICSS)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// nativeWebFiles lists the web/ native page sources; chatbot/ and the built native-chat bundle follow their own spec.
func nativeWebFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch path {
			case "chatbot", "native-chat", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".css", ".js", ".html":
			files = append(files, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestHarnessTokensAreDeclaredOnlyInSharedStylesheet(t *testing.T) {
	declaration := regexp.MustCompile(`--hui-[a-z0-9-]+\s*:`)
	for _, path := range nativeWebFiles(t) {
		if path == harnessUICSS {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if m := declaration.Find(raw); m != nil {
			t.Errorf("%s declares %q; --hui-* tokens are declared only in %s", path, m, harnessUICSS)
		}
	}
	css := readHarnessUI(t)
	for _, block := range []string{":root {", `html[data-theme="dark"] {`} {
		start := strings.Index(css, block)
		if start < 0 {
			t.Fatalf("%s is missing the %q token block", harnessUICSS, block)
		}
		body := css[start : start+strings.Index(css[start:], "}")]
		for _, token := range []string{"--hui-accent:", "--hui-text:", "--hui-border:", "--hui-surface:", "--hui-success:", "--hui-error:", "--hui-panel-shadow:", "--hui-popover-shadow:"} {
			if !strings.Contains(body, token) {
				t.Errorf("%q block is missing %s", block, token)
			}
		}
	}
}

func TestStatusBadgesShareOneImplementation(t *testing.T) {
	legacy := regexp.MustCompile(`\b(?:bot-badge|kb-badge|run-badge|bot-lamp|run-lamp-ring|credential-lamp)\b`)
	for _, path := range nativeWebFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if m := legacy.Find(raw); m != nil {
			t.Errorf("%s still references the page badge %q; use .hui-badge[data-tone]", path, m)
		}
	}
	css := readHarnessUI(t)
	for _, want := range []string{".hui-badge {", `.hui-badge[data-tone="success"]`, `.hui-badge[data-tone="error"]`, `.hui-badge[data-size="sm"]`, ".hui-lamp", "@keyframes huiBreath"} {
		if !strings.Contains(css, want) {
			t.Errorf("%s is missing %q", harnessUICSS, want)
		}
	}
	for path, want := range map[string]string{
		"app/issues.js":    `class="hui-badge" data-tone=`,
		"app/runs.js":      `class="hui-badge" data-size="sm" data-tone=`,
		"app/bots.js":      `class="hui-badge" data-size="sm" data-tone=`,
		"knowledge.js":     `class="hui-badge" data-size="sm" data-tone=`,
		"app/workbench.js": `class="hui-badge credential-status" data-size="sm" data-tone=`,
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s must render %q", path, want)
		}
	}
}

func TestHarnessStylesheetLoadOrder(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	var links []string
	for _, m := range regexp.MustCompile(`<link rel="stylesheet" href="([^"?]+)`).FindAllStringSubmatch(string(html), -1) {
		links = append(links, m[1])
	}
	shared := slices.Index(links, "/"+harnessUICSS)
	if base := slices.Index(links, "/product-v4.css"); shared < 0 || base < 0 || shared < base {
		t.Fatalf("/%s must load after /product-v4.css, got %v", harnessUICSS, links)
	}
	for _, page := range []string{"/bots.css", "/knowledge.css", "/metrics-dashboard.css", "/deployment-dashboard.css", "/business-domains.css", "/app/mcp.css", "/app/issues-list.css", "/app/assets-harness.css", "/app/observability-harness.css", "/app/approvals-harness.css"} {
		if at := slices.Index(links, page); at < 0 || at < shared {
			t.Errorf("%s must load after /%s, got %v", page, harnessUICSS, links)
		}
	}
}

func TestSettingsAreaUsesOneCenteredContainer(t *testing.T) {
	css := readHarnessUI(t)
	container := regexp.MustCompile(`:is\(([^)]*)\)\.page-view\.active > :not\(\.agent-row-menu\) \{([^}]*)\}`).FindStringSubmatch(css)
	if container == nil {
		t.Fatalf("%s is missing the settings container rule", harnessUICSS)
	}
	views := container[1]
	for _, view := range []string{"#settingsView", "#botsView", "#projectsView", "#runnersView", "#agentsView", "#mcpView", "#assetsView", "#knowledgeView", "#metricsView", "#deploymentsView", "#businessDomainsView", "#guideView", "#platformView", "#businessDomainManagementView"} {
		if !strings.Contains(views, view) {
			t.Errorf("settings container must include %s", view)
		}
	}
	for _, view := range []string{"#flowView", "#issuesView", "#runsView", "#approvalsView", "#observabilityView"} {
		if strings.Contains(views, view) {
			t.Errorf("settings container must not include workspace view %s", view)
		}
	}
	for _, want := range []string{"width: 100%;", "max-width: 1200px !important;", "margin-left: auto !important;", "margin-right: auto !important;"} {
		if !strings.Contains(container[2], want) {
			t.Errorf("settings container must contain %q, got %q", want, container[2])
		}
	}

	privateCap := regexp.MustCompile(`[{;]\s*max-width:\s*(?:920|1160|1180|1280)px`)
	for _, path := range []string{"platform.css", "product-v4.css", "knowledge.css", "access.css", "app/assets-harness.css", "app/mcp.css", "bots.css"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if m := privateCap.Find(raw); m != nil {
			t.Errorf("%s keeps a private outer width cap %q; the settings container owns the width", path, m)
		}
	}
	v4, err := os.ReadFile("product-v4.css")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`\.personal-settings[\s,{][^}]*max-width:\s*none`).Match(v4) {
		t.Error("product-v4.css must not reset .personal-settings max-width")
	}
}

// JetBrains Mono is not bundled, so a stack led by it falls through to Courier New on macOS Chrome.
func TestMonoTokenUsesSystemMonospace(t *testing.T) {
	raw, err := os.ReadFile("styles.css")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`--mono:\s*([^;]+);`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("styles.css must declare --mono")
	}
	stack := string(m[1])
	if !strings.HasPrefix(stack, "ui-monospace") {
		t.Errorf("--mono must start with ui-monospace, got %q", stack)
	}
	for _, want := range []string{"Menlo", "Consolas"} {
		if !strings.Contains(stack, want) {
			t.Errorf("--mono must include %s, got %q", want, stack)
		}
	}
	for _, banned := range []string{"Courier New", "JetBrains Mono"} {
		if strings.Contains(stack, banned) {
			t.Errorf("--mono must not include %s, got %q", banned, stack)
		}
	}
}

func TestAccessCapabilityTableKeepsShortColumnsOnOneLine(t *testing.T) {
	raw, err := os.ReadFile("access.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	code := regexp.MustCompile(`\.access-table code\{[^}]*font:\s*\d+\s+(\d+(?:\.\d+)?)px var\(--mono\)`).FindStringSubmatch(css)
	if code == nil {
		t.Fatal("access.css must set .access-table code font with var(--mono)")
	}
	if size, _ := strconv.ParseFloat(code[1], 64); size < 12 {
		t.Errorf(".access-table code must be at least 12px, got %spx", code[1])
	}
	for _, col := range []string{"1", "2", "3", "5"} {
		if !regexp.MustCompile(`\.access-table td:nth-child\(` + col + `\)[^{]*\{[^}]*white-space:\s*nowrap`).MatchString(css) {
			t.Errorf("access.css must keep capability column %s on one line", col)
		}
	}
	if !regexp.MustCompile(`\.access-table th\{[^}]*white-space:\s*nowrap`).MatchString(css) {
		t.Error("access.css must keep capability table headers on one line")
	}
}

// Accent fills flip to a light purple in dark mode, so text on them must use --hui-on-accent instead of white.
func TestAccentFillsUseOnAccentText(t *testing.T) {
	rule := regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`)
	accentFill := regexp.MustCompile(`background(?:-color)?\s*:\s*var\(--(?:hui|pw|cr|hu|pa|rv)-accent\)`)
	whiteText := regexp.MustCompile(`(?:^|;)\s*color\s*:\s*(?:#fff\b|#ffffff\b|white\b)`)
	for _, path := range nativeWebFiles(t) {
		if filepath.Ext(path) != ".css" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range rule.FindAllStringSubmatch(string(raw), -1) {
			if accentFill.MatchString(m[2]) && whiteText.MatchString(m[2]) {
				t.Errorf("%s: %q puts white text on an accent fill; use color: var(--hui-on-accent)", path, strings.TrimSpace(m[1]))
			}
		}
	}
	onAccent := regexp.MustCompile(`(?:^|;)\s*color\s*:\s*var\(--hui-on-accent\)`)
	switched := map[string]bool{}
	for _, m := range rule.FindAllStringSubmatch(readHarnessUI(t), -1) {
		if !onAccent.MatchString(m[2]) {
			continue
		}
		for _, legacy := range []string{".member-avatar.owner", ".pager-page.active", ".access-recommended"} {
			if strings.Contains(m[1], legacy) {
				switched[legacy] = true
			}
		}
		// #agentModal draws the active preset icon on --hu-surface, so on-accent text would vanish there.
		if strings.Contains(m[1], ".as-preset") {
			t.Errorf("%s: %q must not set --hui-on-accent on preset icons", harnessUICSS, strings.TrimSpace(m[1]))
		}
	}
	for _, legacy := range []string{".member-avatar.owner", ".pager-page.active", ".access-recommended"} {
		if !switched[legacy] {
			t.Errorf("%s must switch %s (white on --blue, mapped to the accent) to color: var(--hui-on-accent)", harnessUICSS, legacy)
		}
	}
}
