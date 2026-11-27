package web

import (
	"os"
	"strings"
	"testing"
)

// 配置检查面板在属性抽屉打开时必须让出抽屉宽度，并按剩余宽度自适应列数。
func TestFlowCheckPanelYieldsToDrawer(t *testing.T) {
	b, err := os.ReadFile("app/flow-canvas.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	for _, want := range []string{
		"#flowView .fd-stagewrap.has-drawer .flow-check-panel{right:368px}",
		"#flowView .flow-check-grid{grid-template-columns:repeat(auto-fit,",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("flow-canvas.css missing %q", want)
		}
	}
}
