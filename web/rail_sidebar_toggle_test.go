package web

import (
	"os"
	"strings"
	"testing"
)

func TestRailSidebarToggleSitsAboveSettings(t *testing.T) {
	htmlRaw, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlRaw)
	bottom := strings.Index(html, `class="rail-bottom"`)
	toggle := strings.Index(html, `id="railSidebarToggle"`)
	settings := strings.Index(html, `id="railSettingsEntry"`)
	if bottom < 0 || toggle < bottom || settings < toggle {
		t.Fatal("sidebar toggle must sit in the rail, directly above settings")
	}
	button := html[strings.LastIndex(html[:toggle], "<button") : strings.Index(html[toggle:], "</button>")+toggle]
	if strings.Contains(button, "data-view=") {
		t.Fatal("sidebar toggle must not navigate")
	}
	if !strings.Contains(button, `data-icon="panel-left"`) || !strings.Contains(button, `aria-controls="platformRail"`) {
		t.Fatal("sidebar toggle must use the panel-left icon and control the icon rail")
	}
	if !strings.Contains(button, `aria-expanded="false"`) || !strings.Contains(button, `data-tooltip="展开导航"`) {
		t.Fatal("icon rail starts as icons and offers to expand")
	}
	for _, label := range []string{"AI 工作台", "统计", "需求列表", "待我确认", "设置"} {
		if !strings.Contains(html, `<span class="rail-label">`+label+`</span>`) {
			t.Fatalf("expanded rail missing label %s", label)
		}
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
	start := strings.Index(src, "$('#railSidebarToggle')?.addEventListener('click'")
	if start < 0 {
		t.Fatal("missing rail sidebar click handler")
	}
	end := strings.Index(src[start:], "});")
	handler := src[start : start+end]
	if strings.Contains(handler, "switchView(") || strings.Contains(handler, "agentflow-sidebar-toggle") || strings.Contains(handler, "setSettingsSidebarExpanded(") {
		t.Fatal("icon rail toggle must not change page, chat list, or settings navigation")
	}
	if !strings.Contains(handler, "setPlatformRailExpanded(") || strings.Contains(src, "setPlatformRailCollapsed(") || strings.Contains(src, "platform-rail-collapsed") {
		t.Fatal("toggle must expand the icon rail into labels, not collapse it away")
	}
	if strings.Contains(src, "agentflow-sidebar-toggle") || strings.Contains(src, "syncRailSidebarToggle") {
		t.Fatal("icon rail toggle must not drive the conversation list")
	}
	css, err := os.ReadFile("product-v4.css")
	if err != nil {
		t.Fatal(err)
	}
	sheet := string(css)
	if !strings.Contains(sheet, "body.platform-rail-expanded") || !strings.Contains(sheet, "--rail-w: 240px") || !strings.Contains(sheet, "body.platform-rail-expanded .platform-rail .rail-label") {
		t.Fatal("expanded icon rail must widen and show labels")
	}
	if strings.Contains(sheet, "platform-rail-collapsed") || strings.Contains(sheet, "--rail-w: 0px") {
		t.Fatal("icon rail must stay visible when labels are hidden")
	}
}
