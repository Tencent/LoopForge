package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

const echartsSHA256 = "b66b25aeb4df84e33199dc21694014d336d222cbd9deb0e5a7c14bd6aa0d0fd0"

func TestObsChartsLayer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "obs_charts_test.js").CombinedOutput(); err != nil {
		t.Fatalf("obs charts: %v\n%s", err, out)
	}
}

func TestEChartsVendoredAndEmbedded(t *testing.T) {
	lib, err := fs.ReadFile(Files, "vendor/echarts/echarts.min.js")
	if err != nil {
		t.Fatalf("echarts not embedded: %v", err)
	}
	sum := sha256.Sum256(lib)
	if got := hex.EncodeToString(sum[:]); got != echartsSHA256 {
		t.Fatalf("echarts.min.js sha256 = %s, want %s (update README and this test together when upgrading)", got, echartsSHA256)
	}
	for _, name := range []string{"vendor/echarts/LICENSE", "vendor/echarts/NOTICE"} {
		if _, err := fs.Stat(Files, name); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	charts, err := fs.ReadFile(Files, "app/obs-charts.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(charts), "OBS_ECHARTS_VERSION='6.1.0'") || !strings.Contains(string(charts), "'/vendor/echarts/echarts.min.js?v='") {
		t.Fatal("obs-charts.js must load the vendored ECharts 6.1.0 from the same origin")
	}
}

func TestStatsPageLoadsEChartsLazily(t *testing.T) {
	page, err := fs.ReadFile(Files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if strings.Contains(strings.ToLower(html), "echarts") {
		t.Fatal("index.html must not reference echarts; obsEnsureECharts loads it on demand")
	}
	if m := regexp.MustCompile(`<script[^>]+src="(https?:)?//`).FindString(html); m != "" {
		t.Fatalf("index.html must not load external scripts: %s", m)
	}
	charts := strings.Index(html, `<script src="/app/obs-charts.js"></script>`)
	obs := strings.Index(html, `<script src="/app/observability.js"></script>`)
	if charts < 0 || obs < 0 || charts > obs {
		t.Fatal("obs-charts.js must be loaded before observability.js")
	}
	if !strings.Contains(html, `id="obsExpand"`) {
		t.Fatal("stats page needs the expand dialog container")
	}
}
