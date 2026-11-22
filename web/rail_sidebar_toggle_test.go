package web

import (
	"os"
	"strings"
	"testing"
)

func TestIconRailHasNoExpandToggle(t *testing.T) {
	htmlRaw, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlRaw)
	if strings.Contains(html, `id="railSidebarToggle"`) || strings.Contains(html, `data-tooltip="展开导航"`) || strings.Contains(html, `aria-label="展开导航"`) {
		t.Fatal("icon rail must not offer an expand control")
	}
	bottom := strings.Index(html, `class="rail-bottom"`)
	settings := strings.Index(html, `id="railSettingsEntry"`)
	if bottom < 0 || settings < bottom {
		t.Fatal("settings entry must stay in the icon rail")
	}
	for _, moved := range []string{"流程模板", "运行记录", "定时任务"} {
		if strings.Contains(html, `<span class="rail-label">`+moved+`</span>`) {
			t.Fatalf("%s moved into the settings sidebar and must not stay on the rail", moved)
		}
	}

	app, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(app)
	if strings.Contains(src, "railSidebarToggle") || strings.Contains(src, "setPlatformRailExpanded(") || strings.Contains(src, "platform-rail-expanded") || strings.Contains(src, "只显示图标") {
		t.Fatal("icon rail expand toggle must be removed")
	}

	css, err := os.ReadFile("product-v4.css")
	if err != nil {
		t.Fatal(err)
	}
	sheet := string(css)
	if strings.Contains(sheet, "platform-rail-expanded") || strings.Contains(sheet, "--rail-w: 240px") {
		t.Fatal("icon rail must stay at the icon width")
	}
	if !strings.Contains(sheet, "--rail-w: 56px") || !strings.Contains(sheet, ".platform-rail .rail-label") {
		t.Fatal("icon rail labels stay hidden at the default icon width")
	}
}
