package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestInspectReportsTheElementAtAPoint(t *testing.T) {
	transport := newFakeCDPTransport()
	transport.results = map[string]string{
		"Runtime.evaluate": `{"result":{"value":{"label":"button.btn 「保存」","rect":{"x":10,"y":20,"width":55,"height":35},"viewport":{"width":1280,"height":800},
			"element":{"url":"https://example.com/","selector":"#save","tag":"button","source":"src/Save.tsx:3"}}}}`,
	}
	view := &browserView{targets: map[string]*browserTarget{}, changed: make(chan struct{}), session: "page-1", active: "t1"}
	view.client = newCDPClient(transport, view.onEvent)
	defer view.client.close()

	if got, err := view.inspect(context.Background(), nil, true); err == nil || got != nil {
		t.Fatal("missing point accepted")
	}
	got, err := view.inspect(context.Background(), &domain.BrowserPoint{X: 0.25, Y: 1.7}, true)
	if err != nil || got == nil {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if got.Label != "button.btn 「保存」" || got.Rect.Width != 55 || got.Viewport.Width != 1280 || got.Element == nil || got.Element.Source != "src/Save.tsx:3" {
		t.Fatalf("inspection %+v %+v", got, got.Element)
	}
	transport.mu.Lock()
	last := transport.sent[len(transport.sent)-1]
	transport.mu.Unlock()
	var params struct {
		Expression string `json:"expression"`
	}
	_ = json.Unmarshal(last.Params, &params)
	// Points are clamped to the viewport like input events.
	if !strings.HasSuffix(params.Expression, "})(0.25, 1, true)") {
		t.Fatalf("expression ends %q", params.Expression[len(params.Expression)-40:])
	}

	transport.mu.Lock()
	transport.results["Runtime.evaluate"] = `{"result":{"value":null}}`
	transport.mu.Unlock()
	if got, err := view.inspect(context.Background(), &domain.BrowserPoint{X: 0.5, Y: 0.5}, false); err != nil || got != nil {
		t.Fatalf("empty point: %+v %v", got, err)
	}
}
