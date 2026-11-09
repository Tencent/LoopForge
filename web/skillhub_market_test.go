package web

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestSkillhubMarketMarkup(t *testing.T) {
	view := assetsViewMarkup(t)
	if !regexp.MustCompile(`<button type="button" data-asset-scope="skillhub" hidden>SkillHub 市场</button>`).MatchString(view) {
		t.Fatal("#assetScopeSwitch needs a SkillHub tab that starts hidden until the skills page shows it")
	}
	for _, want := range []string{`id="skillhubMarket" class="skillhub-market" hidden`, `id="skillhubKeyword"`, `<select id="skillhubCategory" data-icon="layers"`, `<select id="skillhubSort" data-icon="sort-descending"`, `id="skillhubGrid" class="asset-grid"`, `id="skillhubPager"`, `id="skillhubDrawer"`, `id="skillhubModal"`, `id="assetSearchBox"`} {
		if !strings.Contains(view, want) {
			t.Fatalf("#assetsView is missing %s", want)
		}
	}
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	assets, market := strings.Index(page, `<script src="/app/assets.js"></script>`), strings.Index(page, `<script src="/app/skillhub.js"></script>`)
	if assets < 0 || market < assets {
		t.Fatalf("/app/skillhub.js must load after /app/assets.js (got %d, %d)", assets, market)
	}
	src, err := os.ReadFile("app/skillhub.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"SKILLHUB_API_KEY", "X-API-Key", "api.skillhub.cn", "myqcloud"} {
		if strings.Contains(string(src), leak) {
			t.Fatalf("app/skillhub.js must only call the platform proxy, found %q", leak)
		}
	}
}

func TestSkillhubMarketBehaviour(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required in CI")
		}
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "skillhub_market_test.js", "app/skillhub.js", "app/assets.js").CombinedOutput(); err != nil {
		t.Fatalf("skillhub_market_test.js failed: %v\n%s", err, out)
	}
}
