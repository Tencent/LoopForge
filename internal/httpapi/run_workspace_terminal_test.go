package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceTerminalRequiresProjectMembership(t *testing.T) {
	a, owner, peer, out, _ := sharedWorkbenchFixture(t)
	root := t.TempDir()
	h := &runsHandlers{deps: a.deps}
	h.engine = engine.New(h.repo, runtime.NewManager(root))
	r, _ := h.repo.RunState("shared-run")
	r.PauseRequested = true
	if err := h.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if _, err := h.repo.RequestRunPause(r.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, r.ID), 0700); err != nil {
		t.Fatal(err)
	}
	call := func(user domain.User, operation, id string) (int, runtime.TerminalResult) {
		t.Helper()
		body, _ := json.Marshal(domain.WorkspaceTerminalRequest{Operation: operation, ID: id})
		c, w := workbenchContext(user, "POST", "/", bytes.NewBuffer(body))
		c.Params = gin.Params{{Key: "id", Value: r.ID}}
		h.workspaceTerminal(c)
		if w.Code == 409 {
			var failure map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
				t.Fatal(err)
			}
			if failure["code"] == "workspace_busy" {
				t.Fatalf("non-lease error marked retryable: %s", w.Body)
			}
		}
		var result runtime.TerminalResult
		json.Unmarshal(w.Body.Bytes(), &result)
		return w.Code, result
	}
	if code, _ := call(out, "open", ""); code != 403 {
		t.Fatal("outsider", code)
	}
	// A refused workspace lease is safe to retry; other terminal errors are not.
	lease, err := h.engine.BeginConversationWorkspaceUse(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	cBusy, wBusy := workbenchContext(owner, "POST", "/", bytes.NewBufferString(`{"operation":"open"}`))
	cBusy.Params = gin.Params{{Key: "id", Value: r.ID}}
	h.workspaceTerminal(cBusy)
	var busy map[string]any
	if err := json.Unmarshal(wBusy.Body.Bytes(), &busy); err != nil {
		t.Fatal(err)
	}
	if wBusy.Code != 409 || busy["code"] != "workspace_busy" {
		t.Fatalf("lease refusal: %d %s", wBusy.Code, wBusy.Body)
	}
	if err := h.engine.EndConversationWorkspaceUse(context.Background(), r.ID, lease); err != nil {
		t.Fatal(err)
	}
	// No exclusive editor lease: any project writer can open a terminal once paused.
	code, opened := call(owner, "open", "")
	if code != 200 || opened.ID == "" {
		t.Fatal("owner terminal", code, opened)
	}
	defer h.terminals.CloseScope(fmt.Sprintf("%s:%d", r.ID, owner.ID))
	if code, _ := call(peer, "read", opened.ID); code != 409 {
		t.Fatal("peer used owner's shell", code)
	}
	c, w := workbenchContext(owner, "POST", "/", bytes.NewBufferString("{}"))
	c.Params = gin.Params{{Key: "id", Value: r.ID}}
	h.workspaceRelease(c)
	if w.Code != 200 {
		t.Fatal("release", w.Code)
	}
	if code, _ := call(owner, "read", opened.ID); code != 409 {
		t.Fatal("shell survived release", code)
	}
}
