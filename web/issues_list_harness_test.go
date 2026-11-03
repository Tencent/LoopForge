package web

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestIssueHeadingMenuKeepsLegacyControls(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	start := strings.Index(page, `<section id="issuesView"`)
	if start < 0 {
		t.Fatal("index.html is missing #issuesView")
	}
	actionsStart := strings.Index(page[start:], `class="heading-actions issue-heading-actions"`)
	if actionsStart < 0 {
		t.Fatal("#issuesView heading is missing .issue-heading-actions")
	}
	actions := page[start+actionsStart:]
	actions = actions[:strings.Index(actions, `<div class="project-context-bar">`)]

	menuStart := strings.Index(actions, `id="issueHeadingMenu"`)
	if menuStart < 0 {
		t.Fatal("heading actions are missing #issueHeadingMenu")
	}
	menu := actions[menuStart:]
	menu = menu[:strings.Index(menu, "</div>")]
	for _, want := range []string{`role="menu"`, `id="openSyncModal"`, `id="syncBadge"`, `id="newScheduleJob"`, `id="scheduleJobCount"`, `id="importGitHubIssues"`, `id="importTAPDIssue"`} {
		if !strings.Contains(menu, want) {
			t.Fatalf("#issueHeadingMenu is missing %s", want)
		}
	}
	if got := strings.Count(menu, `role="menuitem"`); got != 4 {
		t.Fatalf("#issueHeadingMenu should hold 4 menu items, got %d", got)
	}
	if !regexp.MustCompile(`id="importTAPDIssue"[^>]*data-legacy-tracker hidden`).MatchString(menu) {
		t.Fatal("the TAPD import item must stay hidden behind the legacy tracker flag")
	}

	outside := strings.Replace(actions, menu, "", 1)
	for _, want := range []string{`id="refreshIssues"`, `id="issueHeadingMore"`, `aria-haspopup="menu"`, `id="newIssue"`} {
		if !strings.Contains(outside, want) {
			t.Fatalf("heading actions are missing %s outside the menu", want)
		}
	}
	if got := strings.Count(outside, `class="button primary"`); got != 1 {
		t.Fatalf("only 新建需求 may be a solid primary button in the heading, got %d", got)
	}
	if !strings.Contains(outside, `id="newIssue" class="button primary"`) {
		t.Fatal("新建需求 must be the solid primary heading button")
	}
}

func TestIssuesListStylesheetLoadsLast(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`<link rel="stylesheet" href="([^"?]+)`).FindAllStringSubmatch(string(html), -1)
	// Page stylesheets that build on the shared --hui-* tokens, and the
	// .topbar-scoped sheet, may follow it.
	pageSheets := map[string]bool{"/app/assets-harness.css": true, "/app/sops-harness.css": true, "/app/sop-catalog.css": true, "/app/approvals-harness.css": true, "/app/observability-harness.css": true, "/app/topbar-harness.css": true, "/app/business-harness.css": true, "/app/requirement-wizard.css": true}
	for len(links) > 0 && pageSheets[links[len(links)-1][1]] {
		links = links[:len(links)-1]
	}
	if len(links) == 0 || links[len(links)-1][1] != "/app/issues-list.css" {
		t.Fatalf("/app/issues-list.css must be the last shared stylesheet, got %v", links[len(links)-1])
	}
	css, err := os.ReadFile("app/issues-list.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(css), "\n") {
		selector := strings.TrimSpace(line)
		if !strings.HasSuffix(selector, "{") || strings.HasPrefix(selector, "@") || strings.HasPrefix(selector, "from") || strings.HasPrefix(selector, "to") || strings.Contains(selector, "%") {
			continue
		}
		for _, part := range splitSelectorList(strings.TrimSuffix(selector, "{")) {
			if part == "" || part == "#assetsView" || part == "#approvalsView" || strings.Contains(part, "#issuesView") || strings.HasPrefix(part, ".hui-select-") || strings.HasPrefix(part, ".issue-action-popover") || strings.Contains(part, ":is(#issuesView") {
				continue
			}
			t.Fatalf("issues-list.css rule %q escapes the issues page scope", part)
		}
	}
}

// At 1280px the table scroll area is ~1137px wide; with the widest (120px) key
// column every row, including the ⋯ action, must fit without horizontal scrolling.
func TestIssueTableFitsLaptopWidth(t *testing.T) {
	css, err := os.ReadFile("app/issues-list.css")
	if err != nil {
		t.Fatal(err)
	}
	rules := regexp.MustCompile(`#issuesView \.issue-table(\.issue-table-selectable)? \{\s*--issue-cols: ([^;]+);\s*min-width: max\(100%, calc\(var\(--issue-col-key, 76px\) \+ (\d+)px\)\);`).FindAllStringSubmatch(string(css), -1)
	if len(rules) != 2 {
		t.Fatalf("expected .issue-table and .issue-table-selectable column rules, got %d", len(rules))
	}
	const maxKeyWidth, scrollWidthAt1280 = 120, 1120
	track := regexp.MustCompile(`minmax\((\d+)px,[^)]*\)|(\d+)px`)
	for _, rule := range rules {
		cols := strings.Replace(rule[2], "var(--issue-col-key, 76px)", "", 1)
		sum := 0
		for _, m := range track.FindAllStringSubmatch(cols, -1) {
			n, _ := strconv.Atoi(m[1] + m[2])
			sum += n
		}
		offset, _ := strconv.Atoi(rule[3])
		if sum > offset {
			t.Fatalf("issue table%s column minimums sum to %dpx but min-width only reserves %dpx", rule[1], sum, offset)
		}
		if maxKeyWidth+offset > scrollWidthAt1280 {
			t.Fatalf("issue table%s min-width key+%dpx overflows a 1280px viewport (limit %dpx)", rule[1], offset, scrollWidthAt1280-maxKeyWidth)
		}
	}
}

// splitSelectorList splits on top-level commas only, keeping :is()/:not() lists intact.
func splitSelectorList(list string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range list {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(list[start:i]))
				start = i + 1
			}
		}
	}
	return append(parts, strings.TrimSpace(list[start:]))
}

func TestHuiSelectLoadsBeforeIssues(t *testing.T) {
	names := appBundleFiles(t)
	issuesAt := slices.Index(names, "app/issues.js")
	// hui-select.js defines HuiPopover / HuiAuto, which the other controls use.
	for i, name := range []string{"app/hui-select.js", "app/hui-date.js", "app/hui-upload.js", "app/hui-tooltip.js"} {
		at := slices.Index(names, name)
		if at < 0 || issuesAt < 0 || at > issuesAt {
			t.Fatalf("%s must load before app/issues.js, got %v", name, names)
		}
		if i > 0 && at < slices.Index(names, "app/hui-select.js") {
			t.Fatalf("%s must load after app/hui-select.js, got %v", name, names)
		}
	}
	if names[len(names)-1] != "app/start.js" {
		t.Fatalf("app/start.js must stay last, got %v", names)
	}
}

func TestIssueStageBadgeAndRowMarkup(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required in CI")
		}
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "issues_list_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("issue stage badge: %v\n%s", err, out)
	}
}

func TestHuiSelectEnhancement(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required in CI")
		}
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "hui_select_test.js", "app/hui-select.js").CombinedOutput(); err != nil {
		t.Fatalf("hui select: %v\n%s", err, out)
	}
}
