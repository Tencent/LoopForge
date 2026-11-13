package runtime

import (
	"encoding/json"
	"testing"
)

func targetEvent(t *testing.T, method string, params any) cdpMessage {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return cdpMessage{Method: method, Params: raw}
}

func TestBrowserViewTargetInfoEvents(t *testing.T) {
	view := &browserView{targets: map[string]*browserTarget{}, changed: make(chan struct{})}

	view.onEvent(targetEvent(t, "Target.targetCreated", map[string]any{
		"targetInfo": map[string]any{"targetId": "t1", "type": "page", "title": "First", "url": "https://a.test/"},
	}))
	view.onEvent(targetEvent(t, "Target.targetCreated", map[string]any{
		"targetInfo": map[string]any{"targetId": "w1", "type": "service_worker"},
	}))
	got := view.targets["t1"]
	if got == nil || got.Title != "First" || got.URL != "https://a.test/" || got.created.IsZero() {
		t.Fatalf("created target = %+v", got)
	}
	if _, ok := view.targets["w1"]; ok || len(view.targets) != 1 {
		t.Fatalf("non-page target must be ignored: %+v", view.targets)
	}
	created := got.created

	changed := view.changed
	view.onEvent(targetEvent(t, "Target.targetInfoChanged", map[string]any{
		"targetInfo": map[string]any{"targetId": "t1", "type": "page", "title": "Second", "url": "https://b.test/"},
	}))
	select {
	case <-changed:
	default:
		t.Fatal("target change must notify watchers")
	}
	if got := view.targets["t1"]; got.Title != "Second" || got.URL != "https://b.test/" || !got.created.Equal(created) {
		t.Fatalf("changed target = %+v", got)
	}
}

func TestBrowserViewTargetDestroyedEvents(t *testing.T) {
	view := &browserView{targets: map[string]*browserTarget{
		"t1": {ID: "t1"},
		"t2": {ID: "t2"},
	}, changed: make(chan struct{}), active: "t1", session: "page-1"}

	view.onEvent(targetEvent(t, "Target.targetDestroyed", map[string]any{"targetId": "t2"}))
	if _, ok := view.targets["t2"]; ok || view.activeID() != "t1" || view.session != "page-1" {
		t.Fatalf("destroying an inactive target changed state: active=%q session=%q targets=%v", view.active, view.session, view.targets)
	}

	view.onEvent(targetEvent(t, "Target.targetDestroyed", map[string]any{"targetId": "t1"}))
	view.mu.Lock()
	defer view.mu.Unlock()
	if len(view.targets) != 0 || view.active != "" || view.session != "" {
		t.Fatalf("destroying the active target must clear it: active=%q session=%q targets=%v", view.active, view.session, view.targets)
	}
}
