package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"strings"
	"testing"
)

func TestBrowserControlRequiresProjectMembership(t *testing.T) {
	a, owner, _, out, _ := sharedWorkbenchFixture(t)
	h := &browserHandlers{deps: a.deps}
	h.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	call := func(user domain.User, body string) (int, domain.BrowserState) {
		t.Helper()
		c, w := workbenchContext(user, "POST", "/", bytes.NewBufferString(body))
		c.Params = gin.Params{{Key: "id", Value: "shared-run"}}
		h.browser(c)
		var state domain.BrowserState
		_ = json.Unmarshal(w.Body.Bytes(), &state)
		return w.Code, state
	}
	if code, _ := call(out, `{"operation":"input","events":[{"type":"text","text":"x"}]}`); code != 403 {
		t.Fatalf("outsider input: %d", code)
	}
	if code, _ := call(out, `{"operation":"navigate","url":"example.com"}`); code != 403 {
		t.Fatalf("outsider navigate: %d", code)
	}
	code, state := call(out, `{"operation":"status"}`)
	if code != 200 || state.Available || !strings.Contains(state.Reason, "尚未启动") {
		t.Fatalf("read-only status: %d %+v", code, state)
	}
	if code, state := call(owner, `{"operation":"navigate","url":"example.com"}`); code != 200 || state.Available {
		t.Fatalf("owner without a browser: %d %+v", code, state)
	}
}

func TestBrowserControlKeepsConversationWorkspaceAlive(t *testing.T) {
	a, owner, _, _, _ := sharedWorkbenchFixture(t)
	h := &browserHandlers{deps: a.deps}
	h.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	call := func(body string) {
		t.Helper()
		c, _ := workbenchContext(owner, "POST", "/", bytes.NewBufferString(body))
		c.Params = gin.Params{{Key: "id", Value: "shared-run"}}
		h.browser(c)
	}
	call(`{"operation":"frame"}`)
	if _, ok := h.repo.WorkspaceCheckpoint("shared-run"); ok {
		t.Fatal("frame polling must not count as workspace use")
	}
	call(`{"operation":"navigate","url":"example.com"}`)
	first, ok := h.repo.WorkspaceCheckpoint("shared-run")
	if !ok || !first.FollowUp || first.LastUsedAt.IsZero() {
		t.Fatalf("control did not mark use: %+v %v", first, ok)
	}
	call(`{"operation":"navigate","url":"example.com"}`)
	if again, _ := h.repo.WorkspaceCheckpoint("shared-run"); !again.LastUsedAt.Equal(first.LastUsedAt) {
		t.Fatal("touches within the interval must be throttled")
	}
}
