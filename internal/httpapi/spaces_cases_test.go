package httpapi

import (
	"agentflow/internal/domain"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSpaceValidationAndArtifactBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	created := cvBRequest(t, f, http.MethodPost, "/api/spaces", map[string]any{
		"projectId": "platform", "title": "CVB Space", "summary": "coverage",
	}, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create space = %d: %s", created.Code, created.Body.String())
	}
	var space spaceView
	if err := json.Unmarshal(created.Body.Bytes(), &space); err != nil {
		t.Fatal(err)
	}
	base := "/api/spaces/" + space.ID
	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPatch, base, map[string]any{"title": " "}, 400},
		{http.MethodPatch, base, map[string]any{"summary": strings.Repeat("x", 4001)}, 400},
		{http.MethodPatch, base, map[string]any{"title": "Renamed", "summary": "new", "pinned": true}, 200},
		{http.MethodGet, base + "/members", nil, 200},
		{http.MethodPost, base + "/members", map[string]any{"userId": 999999}, 400},
		{http.MethodDelete, base + "/members", nil, 400},
		{http.MethodGet, base + "/threads", nil, 200},
		{http.MethodPost, base + "/threads", map[string]any{}, 400},
		{http.MethodPost, base + "/threads", map[string]any{"messageId": "missing"}, 400},
		{http.MethodPost, base + "/threads", map[string]any{"title": "Discussion"}, 200},
		{http.MethodPost, base + "/threads", map[string]any{"title": "Run", "runId": "missing"}, 400},
		{http.MethodGet, base + "/messages?threadId=missing", nil, 404},
		{http.MethodPost, base + "/messages", map[string]any{}, 400},
		{http.MethodPost, base + "/messages", map[string]any{"body": strings.Repeat("x", 4001)}, 400},
		{http.MethodPost, base + "/messages", map[string]any{"body": "x", "threadId": "missing"}, 404},
		{http.MethodPost, base + "/messages", map[string]any{"body": "x", "attachments": []any{map[string]any{"id": "bad"}}}, 400},
		{http.MethodPost, base + "/messages", map[string]any{"body": "hello"}, 201},
		{http.MethodGet, base + "/attachments/bad", nil, 404},
		{http.MethodPost, base + "/requirement", map[string]any{"title": strings.Repeat("x", 201)}, 400},
		{http.MethodPost, base + "/requirement", map[string]any{"title": "x", "description": strings.Repeat("x", 8001)}, 400},
		{http.MethodPost, base + "/requirement", map[string]any{"title": "x", "priority": "bad"}, 400},
		{http.MethodGet, base + "/catalog", nil, 200},
		{http.MethodGet, base + "/lanes", nil, 200},
		{http.MethodGet, base + "/lanes/missing", nil, 404},
		{http.MethodGet, base + "/usage", nil, 200},
		{http.MethodPost, base + "/sops", map[string]any{}, 400},
		{http.MethodPost, base + "/sops", map[string]any{"workflowIds": []string{"missing"}}, 400},

		{http.MethodPost, base + "/artifacts", map[string]any{"kind": "doc", "title": "x", "format": "exe"}, 400},
		{http.MethodPost, base + "/artifacts", map[string]any{"kind": "", "title": "", "format": "markdown"}, 400},
		{http.MethodGet, base + "/artifacts/missing", nil, 404},
		{http.MethodGet, base + "/artifacts/missing/revisions/bad", nil, 400},
		{http.MethodGet, base + "/artifacts/missing/revisions/1", nil, 404},
		{http.MethodGet, base + "/artifacts/missing/raw", nil, 404},
	}
	for i, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%d %s %s = %d want %d: %s", i, tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	docRec := cvBRequest(t, f, http.MethodPost, base+"/artifacts", map[string]any{"kind": "doc", "title": "Doc", "format": "text", "body": "body"}, true)
	if docRec.Code != 201 {
		t.Fatalf("doc = %d: %s", docRec.Code, docRec.Body.String())
	}
	var doc domain.SpaceArtifact
	_ = json.Unmarshal(docRec.Body.Bytes(), &doc)
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, base + "/artifacts", nil, 200},
		{http.MethodGet, base + "/artifacts/" + doc.ID, nil, 200},
		{http.MethodPut, base + "/artifacts/" + doc.ID, map[string]any{"title": "Doc2", "format": "text", "body": "v2", "baseVersion": 99}, 409},
		{http.MethodPut, base + "/artifacts/" + doc.ID, map[string]any{"title": "Doc2", "format": "text", "body": "v2", "baseVersion": 1, "announce": true}, 200},
		{http.MethodGet, base + "/artifacts/" + doc.ID + "/revisions", nil, 200},
		{http.MethodGet, base + "/artifacts/" + doc.ID + "/revisions/1", nil, 200},
		{http.MethodGet, base + "/artifacts/" + doc.ID + "/raw", nil, 200},
		{http.MethodDelete, base + "/artifacts/" + doc.ID, nil, 200},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestPublishWorkbenchArtifactBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	room := domain.ChatRoom{ID: "cvb-publish-space", ProjectID: "platform", Name: "Publish"}
	if err := f.s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	gateway := &agentToolGateway{deps: &deps{repo: f.s}}
	validSession := &AgentToolSession{Mode: AgentToolAssistant, SpaceID: room.ID, ProjectID: room.ProjectID, UserID: f.user.ID, User: f.user}
	cases := []struct {
		session *AgentToolSession
		raw     string
	}{
		{nil, `{}`},
		{&AgentToolSession{}, `{}`},
		{validSession, `{`},
		{validSession, `{}`},
		{validSession, `{"title":"x","body":"/tmp/private"}`},
		{validSession, `{"title":"x","format":"exe","body":"x"}`},
		{validSession, `{"title":"x","imageBase64":"bad"}`},
		{validSession, `{"title":"x","imageBase64":"` + base64.StdEncoding.EncodeToString([]byte("not image")) + `"}`},
	}
	for i, tc := range cases {
		if result, err := gateway.publishWorkbenchArtifact(tc.session, json.RawMessage(tc.raw)); err != nil || result == nil {
			t.Errorf("case %d result=%v err=%v", i, result, err)
		}
	}
	png := append([]byte{0x89, 'P', 'N', 'G', 0, 0, 0, 0}, []byte("data")...)
	for _, raw := range []string{
		`{"title":"Document","body":"# hello"}`,
		`{"title":"Prototype","kind":"prototype","body":"<b>x</b>"}`,
		`{"title":"Image","imageBase64":"` + base64.StdEncoding.EncodeToString(png) + `"}`,
		`{"title":"Browser","url":"https://example.test"}`,
	} {
		result, err := gateway.publishWorkbenchArtifact(validSession, json.RawMessage(raw))
		if err != nil || result == nil {
			t.Errorf("publish %s: %v %#v", raw, err, result)
		}
	}
	for _, tc := range []struct {
		raw  []byte
		want string
	}{
		{[]byte{0x89, 'P', 'N', 'G', 0, 0, 0, 0}, "png"},
		{[]byte{0xff, 0xd8, 0xff}, "jpeg"},
		{[]byte("GIF89a"), "gif"},
		{[]byte("RIFFxxxxWEBP"), "webp"},
		{[]byte("none"), ""},
	} {
		if got := sniffImageFormat(tc.raw); got != tc.want {
			t.Errorf("sniff = %q want %q", got, tc.want)
		}
	}
	for _, path := range []string{"/tmp/x", "~/x", `C:\x`} {
		if !looksLikeLocalPath(path) {
			t.Errorf("local path not detected: %q", path)
		}
	}
}

func TestSpaceFlowWorkflowValidationBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	const projectID = "cvb-flow-project"
	if err := f.s.PutProject(domain.Project{ID: projectID, Key: "CVBF", Name: "Flow project", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	h := &spacesHandlers{deps: &deps{repo: f.s}}
	room := domain.ChatRoom{ID: "cvb-flow-room", ProjectID: projectID}
	base := spaceFlowRequest{Name: "Flow", Nodes: []spaceFlowNode{{ID: "one", Name: `Say "hi"`, Type: "script"}}}
	_, _ = h.flowWorkflow(room, base)
	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-flow-box", ProjectID: projectID, Name: "Box", Driver: "bwrap"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "cvb-flow-agent", ProjectID: projectID, Name: "Agent"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "cvb-flow-foreign", ProjectID: "platform", Name: "Foreign"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		req  spaceFlowRequest
		bad  bool
	}{
		{"missing workflow", spaceFlowRequest{WorkflowID: "missing", Name: "x", Nodes: base.Nodes}, true},
		{"blank id", spaceFlowRequest{Name: "x", Nodes: []spaceFlowNode{{Type: "script"}}}, true},
		{"duplicate id", spaceFlowRequest{Name: "x", Nodes: []spaceFlowNode{{ID: "x", Type: "script"}, {ID: "x", Type: "approval"}}}, true},
		{"missing agent", spaceFlowRequest{Name: "x", Nodes: []spaceFlowNode{{ID: "x", Type: "agent", AgentID: "missing"}}}, true},
		{"foreign agent", spaceFlowRequest{Name: "x", Nodes: []spaceFlowNode{{ID: "x", Type: "agent", AgentID: "cvb-flow-foreign"}}}, true},
		{"bad type", spaceFlowRequest{Name: "x", Nodes: []spaceFlowNode{{ID: "x", Type: "other"}}}, true},
		{"bad edge source", spaceFlowRequest{Name: "x", Nodes: base.Nodes, Edges: []spaceLaneEdge{{Source: "missing", Target: "one"}}}, true},
		{"self edge", spaceFlowRequest{Name: "x", Nodes: base.Nodes, Edges: []spaceLaneEdge{{Source: "one", Target: "one"}}}, true},
		{"all node types", spaceFlowRequest{Name: "x", SOPLane: "dev", Nodes: []spaceFlowNode{
			{ID: "a", Type: "agent", AgentID: "cvb-flow-agent"},
			{ID: "s", Type: "script", MetricStage: "deploy"},
			{ID: "ok", Type: "approval"},
		}, Edges: []spaceLaneEdge{{Source: "a", Target: "s"}, {Source: "s", Target: "ok", When: "rework"}}}, false},
	}
	for _, tc := range cases {
		wf, msg := h.flowWorkflow(room, tc.req)
		if tc.bad != (msg != "") {
			t.Errorf("%s bad=%v message=%q workflow=%+v", tc.name, tc.bad, msg, wf)
		}
	}
}

func TestSpaceLaneProjectionAndUtilityBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &spacesHandlers{deps: &deps{repo: f.s}}
	now := time.Now().UTC()
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "cvb-lane-agent", Name: "Lane Agent", ProjectID: "platform"}); err != nil {
		t.Fatal(err)
	}
	namer := h.agentNamer()
	if namer("") != "" || namer("missing") != "" || namer("missing") != "" || namer("cvb-lane-agent") != "Lane Agent" {
		t.Fatal("agent namer")
	}
	lane := spaceLane{WorkflowName: "", Nodes: []spaceLaneNode{}, Edges: []spaceLaneEdge{}}
	run := domain.Run{
		ID: "cvb-lane-run", Status: "running", UpdatedAt: now,
		WorkflowSnapshot: domain.WorkflowDefinition{Name: "Projected", SOPLane: "development",
			Nodes: []domain.WorkflowNode{{ID: "n", Type: "agent", AgentID: "cvb-lane-agent"}, {ID: "nil", Type: "approval"}},
			Edges: []domain.WorkflowEdge{{Source: "n", Target: "nil", When: "success"}}},
		Nodes: map[string]*domain.NodeRun{"n": {Status: "succeeded", Attempt: 2, ReworkCount: 1, Output: strings.Repeat("o", 1400),
			Error: strings.Repeat("e", 700), StartedAt: now.Add(-time.Minute), FinishedAt: now, Usage: domain.TokenUsage{TotalTokens: 15, CostUSD: .2}, ToolCalls: 3}},
		Repositories: []domain.RepositoryContext{{Name: "repo", BaseBranch: "main", WorkBranch: "feature"}},
	}
	fillLane(&lane, run, namer)
	if lane.WorkflowName != "Projected" || lane.Tokens != 15 || lane.ToolCalls != 3 || len(lane.Nodes) != 2 || len(lane.Edges) != 1 {
		t.Fatalf("lane projection: %+v", lane)
	}
	ws := h.laneWorkspace(run)
	if ws.State != "running" || ws.Branch != "feature" {
		t.Fatalf("running workspace: %+v", ws)
	}
	run.Status = "succeeded"
	run.WorkspaceCleanedAt = now
	if ws = h.laneWorkspace(run); ws.State != "cleaned" {
		t.Fatalf("cleaned workspace: %+v", ws)
	}
	if got := withInputs(strings.Repeat("x", 7990), []string{"line"}); len([]rune(got)) > 8000 || !strings.HasSuffix(got, "…") {
		t.Fatalf("withInputs clipping len=%d suffix=%q", len([]rune(got)), got[len(got)-3:])
	}
	for _, tc := range []struct {
		owner, project string
		shared, want   bool
	}{
		{"p", "p", false, true}, {"p", "x", true, true}, {"p", "x", false, false}, {"", "x", true, true},
	} {
		if got := catalogVisible(tc.owner, tc.shared, tc.project); got != tc.want {
			t.Errorf("catalogVisible = %v want %v", got, tc.want)
		}
	}
}
