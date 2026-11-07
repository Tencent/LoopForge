package web

import (
	"os"
	"strings"
	"testing"
)

func TestToastHarnessTheme(t *testing.T) {
	read := func(name string) string {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	section := func(src, start, end string) string {
		i := strings.Index(src, start)
		if i < 0 {
			t.Fatalf("missing %q", start)
		}
		rest := src[i:]
		j := strings.Index(rest, end)
		if j < 0 {
			t.Fatalf("missing %q after %q", end, start)
		}
		return rest[:j]
	}

	css := section(read("platform.css"), "/* ================= 堆叠式 Toast 通知系统", "/* ================= 列表分页")
	for _, want := range []string{
		"background:var(--hui-surface)",
		"border:1px solid var(--hui-border)",
		"box-shadow:var(--hui-popover-shadow)",
		"color:var(--hui-text)",
		"var(--hui-sans)",
		".toast-item.toast-ok .toast-item-icon{color:var(--hui-success)}",
		".toast-item.toast-err .toast-item-icon{color:var(--hui-error)}",
		".toast-item.toast-warn .toast-item-icon{color:var(--hui-warn)}",
		".toast-item.toast-info .toast-item-icon{color:var(--hui-info)}",
		".toast-item-close{border:none;background:transparent;color:var(--hui-tertiary)",
		".toast-item-close:hover{background:var(--hui-soft);color:var(--hui-text)}",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("platform.css toast section must contain %q", want)
		}
	}
	for _, banned := range []string{"border-left", "var(--panel)", "var(--line-strong)", "var(--green)", "var(--red)", "var(--amber)", "var(--blue)", "var(--muted-2)", "var(--hover)"} {
		if strings.Contains(css, banned) {
			t.Errorf("platform.css toast section must not use legacy %q", banned)
		}
	}

	js := section(read("app.js"), "function toast(", "\n}\n")
	for _, want := range []string{
		"{ok:'check',err:'alert',warn:'warning',info:'info'}",
		`<svg class="agent-icon harness-icon" data-icon="${icons[kind]}" width="16" height="16"`,
		`<svg class="agent-icon harness-icon" data-icon="close" width="14" height="14"`,
		`title="关闭"`,
		"window.AgentFlowHarnessIcons?.hydrate?.(item)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js toast() must contain %q", want)
		}
	}
	if strings.Index(js, "window.AgentFlowHarnessIcons?.hydrate?.(item)") < strings.Index(js, "container.appendChild(item)") {
		t.Error("app.js toast() must hydrate icons after inserting the item")
	}
	for _, banned := range []string{"✓", "⚠️", "ℹ️", ">×<"} {
		if strings.Contains(js, banned) {
			t.Errorf("app.js toast() must not use glyph icon %q", banned)
		}
	}
}
