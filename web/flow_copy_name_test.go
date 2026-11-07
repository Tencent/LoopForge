package web

import (
	"os"
	"strings"
	"testing"
)

func TestFlowDesignerCopiesSOPName(t *testing.T) {
	htmlRaw, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlRaw)
	name := strings.Index(html, `id="flowCommandTitle"`)
	copy := strings.Index(html, `id="copyWorkflowName"`)
	if name < 0 || copy < name {
		t.Fatal("copy control must sit with the SOP name in the flow designer")
	}
	button := html[strings.LastIndex(html[:copy], "<button") : strings.Index(html[copy:], "</button>")+copy]
	for _, want := range []string{`data-icon="copy"`, `aria-label="复制 SOP 名称"`, `data-tip="复制 SOP 名称"`, `hidden`, `width="14"`} {
		if !strings.Contains(button, want) {
			t.Errorf("copy button missing %s", want)
		}
	}
	if strings.Contains(button, "title=") {
		t.Fatal("header copy control must not add a native title beside data-tip")
	}
	if strings.Contains(button, "复制 SOP") && strings.Contains(button, `class="lbl"`) {
		t.Error("copy control should be an icon, not a labeled button")
	}

	css, err := os.ReadFile("app/flow-canvas.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "#flowView .fd-copy-name{") || !strings.Contains(string(css), "color:var(--fd-ink-4)") {
		t.Fatal("copy control must stay visually quiet")
	}

	palette, err := os.ReadFile("app/flow-palette.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(palette)
	if !strings.Contains(src, "#sandboxChip,#copyWorkflowName") || !strings.Contains(src, "copyText(name,'SOP 名称已复制')") {
		t.Fatal("copying the SOP name must not open the switcher")
	}
	if !strings.Contains(src, `class="fd-pop-copy"`) || !strings.Contains(src, "data-copy-sop-name") || !strings.Contains(src, "e.target.closest('[data-copy-sop-name]')") {
		t.Fatal("each SOP row needs its own copy control")
	}
	if strings.Contains(src, `data-tip="复制 SOP 名称"`) {
		t.Fatal("row copy control already uses title and must not add a second tip")
	}
	row := src[strings.Index(src, "e.target.closest('[data-copy-sop-name]')"):]
	row = row[:strings.Index(row, "data-switch-workflow")]
	if strings.Contains(row, "setSwitcher(false)") || strings.Contains(row, "switchFlowWorkflow(") {
		t.Fatal("copying a row must not switch SOP or close the menu")
	}
	if !strings.Contains(string(css), "#flowView .fd-pop-copy{") || !strings.Contains(string(css), "color:var(--fd-ink-4)") {
		t.Fatal("row copy control must stay visually quiet")
	}
	header, err := os.ReadFile("app/flows.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(header), "copyName.hidden=!wf") {
		t.Fatal("copy control must hide when no SOP is open")
	}
}
