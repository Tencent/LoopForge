package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func TestSpaceFeaturesLifecycleAndAuthorization(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	f := cvBNewHTTPFixture(t)
	owner := domain.User{ID: 8401, LoginName: "cvb-space-owner", ChineseName: "Owner"}
	member := domain.User{ID: 8402, LoginName: "cvb-space-member", ChineseName: "Member"}
	viewer := domain.User{ID: 8403, LoginName: "cvb-space-viewer"}
	outsider := domain.User{ID: 8404, LoginName: "cvb-space-outsider"}
	for _, user := range []domain.User{owner, member, viewer, outsider} {
		if err := f.s.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	project := domain.Project{ID: "cvb-space-features", Key: "CSF", Name: "Space features", OwnerID: owner.ID, Members: []domain.ProjectMember{
		{UserID: member.ID, Role: "member"}, {UserID: viewer.ID, Role: "viewer"},
	}}
	foreign := domain.Project{ID: "cvb-space-foreign", Key: "CSX", Name: "Foreign", OwnerID: owner.ID}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutProject(foreign); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "cvb-space-existing-issue", ProjectID: project.ID, Title: "Existing", Status: "todo", Priority: "high", Kind: "issue", Source: "local"}
	foreignIssue := domain.Issue{ID: "cvb-space-foreign-issue", ProjectID: foreign.ID, Title: "Foreign", Status: "todo", Priority: "low", Kind: "issue", Source: "local"}
	if err := f.s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutIssue(foreignIssue); err != nil {
		t.Fatal(err)
	}

	for i, tc := range []struct {
		user         domain.User
		method, path string
		body         any
		want         int
	}{
		{owner, http.MethodGet, "/api/spaces", nil, http.StatusBadRequest},
		{owner, http.MethodGet, "/api/spaces?projectId=missing", nil, http.StatusNotFound},
		{outsider, http.MethodGet, "/api/spaces?projectId=" + project.ID, nil, http.StatusForbidden},
		{owner, http.MethodPost, "/api/spaces", "{", http.StatusBadRequest},
		{owner, http.MethodPost, "/api/spaces", map[string]any{"projectId": project.ID, "title": strings.Repeat("界", 81)}, http.StatusBadRequest},
		{owner, http.MethodPost, "/api/spaces", map[string]any{"projectId": project.ID, "title": "x", "summary": strings.Repeat("界", 4001)}, http.StatusBadRequest},
		{owner, http.MethodPost, "/api/spaces", map[string]any{"projectId": "missing", "title": "x"}, http.StatusBadRequest},
		{owner, http.MethodPost, "/api/spaces", map[string]any{"projectId": project.ID, "title": "x", "issueId": "missing"}, http.StatusBadRequest},
		{owner, http.MethodPost, "/api/spaces", map[string]any{"projectId": project.ID, "title": "x", "issueId": foreignIssue.ID}, http.StatusBadRequest},
		{owner, http.MethodPost, "/api/spaces", map[string]any{"projectId": project.ID, "title": "x", "members": []any{map[string]any{"userId": outsider.ID}}}, http.StatusBadRequest},
		{viewer, http.MethodPost, "/api/spaces", map[string]any{"projectId": project.ID, "title": "x"}, http.StatusForbidden},
	} {
		rec := cvBMiscRequestAs(t, f, tc.user, tc.method, tc.path, tc.body)
		if rec.Code != tc.want {
			t.Fatalf("validation %d = %d want %d: %s", i, rec.Code, tc.want, rec.Body.String())
		}
	}

	created := cvBMiscRequestAs(t, f, owner, http.MethodPost, "/api/spaces", map[string]any{
		"projectId": project.ID, "title": "Linked space", "summary": "summary", "creatorTitle": "lead",
		"issueId": issue.ID, "members": []any{map[string]any{"userId": owner.ID}, map[string]any{"userId": member.ID, "title": strings.Repeat("m", 30)}},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create linked = %d: %s", created.Code, created.Body.String())
	}
	var linked spaceView
	if err := json.Unmarshal(created.Body.Bytes(), &linked); err != nil {
		t.Fatal(err)
	}
	if linked.Requirement == nil || len(linked.Members) != 2 {
		t.Fatalf("linked view = %+v", linked)
	}
	duplicate := cvBMiscRequestAs(t, f, owner, http.MethodPost, "/api/spaces", map[string]any{
		"projectId": project.ID, "title": "Duplicate", "issueId": issue.ID,
	})
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate issue = %d: %s", duplicate.Code, duplicate.Body.String())
	}
	base := "/api/spaces/" + linked.ID
	for i, tc := range []struct {
		user         domain.User
		method, path string
		body         any
		want         int
	}{
		{owner, http.MethodGet, "/api/spaces?projectId=" + project.ID, nil, http.StatusOK},
		{owner, http.MethodGet, base, nil, http.StatusOK},
		{viewer, http.MethodGet, base, nil, http.StatusOK},
		{viewer, http.MethodPatch, base, map[string]any{"pinned": true}, http.StatusForbidden},
		{outsider, http.MethodGet, base, nil, http.StatusForbidden},
		{owner, http.MethodPatch, base, "{", http.StatusBadRequest},
		{owner, http.MethodPatch, base, map[string]any{"title": strings.Repeat("界", 81)}, http.StatusBadRequest},
		{owner, http.MethodPatch, base, map[string]any{"title": " Updated ", "summary": " updated ", "pinned": true}, http.StatusOK},
		{owner, http.MethodPost, base + "/requirement", map[string]any{"title": "again"}, http.StatusConflict},
		{owner, http.MethodPost, base + "/members", "{", http.StatusBadRequest},
		{owner, http.MethodPost, base + "/members", map[string]any{"userId": member.ID, "title": "again"}, http.StatusOK},
		{owner, http.MethodDelete, base + "/members?userId=" + "8402", nil, http.StatusOK},
		{owner, http.MethodPost, base + "/members", map[string]any{"userId": member.ID, "title": "developer"}, http.StatusOK},
		{owner, http.MethodGet, base + "/members", nil, http.StatusOK},
	} {
		rec := cvBMiscRequestAs(t, f, tc.user, tc.method, tc.path, tc.body)
		if rec.Code != tc.want {
			t.Fatalf("lifecycle %d %s = %d want %d: %s", i, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	plain := cvBMiscRequestAs(t, f, owner, http.MethodPost, "/api/spaces", map[string]any{"projectId": project.ID, "title": "Plain"})
	var second spaceView
	if plain.Code != http.StatusCreated || json.Unmarshal(plain.Body.Bytes(), &second) != nil {
		t.Fatalf("plain space = %d: %s", plain.Code, plain.Body.String())
	}
	secondBase := "/api/spaces/" + second.ID
	if rec := cvBMiscRequestAs(t, f, owner, http.MethodPost, secondBase+"/requirement", "{"); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed requirement = %d: %s", rec.Code, rec.Body.String())
	}
	reqRec := cvBMiscRequestAs(t, f, owner, http.MethodPost, secondBase+"/requirement", map[string]any{"title": " New requirement ", "description": " work "})
	if reqRec.Code != http.StatusCreated {
		t.Fatalf("create requirement = %d: %s", reqRec.Code, reqRec.Body.String())
	}
	h := &spacesHandlers{deps: &deps{repo: f.s}}
	if got := h.requirementOf(domain.RequirementSpace{IssueID: "deleted-or-missing"}); got != nil {
		t.Fatalf("missing requirement = %+v", got)
	}
}

func TestSpaceFeaturesThreadsMessagesAndDefaults(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	room := domain.ChatRoom{ID: "cvb-thread-space", ProjectID: "platform", Name: "Threads", CreatedBy: f.user.ID}
	if err := f.s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	main := domain.ChatMessage{ID: "cvb-thread-origin", RoomID: room.ID, UserID: f.user.ID, Author: "owner", Kind: domain.ChatKindText, Body: "  an origin message with words  "}
	child := domain.ChatMessage{ID: "cvb-thread-child", RoomID: room.ID, UserID: f.user.ID, Kind: domain.ChatKindText, Body: "child", ThreadID: "already"}
	if err := f.s.PutMessage(main); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutMessage(child); err != nil {
		t.Fatal(err)
	}
	base := "/api/spaces/" + room.ID
	for i, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPost, base + "/threads", "{", http.StatusBadRequest},
		{http.MethodPost, base + "/threads", map[string]any{"messageId": child.ID}, http.StatusBadRequest},
		{http.MethodPost, base + "/threads", map[string]any{"title": strings.Repeat("界", 81)}, http.StatusBadRequest},
		{http.MethodPost, base + "/threads", map[string]any{"messageId": main.ID}, http.StatusOK},
		{http.MethodPost, base + "/threads", map[string]any{"messageId": main.ID}, http.StatusOK},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Fatalf("thread %d = %d want %d: %s", i, rec.Code, tc.want, rec.Body.String())
		}
	}
	threads := f.s.SpaceThreads(room.ID)
	if len(threads) != 1 || threads[0].MessageID != main.ID {
		t.Fatalf("threads = %+v", threads)
	}
	threadID := threads[0].ID
	for i, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, base + "/messages?threadId=" + threadID, nil, http.StatusOK},
		{http.MethodPost, base + "/messages", "{", http.StatusBadRequest},
		{http.MethodPost, base + "/messages", map[string]any{"body": "too many", "attachments": []any{
			map[string]any{"id": "1"}, map[string]any{"id": "2"}, map[string]any{"id": "3"},
			map[string]any{"id": "4"}, map[string]any{"id": "5"}, map[string]any{"id": "6"},
			map[string]any{"id": "7"}, map[string]any{"id": "8"}, map[string]any{"id": "9"},
		}}, http.StatusBadRequest},
		{http.MethodPost, base + "/messages", map[string]any{"body": " reply ", "threadId": threadID}, http.StatusCreated},
		{http.MethodGet, base + "/messages", nil, http.StatusOK},
		{http.MethodGet, base + "/threads", nil, http.StatusOK},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Fatalf("message %d = %d want %d: %s", i, rec.Code, tc.want, rec.Body.String())
		}
	}

	d := &deps{repo: f.s}
	h := &spacesHandlers{deps: d, rooms: &roomsHandlers{deps: d}}
	for _, invoke := range []func(*gin.Context){
		h.spaces, h.space, h.members, h.threads, h.messages, h.artifacts, h.artifact,
	} {
		c, rec := cvBMiscContext(http.MethodOptions, "/direct")
		c.Params = gin.Params{{Key: "id", Value: room.ID}, {Key: "artifactId", Value: "missing"}}
		invoke(c)
		if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("direct default = %d: %s", rec.Code, rec.Body.String())
		}
	}
}

func TestSpaceFeaturesArtifactsRawAndPublishing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := cvBNewHTTPFixture(t)
	room := domain.ChatRoom{ID: "cvb-artifact-space", ProjectID: "platform", Name: "Artifacts", CreatedBy: f.user.ID}
	if err := f.s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	base := "/api/spaces/" + room.ID + "/artifacts"
	create := func(kind, title, format, body string) domain.SpaceArtifact {
		t.Helper()
		rec := cvBRequest(t, f, http.MethodPost, base, map[string]any{"kind": kind, "title": title, "format": format, "body": body, "announce": true}, true)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s = %d: %s", title, rec.Code, rec.Body.String())
		}
		var item domain.SpaceArtifact
		if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	text := create("doc", "Text", "text", "hello")
	html := create("prototype", "HTML", "html", "<script>1</script>")
	pngBody := base64.StdEncoding.EncodeToString(append([]byte{0x89, 'P', 'N', 'G', 0, 0, 0, 0}, []byte("pixels")...))
	png := create("file", "PNG", "png", "data:image/png;base64,\n"+pngBody)
	badImage := create("file", "Broken", "png", "not-base64")
	if rec := cvBRequest(t, f, http.MethodPost, base, "{", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed artifact create = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodPut, base+"/"+text.ID, "{", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed artifact update = %d: %s", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{base + "/" + text.ID + "/raw", http.StatusOK},
		{base + "/" + text.ID + "/raw?version=bad", http.StatusOK},
		{base + "/" + html.ID + "/raw", http.StatusOK},
		{base + "/" + png.ID + "/raw", http.StatusOK},
		{base + "/" + badImage.ID + "/raw", http.StatusBadRequest},
		{base + "/" + text.ID + "/raw?version=99", http.StatusNotFound},
		{base + "/" + text.ID + "/revisions/not-a-number", http.StatusBadRequest},
		{base + "/" + text.ID + "/revisions/99", http.StatusNotFound},
	} {
		rec := cvBRequest(t, f, http.MethodGet, tc.path, nil, true)
		if rec.Code != tc.want {
			t.Errorf("GET %s = %d want %d: %s", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	update := cvBRequest(t, f, http.MethodPut, base+"/"+text.ID, map[string]any{
		"title": "Text v2", "format": "text", "body": "second", "baseVersion": 1,
	}, true)
	if update.Code != http.StatusOK {
		t.Fatalf("update inherited kind = %d: %s", update.Code, update.Body.String())
	}
	announced := cvBRequest(t, f, http.MethodPut, base+"/"+text.ID, map[string]any{
		"title": "Text v3", "format": "text", "body": "third", "baseVersion": 2, "announce": true,
	}, true)
	if announced.Code != http.StatusOK {
		t.Fatalf("announced update = %d: %s", announced.Code, announced.Body.String())
	}

	attachmentID := strings.Repeat("a", 32)
	record := workbenchAttachmentRecord{ProjectID: room.ProjectID, AssistantAttachment: domain.AssistantAttachment{
		ID: attachmentID, Name: "pixel.png", MimeType: "image/png", Size: 4,
	}}
	dir := workbenchAttachmentDir(f.user.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	rawRecord, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(dir, attachmentID+".json"), rawRecord, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, attachmentID+".bin"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutMessage(domain.ChatMessage{ID: "cvb-attachment-message", RoomID: room.ID, UserID: f.user.ID, Kind: domain.ChatKindText,
		Body: "image", Attachments: []domain.AssistantAttachment{record.AssistantAttachment}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/spaces/" + room.ID + "/attachments/" + attachmentID,
		"/api/spaces/" + room.ID + "/attachments/" + attachmentID + "?download=1",
	} {
		if rec := cvBRequest(t, f, http.MethodGet, path, nil, true); rec.Code != http.StatusOK {
			t.Errorf("attachment %s = %d: %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/spaces/"+room.ID+"/attachments/"+strings.Repeat("b", 32), nil, true); rec.Code != http.StatusNotFound {
		t.Errorf("unreferenced attachment = %d: %s", rec.Code, rec.Body.String())
	}
	missingFileID := strings.Repeat("c", 32)
	if err := f.s.PutMessage(domain.ChatMessage{ID: "cvb-attachment-missing-file", RoomID: room.ID, UserID: f.user.ID, Kind: domain.ChatKindText,
		Body: "missing", Attachments: []domain.AssistantAttachment{{ID: missingFileID, Name: "gone.txt", MimeType: "text/plain"}}}); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/spaces/"+room.ID+"/attachments/"+missingFileID, nil, true); rec.Code != http.StatusNotFound {
		t.Errorf("missing attachment file = %d: %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{
		"/api/spaces/missing/artifacts",
		"/api/spaces/missing/artifacts/item",
		"/api/spaces/missing/artifacts/item/revisions",
		"/api/spaces/missing/artifacts/item/raw",
		"/api/spaces/missing/attachments/" + attachmentID,
	} {
		if rec := cvBRequest(t, f, http.MethodGet, path, nil, true); rec.Code != http.StatusNotFound {
			t.Errorf("missing space %s = %d: %s", path, rec.Code, rec.Body.String())
		}
	}

	if raw, err := decodeArtifactImage(" YQ "); err != nil || string(raw) != "a" {
		t.Fatalf("raw base64 = %q, %v", raw, err)
	}
	for _, value := range []string{"", "%%%", "data:image/png;base64,"} {
		if _, err := decodeArtifactImage(value); err == nil {
			t.Errorf("decode accepted %q", value)
		}
	}
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false}, {"relative/file", false}, {"line\nbreak", false}, {strings.Repeat("x", 501), false}, {"/tmp/file", true},
	} {
		if got := looksLikeLocalPath(tc.value); got != tc.want {
			t.Errorf("looksLikeLocalPath(%q) = %v", tc.value, got)
		}
	}

	gateway := &agentToolGateway{deps: &deps{repo: f.s}}
	session := &AgentToolSession{Mode: AgentToolAssistant, SpaceID: room.ID, ProjectID: room.ProjectID, UserID: f.user.ID, User: domain.User{}}
	pngLarge := append([]byte{0x89, 'P', 'N', 'G', 0, 0, 0, 0}, make([]byte, (700<<10)+1)...)
	for i, tc := range []struct {
		session *AgentToolSession
		raw     string
	}{
		{&AgentToolSession{Mode: AgentToolAssistant, SpaceID: "missing", ProjectID: room.ProjectID}, `{"title":"x"}`},
		{&AgentToolSession{Mode: AgentToolAssistant, SpaceID: room.ID, ProjectID: "wrong"}, `{"title":"x"}`},
		{session, `{"title":"` + strings.Repeat("x", 121) + `","body":"x"}`},
		{session, `{"title":"bad image","imageBase64":"%%%"}`},
		{session, `{"title":"large","imageBase64":"` + base64.StdEncoding.EncodeToString(pngLarge) + `"}`},
		{session, `{"title":"ppt","kind":"ppt","body":"slides"}`},
	} {
		result, err := gateway.publishWorkbenchArtifact(tc.session, json.RawMessage(tc.raw))
		if err != nil || result == nil {
			t.Errorf("publish %d = %#v, %v", i, result, err)
		}
	}

	closed, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	closedDeps := &deps{repo: closed}
	closedSpaces := &spacesHandlers{deps: closedDeps, rooms: &roomsHandlers{deps: closedDeps}}
	if err := closedSpaces.saveSpace(room, domain.RequirementSpace{ID: room.ID, ProjectID: room.ProjectID}); err == nil {
		t.Error("saveSpace on closed store succeeded")
	}
	if _, err := closedSpaces.post(room, domain.ChatMessage{Kind: domain.ChatKindText, Body: "x"}); err == nil {
		t.Error("post on closed store succeeded")
	}
	if _, err := closedSpaces.createIssue(room, "title", "description", "medium", 0, "actor"); err == nil {
		t.Error("createIssue on closed store succeeded")
	}
	c, rec := cvBMiscContext(http.MethodPost, "/direct")
	closedSpaces.writeArtifact(c, room, domain.SpaceArtifact{ID: "closed-art", SpaceID: room.ID},
		spaceArtifactRequest{Kind: "doc", Title: "closed", Format: "text", Body: "body"}, 0, false)
	if rec.Code < 400 {
		t.Errorf("closed artifact = %d: %s", rec.Code, rec.Body.String())
	}
	c, rec = cvBMiscContext(http.MethodOptions, "/direct")
	c.Params = gin.Params{{Key: "id", Value: room.ID}, {Key: "artifactId", Value: text.ID}}
	d := &deps{repo: f.s}
	(&spacesHandlers{deps: d, rooms: &roomsHandlers{deps: d}}).artifact(c)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("artifact default method = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSpaceFeaturesLanesCatalogFlowAndUsage(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	const projectID = "cvb-lane-features"
	project := domain.Project{ID: projectID, Key: "CLF", Name: "Lane features", OwnerID: f.user.ID}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-lane-box", ProjectID: projectID, Name: "Box", Driver: "bwrap"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "cvb-lane-visible", ProjectID: projectID, Name: "Visible"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "cvb-lane-hidden", ProjectID: "other", Name: "Hidden"}); err != nil {
		t.Fatal(err)
	}
	room := domain.ChatRoom{ID: "cvb-lane-space", ProjectID: projectID, Name: "Lanes", CreatedBy: f.user.ID}
	if err := f.s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	runID := "cvb-lane-detail-run"
	dispatch := domain.ChatMessage{ID: "cvb-lane-dispatch", RoomID: room.ID, UserID: f.user.ID, Author: "owner", Kind: domain.ChatKindDispatch,
		RunID: runID, IssueID: "issue", WorkflowID: "wf", WorkflowName: "Workflow", Body: "dispatch", CreatedAt: now.Add(-time.Minute)}
	if err := f.s.PutMessage(dispatch); err != nil {
		t.Fatal(err)
	}
	events := make([]domain.RunEvent, 305)
	for i := range events {
		events[i] = domain.RunEvent{ID: "event-" + newChatID("e"), RunID: runID, NodeID: "n1", Type: "progress", Message: strings.Repeat("m", 700), Timestamp: now}
	}
	run := domain.Run{ID: runID, WorkflowID: "wf", Status: "succeeded", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, Revision: 1,
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", Name: "Workflow", SOPLane: "backend", Nodes: []domain.WorkflowNode{
			{ID: "n1", Name: "Node", Type: "agent", AgentID: "cvb-lane-visible"}, {ID: "n2", Name: "Waiting", Type: "approval"}, {ID: "n3", Name: "Not started", Type: "approval"},
		}, Edges: []domain.WorkflowEdge{{Source: "n1", Target: "n2", When: "success"}}},
		Nodes: map[string]*domain.NodeRun{
			"n1": {NodeID: "n1", Status: "succeeded", StartedAt: now.Add(-10 * time.Second), FinishedAt: now, Usage: domain.TokenUsage{TotalTokens: 12, CostUSD: .3}, ToolCalls: 2},
			"n2": {NodeID: "n2", Status: "running", StartedAt: now.Add(-2 * time.Second)},
			"n3": {NodeID: "n3", Status: "pending"},
		},
		Events: events, Usage: domain.TokenUsage{TotalTokens: 20, CostUSD: .4}, ToolCalls: 3,
		Artifacts:    []domain.Artifact{{ID: "run-art", RunID: runID, NodeID: "n1", Attempt: 1, Name: "result", MediaType: "text/plain", Size: 7, CreatedAt: now}},
		Repositories: []domain.RepositoryContext{{Name: "repo", BaseBranch: "main"}},
	}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	saved := now.Add(-time.Minute)
	if err := f.s.DB.Create(&repo.WorkspaceCheckpoint{RunID: runID, LastUsedAt: saved, SavedAt: &saved, Error: strings.Repeat("e", 400)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutMessage(domain.ChatMessage{ID: "cvb-lane-no-run", RoomID: room.ID, Kind: domain.ChatKindDispatch, RunID: "cvb-missing-run", WorkflowID: "wf", Body: "missing"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutMessage(domain.ChatMessage{ID: "cvb-lane-failed", RoomID: room.ID, Kind: domain.ChatKindDispatch, DispatchError: "boom", WorkflowID: "wf", Body: "failed"}); err != nil {
		t.Fatal(err)
	}
	thread := domain.SpaceThread{ID: "cvb-lane-thread", SpaceID: room.ID, RunID: runID, Title: "Run thread", CreatedBy: f.user.ID}
	if err := f.s.PutSpaceThread(thread); err != nil {
		t.Fatal(err)
	}
	base := "/api/spaces/" + room.ID
	for _, tc := range []struct {
		path string
		want int
	}{
		{base + "/lanes", http.StatusOK},
		{base + "/lanes/" + runID, http.StatusOK},
		{base + "/lanes/cvb-missing-run", http.StatusNotFound},
		{base + "/usage", http.StatusOK},
		{base + "/catalog", http.StatusOK},
	} {
		rec := cvBRequest(t, f, http.MethodGet, tc.path, nil, true)
		if rec.Code != tc.want {
			t.Errorf("GET %s = %d want %d: %s", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	if rec := cvBRequest(t, f, http.MethodPost, base+"/sops", map[string]any{"inputs": []string{"missing"}}, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing SOP input = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodPost, base+"/sops", "{", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed SOP input = %d: %s", rec.Code, rec.Body.String())
	}
	input, err := f.s.PutSpaceArtifact(domain.SpaceArtifact{ID: "cvb-sop-input", SpaceID: room.ID, Kind: "doc", Title: "Specification",
		Summary: "summary", Format: "markdown", Body: "# body"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	linkedIssue := domain.Issue{ID: "cvb-sop-linked", ProjectID: projectID, Title: "Linked", Description: "original", Status: "todo", Priority: "medium", Kind: "issue", Source: "local"}
	if err := f.s.PutIssue(linkedIssue); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSpace(domain.RequirementSpace{ID: room.ID, ProjectID: projectID, IssueID: linkedIssue.ID, Summary: "space summary"}); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodPost, base+"/sops", map[string]any{
		"inputs": []string{input.ID}, "workflowIds": []string{"missing-workflow"},
	}, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("SOP with linked input = %d: %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{
		"/api/spaces/missing/lanes", "/api/spaces/missing/lanes/run", "/api/spaces/missing/catalog",
		"/api/spaces/missing/usage",
	} {
		if rec := cvBRequest(t, f, http.MethodGet, path, nil, true); rec.Code != http.StatusNotFound {
			t.Errorf("missing lane space %s = %d: %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := cvBRequest(t, f, http.MethodPost, "/api/spaces/missing/sops", map[string]any{}, true); rec.Code != http.StatusNotFound {
		t.Errorf("missing SOP space = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodPost, "/api/spaces/missing/flows", map[string]any{}, true); rec.Code != http.StatusNotFound {
		t.Errorf("missing flow space = %d: %s", rec.Code, rec.Body.String())
	}

	for i, body := range []any{
		"{",
		map[string]any{"name": "", "nodes": []any{map[string]any{"id": "x", "type": "script"}}},
		map[string]any{"name": "Empty", "nodes": []any{}},
		map[string]any{"name": "Bad", "nodes": []any{map[string]any{"id": "x", "type": "unknown"}}},
	} {
		rec := cvBRequest(t, f, http.MethodPost, base+"/flows", body, true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("bad flow %d = %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	flow := cvBRequest(t, f, http.MethodPost, base+"/flows", map[string]any{
		"name": "Saved flow", "description": "flow", "nodes": []any{
			map[string]any{"id": "script", "name": `Say "hello"`, "type": "script"},
			map[string]any{"id": "approval", "type": "approval"},
		}, "edges": []any{map[string]any{"source": "script", "target": "approval"}},
	}, true)
	if flow.Code != http.StatusOK {
		t.Fatalf("save flow = %d: %s", flow.Code, flow.Body.String())
	}
	var flowResult struct {
		Artifact domain.SpaceArtifact `json:"artifact"`
		Workflow struct {
			ID string `json:"id"`
		} `json:"workflow"`
	}
	if err := json.Unmarshal(flow.Body.Bytes(), &flowResult); err != nil {
		t.Fatal(err)
	}
	updatedFlow := cvBRequest(t, f, http.MethodPost, base+"/flows", map[string]any{
		"artifactId": flowResult.Artifact.ID, "workflowId": flowResult.Workflow.ID, "name": "Saved flow v2",
		"nodes": []any{map[string]any{"id": "script", "name": "Build", "type": "script", "script": "true", "metricStage": "testing"}},
	}, true)
	if updatedFlow.Code != http.StatusOK && updatedFlow.Code != http.StatusBadRequest {
		t.Fatalf("update flow = %d: %s", updatedFlow.Code, updatedFlow.Body.String())
	}
	missingArtifact := cvBRequest(t, f, http.MethodPost, base+"/flows", map[string]any{
		"artifactId": "missing", "workflowId": flowResult.Workflow.ID, "name": "Missing artifact",
		"nodes": []any{map[string]any{"id": "script", "type": "script"}},
	}, true)
	if missingArtifact.Code != http.StatusBadRequest {
		t.Fatalf("missing flow artifact = %d: %s", missingArtifact.Code, missingArtifact.Body.String())
	}
	badWorkflow := cvBRequest(t, f, http.MethodPost, base+"/flows", map[string]any{
		"name": "Invalid workflow", "nodes": []any{map[string]any{"id": "script", "type": "script", "metricStage": "invalid"}},
	}, true)
	if badWorkflow.Code != http.StatusBadRequest {
		t.Fatalf("invalid stored workflow = %d: %s", badWorkflow.Code, badWorkflow.Body.String())
	}
	nodes := make([]any, 31)
	for i := range nodes {
		nodes[i] = map[string]any{"id": newChatID("node"), "type": "script"}
	}
	if rec := cvBRequest(t, f, http.MethodPost, base+"/flows", map[string]any{"name": "Too many", "nodes": nodes}, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("large flow = %d: %s", rec.Code, rec.Body.String())
	}

	d := &deps{repo: f.s}
	h := &spacesHandlers{deps: d, rooms: &roomsHandlers{deps: d}}
	threadContext, _ := cvBMiscContext(http.MethodPost, "/direct")
	if got, message := h.ensureThread(threadContext, room, "", runID); message != "" || got.ID != thread.ID {
		t.Fatalf("existing run thread = %+v, %q", got, message)
	}
	if _, message := h.flowWorkflow(room, spaceFlowRequest{WorkflowID: "missing", Name: "x", Nodes: []spaceFlowNode{{ID: "x", Type: "script"}}}); message == "" {
		t.Fatal("missing workflow accepted")
	}
	complex, message := h.flowWorkflow(room, spaceFlowRequest{Name: "Complex", Nodes: []spaceFlowNode{
		{ID: "agent", Name: "Agent", Type: "agent", AgentID: "cvb-lane-visible"},
		{ID: "script", Name: `Say "hello"`, Type: "script", MetricStage: "release"},
		{ID: "approve", Type: "approval"},
	}, Edges: []spaceLaneEdge{
		{Source: "agent", Target: "script"}, {Source: "script", Target: "approve", When: "rework"},
	}})
	if message != "" || len(complex.Nodes) != 3 || len(complex.Edges) != 1 || complex.Nodes[1].MetricStage != "release" {
		t.Fatalf("complex workflow = %+v, %q", complex, message)
	}
	for _, req := range []spaceFlowRequest{
		{Name: "blank", Nodes: []spaceFlowNode{{ID: "", Type: "script"}}},
		{Name: "duplicate", Nodes: []spaceFlowNode{{ID: "x", Type: "script"}, {ID: "x", Type: "script"}}},
		{Name: "agent", Nodes: []spaceFlowNode{{ID: "x", Type: "agent", AgentID: "missing"}}},
		{Name: "edge", Nodes: []spaceFlowNode{{ID: "x", Type: "script"}}, Edges: []spaceLaneEdge{{Source: "x", Target: "x"}}},
	} {
		if _, message := h.flowWorkflow(room, req); message == "" {
			t.Errorf("invalid flow accepted: %+v", req)
		}
	}
	if got := withInputs(" base ", []string{"- one", "- two"}); !strings.Contains(got, inputsHeading) {
		t.Fatalf("withInputs = %q", got)
	}
}
