package httpapi

import (
	"bytes"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestPickedPageElementsBecomeTurnContext(t *testing.T) {
	h, u := workbenchFixture(t)
	h.repo.PutRun(domain.Run{ID: "task", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}, Nodes: map[string]*domain.NodeRun{}})
	element := func() *domain.AssistantPageElement {
		return &domain.AssistantPageElement{
			URL:       "http://localhost:5173/settings",
			Selector:  "#save",
			Tag:       "button",
			Text:      "保存",
			HTML:      strings.Repeat("<b>x</b>", 2000),
			Component: "SaveButton",
			Source:    "src/Settings.tsx:42",
			Styles:    map[string]string{"color": "rgb(0, 0, 0)"},
		}
	}
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	// Task conversations accept them too: they are not task objects.
	req := assistantRequest{ProjectID: "workbench", RunID: "task", Messages: []assistantMessage{{Role: "user", Content: "改成次要按钮", References: []domain.AssistantReference{
		{Type: "element", ID: "el-1", Name: "button#save 「保存」", ProjectID: "forged", Element: element()},
	}}}}
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatal(err)
	}
	ref := req.Messages[0].References[0]
	if ref.ProjectID != "" || ref.Element == nil || len([]rune(ref.Element.HTML)) > 4001 {
		t.Fatalf("canonical %+v", ref)
	}
	for _, want := range []string{"原生预览", "button#save 「保存」", "src/Settings.tsx:42", "SaveButton", "http://localhost:5173/settings", `"selector":"#save"`} {
		if !strings.Contains(req.contextText, want) {
			t.Fatalf("context misses %q: %s", want, req.contextText)
		}
	}

	for _, bad := range []domain.AssistantReference{
		{Type: "element", ID: "el-2"},
		{Type: "element", ID: "../x", Element: element()},
	} {
		req := assistantRequest{ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "x", References: []domain.AssistantReference{bad}}}}
		if err := h.prepareWorkbenchContext(c, &req, u); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
