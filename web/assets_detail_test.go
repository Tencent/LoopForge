package web

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func assetsViewMarkup(t *testing.T) string {
	t.Helper()
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	start := strings.Index(page, `<section id="assetsView"`)
	if start < 0 {
		t.Fatal("index.html is missing #assetsView")
	}
	end := strings.Index(page[start:], "</section>")
	if end < 0 {
		t.Fatal("#assetsView is not closed")
	}
	return page[start : start+end]
}

func TestAssetsViewSplitsListAndDetail(t *testing.T) {
	view := assetsViewMarkup(t)
	for _, want := range []string{`id="assetListPane"`, `id="assetDetailPane"`, `id="assetSearch"`, `id="assetCount"`, `id="assetList"`, `id="assetCrumbs"`, `id="assetDetailHead"`, `<form id="assetForm"`, `data-asset-scope-count="project"`, `data-asset-scope-count="shared"`} {
		if !strings.Contains(view, want) {
			t.Fatalf("#assetsView is missing %s", want)
		}
	}
	if strings.Contains(view, "agent-layout") {
		t.Fatal("#assetsView must not use the old .agent-layout list + form split")
	}
	list := view[strings.Index(view, `id="assetListPane"`):strings.Index(view, `id="assetDetailPane"`)]
	if strings.Contains(list, "assetForm") {
		t.Fatal("the edit form belongs to the detail pane, not the card grid")
	}
	if !regexp.MustCompile(`id="assetDetailPane"[^>]*\bhidden\b`).MatchString(view) {
		t.Fatal("the detail pane starts hidden so the page opens on the card grid")
	}
}

func TestAssetsStylesheetFollowsHarnessTokens(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	tokens, assets := strings.Index(page, `href="/app/issues-list.css"`), strings.Index(page, `href="/app/assets-harness.css"`)
	if tokens < 0 || assets < 0 || assets < tokens {
		t.Fatalf("/app/assets-harness.css must load after /app/issues-list.css (got %d, %d)", tokens, assets)
	}
	if shared := strings.Index(page, `href="/app/harness-ui.css`); shared < 0 || shared > assets {
		t.Fatalf("/app/assets-harness.css must load after /app/harness-ui.css (got %d, %d)", shared, assets)
	}
	tokenCSS, err := os.ReadFile("app/harness-ui.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{":root {\n", `html[data-theme="dark"] {`, "--hui-stage: #f4f3f8;", "--hui-stage: #18181c;"} {
		if !strings.Contains(string(tokenCSS), want) {
			t.Fatalf("harness-ui.css token block is missing %q", want)
		}
	}
	css, err := os.ReadFile("app/assets-harness.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(css), "\n") {
		selector := strings.TrimSpace(line)
		if !strings.HasSuffix(selector, "{") || strings.HasPrefix(selector, "@") || strings.HasPrefix(selector, "from") || strings.HasPrefix(selector, "to") {
			continue
		}
		for _, part := range splitSelectorList(strings.TrimSuffix(selector, "{")) {
			if part != "" && !strings.Contains(part, "#assetsView") {
				t.Fatalf("assets-harness.css rule %q escapes #assetsView", part)
			}
		}
	}
	if !strings.Contains(string(tokenCSS), ".hui-menu.is-open") {
		t.Fatal("harness-ui.css is missing the shared \".hui-menu.is-open\" state")
	}
	for _, want := range []string{".asset-card-go", "@media (max-width: 1100px)", "@media (max-width: 820px)", "prefers-reduced-motion", "var(--hui-stage)"} {
		if !strings.Contains(string(css), want) {
			t.Fatalf("assets-harness.css is missing %q", want)
		}
	}
	// 1280px viewport leaves ~835px for #assetList; three columns must fit (3*256+2*14 <= 835).
	if !strings.Contains(string(css), "minmax(min(100%, 256px), 1fr)") {
		t.Fatal("assets-harness.css card grid must use minmax(min(100%, 256px), 1fr) so 1280px viewports show three columns")
	}
	v4, err := os.ReadFile("product-v4.css")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(v4), "#assetsView .agent-layout") {
		t.Fatal("product-v4.css still styles the removed #assetsView .agent-layout")
	}
}

func TestAssetsPanesFillWidthAndEmptyStateFillsHeight(t *testing.T) {
	raw, err := os.ReadFile("app/assets-harness.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	// #assetsView is a column flex container, so the panes shrink to content without an explicit width.
	pane := regexp.MustCompile(`#assetsView > \.asset-detail-pane \{([^}]*)\}`).FindStringSubmatch(css)
	if pane == nil {
		t.Fatal("assets-harness.css is missing the list/detail pane rule")
	}
	for _, want := range []string{"width: 100%;", "box-sizing: border-box;"} {
		if !strings.Contains(pane[1], want) {
			t.Fatalf("pane rule must contain %q, got %q", want, pane[1])
		}
	}
	if strings.Contains(pane[1], "max-width") || strings.Contains(pane[1], "margin") {
		t.Fatalf("pane width comes from the shared settings container, got %q", pane[1])
	}
	shared, err := os.ReadFile("app/harness-ui.css")
	if err != nil {
		t.Fatal(err)
	}
	container := regexp.MustCompile(`:is\(([^)]*)\)\.page-view\.active > :not\(\.agent-row-menu\) \{([^}]*)\}`).FindStringSubmatch(string(shared))
	if container == nil || !strings.Contains(container[1], "#assetsView") || !strings.Contains(container[2], "max-width: 1200px !important;") {
		t.Fatalf("assets panes must use the shared 1200px settings container, got %q", container)
	}
	for _, want := range []string{
		"#assetsView > .asset-list-pane:has(> #assetList > .asset-empty) { flex: 1 1 auto; display: flex; flex-direction: column; }",
		"#assetsView #assetList:has(> .asset-empty) { flex: 1 1 auto; display: flex; flex-direction: column; }",
		"#assetsView #assetList > .asset-empty { flex: 1 1 auto; justify-content: center; }",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("assets-harness.css is missing empty-state fill rule %q", want)
		}
	}
}

func TestAssetRoutesAreWired(t *testing.T) {
	app, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(app)
	for _, want := range []string{
		"function assetRouteId(",
		"function assetViewHash(",
		"function applyAssetRoute(",
		"if(isProjectSharedAsset(name))applyAssetRoute();",
		"if(isProjectSharedAsset(name)&&routePart(0)===name&&assetRouteId())",
		"if(staying&&state.assetType===name){if(state.selectedAsset)closeAssetDetail({force:true})",
		"$('#newAsset').onclick=()=>openAssetDetail('__new__')",
		"if(!(await confirmAssetLeave()))return;",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("app.js asset routing is missing %q", want)
		}
	}
	tabs, err := os.ReadFile("app/sliding-tabs.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tabs), "['.asset-pane-tabs', '.active']") {
		t.Fatal("sliding-tabs.js must register .asset-pane-tabs")
	}
	assets, err := os.ReadFile("app/assets.js")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`data-(share|delete)-asset`).Match(assets) {
		t.Fatal("share/delete buttons must not be rendered on asset cards")
	}
}

func TestAssetDetailBehaviour(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required in CI")
		}
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "assets_detail_test.js", "app/assets.js", "app.js").CombinedOutput(); err != nil {
		t.Fatalf("assets_detail_test.js failed: %v\n%s", err, out)
	}
}
