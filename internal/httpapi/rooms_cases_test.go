package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestRoomValidationAndLifecycleBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/api/rooms?projectId=missing", nil, 404},
		{http.MethodPost, "/api/rooms", map[string]any{"projectId": "missing", "name": "x"}, 400},
		{http.MethodPost, "/api/rooms", map[string]any{"projectId": "platform", "name": strings.Repeat("x", 81)}, 400},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s = %d want %d", tc.path, rec.Code, tc.want)
		}
	}
	created := cvBRequest(t, f, http.MethodPost, "/api/rooms", map[string]any{"projectId": "platform", "name": "CVB Room"}, true)
	if created.Code != 201 {
		t.Fatalf("create room = %d: %s", created.Code, created.Body.String())
	}
	var view domain.ChatRoomView
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	base := "/api/rooms/" + view.ID
	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/api/rooms?projectId=platform", nil, 200},
		{http.MethodGet, base, nil, 200},
		{http.MethodPatch, base, map[string]any{"name": ""}, 400},
		{http.MethodPatch, base, map[string]any{"name": strings.Repeat("x", 81)}, 400},
		{http.MethodPatch, base, map[string]any{"name": "Renamed"}, 200},
		{http.MethodGet, base + "/messages", nil, 200},
		{http.MethodPost, base + "/messages", map[string]any{}, 400},
		{http.MethodPost, base + "/messages", map[string]any{"body": strings.Repeat("x", 4001)}, 400},
		{http.MethodPost, base + "/messages", map[string]any{"body": "hello"}, 201},
		{http.MethodPost, base + "/issues", map[string]any{}, 400},
		{http.MethodPost, base + "/issues", map[string]any{"title": "x"}, 400},
		{http.MethodPost, base + "/issues", map[string]any{"title": strings.Repeat("x", 201), "workflowId": "missing"}, 400},
		{http.MethodPost, base + "/issues", map[string]any{"title": "x", "workflowId": "missing"}, 400},
		{http.MethodPost, base + "/issues", map[string]any{"title": "x", "workflowIds": []string{"a", "b", "c", "d", "e", "f"}}, 400},
		{http.MethodPost, base + "/issues", map[string]any{"issueId": "missing", "workflowId": "generic-backend-delivery"}, 400},
	}
	for i, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%d %s %s = %d want %d: %s", i, tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestRoomPureHelperBranches(t *testing.T) {
	ids := roomWorkflowIDs(roomIssueRequest{WorkflowID: " a ", WorkflowIDs: []string{"a", "", "b"}})
	if len(ids) != 2 {
		t.Fatalf("ids = %#v", ids)
	}
	for _, tc := range []struct {
		wf, wn, agent, an, typ string
	}{
		{"w", "Workflow", "a", "Agent", "both"},
		{"w", "", "", "", "sop"},
		{"", "", "a", "", "role"},
	} {
		typ, _, _ := roomTarget(tc.wf, tc.wn, tc.agent, tc.an)
		if typ != tc.typ {
			t.Errorf("target type = %q want %q", typ, tc.typ)
		}
	}
	if fallbackName(" Name ", "id") != " Name " || fallbackName("", "id") != "id" {
		t.Fatal("fallbackName")
	}
}

func TestRoomDispatchTarget(t *testing.T) {
	for _, tc := range []struct {
		name               string
		ids, names         []string
		typ, id, targetStr string
	}{
		{"single", []string{"wf-1"}, []string{"Build"}, "sop", "wf-1", "SOP「Build」"},
		{"single without name", []string{"wf-1"}, []string{""}, "sop", "wf-1", "SOP「wf-1」"},
		{"multi", []string{"wf-1", "wf-2", "wf-3"}, []string{"Build", "", "Ship"}, "multi", "wf-1", "SOP「Build」、「wf-2」、「Ship」"},
	} {
		typ, id, name := roomDispatchTarget(tc.ids, tc.names)
		if typ != tc.typ || id != tc.id || name != tc.targetStr {
			t.Errorf("%s: roomDispatchTarget = (%q, %q, %q) want (%q, %q, %q)", tc.name, typ, id, name, tc.typ, tc.id, tc.targetStr)
		}
	}
}
