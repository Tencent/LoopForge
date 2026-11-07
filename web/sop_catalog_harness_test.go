package web

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestSOPCatalogCardsAndMotion(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required in CI")
		}
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "sop_catalog_test.js", "app/sops.js", "app/dashboards.js").CombinedOutput(); err != nil {
		t.Fatalf("sop_catalog_test.js failed: %v\n%s", err, out)
	}
}

func TestSOPCatalogStylesheetFollowsHarnessTokens(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	tokens, assets, sop := strings.Index(page, `href="/app/issues-list.css"`), strings.Index(page, `href="/app/assets-harness.css"`), strings.Index(page, `href="/app/sop-catalog.css"`)
	if tokens < 0 || assets < 0 || sop < 0 || sop < tokens || sop < assets {
		t.Fatalf("/app/sop-catalog.css must load after issues-list.css and assets-harness.css (got %d, %d, %d)", tokens, assets, sop)
	}

	if !strings.Contains(readHarnessUI(t), "html[data-theme=\"dark\"] {") {
		t.Fatalf("%s must declare the --hui-* tokens #sopsView inherits", harnessUICSS)
	}

	raw, err := os.ReadFile("app/sop-catalog.css")
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
			if part != "" && !strings.Contains(part, "#sopsView") && !strings.HasPrefix(part, ".sop-action-popover") {
				t.Fatalf("sop-catalog.css rule %q escapes #sopsView", part)
			}
		}
	}

	hover := regexp.MustCompile(`(?s)#sopsView \.sop-card:hover \{([^}]*)\}`).FindStringSubmatch(css)
	if hover == nil {
		t.Fatal("sop-catalog.css is missing the #sopsView .sop-card:hover rule")
	}
	if strings.Contains(hover[1], "translate") || !strings.Contains(hover[1], "transform: none;") || !strings.Contains(hover[1], "box-shadow:") {
		t.Fatalf("card hover must only deepen the shadow and cancel the v3/v4 lift, got %q", hover[1])
	}

	reduced := regexp.MustCompile(`(?s)@media \(prefers-reduced-motion: reduce\) \{(.*)\}\s*$`).FindStringSubmatch(css)
	if reduced == nil {
		t.Fatal("sop-catalog.css must end with a prefers-reduced-motion block")
	}
	for _, want := range []string{"animation: none !important;", "transition: none !important;", ".sop-action-popover"} {
		if !strings.Contains(reduced[1], want) {
			t.Fatalf("reduced-motion block is missing %q", want)
		}
	}
	for _, want := range []string{"animation-delay: calc(min(var(--card-index, 0), 10) * 30ms);", "@keyframes sopCardIn", "filter: blur(3px)", ".sop-card.is-leaving", "@media (max-width: 1050px)", "@media (max-width: 720px)"} {
		if !strings.Contains(css, want) {
			t.Fatalf("sop-catalog.css is missing %q", want)
		}
	}
}

func TestSOPCatalogWiring(t *testing.T) {
	ink, err := os.ReadFile("app/hover-ink.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ink), "#sopsView :is(button, a)[data-hover-ink-control]") {
		t.Fatal("hover-ink.js must cover #sopsView buttons with data-hover-ink-control")
	}
	app, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), "if(name==='sops'){if(!staying&&state.data){sopCatalogMotion.reset();renderSOPCatalog()}refreshWorkflowMetrics()}") {
		t.Fatal("entering #sopsView must reset the card reveal before the metrics refresh")
	}
	if !strings.Contains(string(app), "if(name!=='sops'&&typeof hideSOPCardMenu==='function')hideSOPCardMenu();") {
		t.Fatal("leaving #sopsView must close the SOP card ⋯ menu")
	}
	sops, err := os.ReadFile("app/sops.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(sops)
	for _, want := range []string{"menu.className='issue-action-popover sop-action-popover'", "trigger.focus({preventScroll:true})", "setAttribute('aria-expanded','true')", "hideIssueActionMenu()", "AgentFlowHarnessIcons?.hydrate?.(sopCatalog)"} {
		if !strings.Contains(src, want) {
			t.Fatalf("app/sops.js is missing %q", want)
		}
	}
	if strings.Contains(src, "scope-share") {
		t.Fatal("SOP cards no longer render the legacy .scope-share buttons")
	}
}
