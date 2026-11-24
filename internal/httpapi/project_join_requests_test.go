package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProjectJoinRequests(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h := New(s, engine.New(s, runtime.NewManager(t.TempDir())), nil, "", "")
	session := func(login string) *http.Cookie {
		r := httptest.NewRequest("POST", "http://localhost/api/auth/register", bytes.NewBufferString(`{"login":"`+login+`","password":"secret1"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		r.RemoteAddr = "192.0.2.10:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || len(w.Result().Cookies()) != 1 {
			t.Fatalf("register %s: %d %s", login, w.Code, w.Body.String())
		}
		return w.Result().Cookies()[0]
	}
	call := func(cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	expect := func(w *httptest.ResponseRecorder, code int, what string) domain.ProjectJoinRequest {
		t.Helper()
		if w.Code != code {
			t.Fatalf("%s: want %d, got %d %s", what, code, w.Code, w.Body.String())
		}
		var v domain.ProjectJoinRequest
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return v
	}
	list := func(cookie *http.Cookie, query string) []domain.ProjectJoinRequest {
		t.Helper()
		w := call(cookie, "GET", "/api/project-join-requests"+query, "")
		if w.Code != 200 {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var out []domain.ProjectJoinRequest
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	user := func(login string) int64 {
		u, _ := s.UserByLoginName(login)
		return u.ID
	}

	session("admin")
	owner, maint, member := session("owner"), session("maint"), session("member")
	alice, bob, carol, other := session("alice"), session("bob"), session("carol"), session("other")
	if err := s.PutProject(domain.Project{ID: "p1", Name: "P1", OwnerID: user("owner"), Members: []domain.ProjectMember{
		{UserID: user("maint"), Role: "maintainer"}, {UserID: user("member"), Role: "member"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProject(domain.Project{ID: "open", Name: "Open"}); err != nil {
		t.Fatal(err)
	}

	req := expect(call(alice, "POST", "/api/projects/p1/join-requests", `{"reason":"想参与登录模块"}`), 200, "alice applies")
	if req.Status != domain.JoinRequestPending || req.Role != domain.ProjectRoleMember || req.UserID != user("alice") {
		t.Fatalf("unexpected request: %+v", req)
	}
	expect(call(alice, "POST", "/api/projects/p1/join-requests", `{}`), 409, "duplicate pending request")
	expect(call(owner, "POST", "/api/projects/p1/join-requests", `{}`), 409, "owner applies")
	expect(call(member, "POST", "/api/projects/p1/join-requests", `{}`), 409, "member applies")
	expect(call(bob, "POST", "/api/projects/p1/join-requests", `{"role":"maintainer"}`), 400, "applicant asks for maintainer")
	expect(call(bob, "POST", "/api/projects/open/join-requests", `{}`), 400, "open projects need no request")
	expect(call(bob, "POST", "/api/projects/missing/join-requests", `{}`), 404, "missing project")

	if got := list(alice, ""); len(got) != 1 || got[0].ID != req.ID {
		t.Fatalf("applicant sees own request: %+v", got)
	}
	if got := list(maint, "?status=pending"); len(got) != 1 {
		t.Fatalf("maintainer sees pending requests: %+v", got)
	}
	if got := list(member, ""); len(got) != 0 {
		t.Fatalf("plain members do not review: %+v", got)
	}
	if got := list(other, ""); len(got) != 0 {
		t.Fatalf("outsiders see nothing: %+v", got)
	}

	approve := fmt.Sprintf("/api/project-join-requests/%d/approve", req.ID)
	stale, _ := s.Project("p1")
	expect(call(member, "POST", approve, ""), 403, "member approves")
	expect(call(other, "POST", approve, ""), 403, "outsider approves")
	expect(call(maint, "POST", approve, `{"role":"maintainer"}`), 403, "maintainer grants maintainer")
	done := expect(call(maint, "POST", approve, `{"role":"viewer"}`), 200, "maintainer approves as viewer")
	if done.Status != domain.JoinRequestApproved || done.ReviewerID != user("maint") || done.ReviewedAt == nil {
		t.Fatalf("approved request: %+v", done)
	}
	if p, _ := s.Project("p1"); domain.ProjectMemberRole(p, user("alice")) != domain.ProjectRoleViewer {
		t.Fatalf("approved applicant not added as viewer: %+v", p.Members)
	}
	stale.Description = "Owner edited an old draft in another session"
	body, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	expect(call(owner, "PUT", "/api/projects", string(body)), 409, "stale draft cannot remove approved member")
	expect(call(owner, "PUT", "/api/projects", `{"id":"p1","name":"P1","members":[]}`), 409, "member replacement requires snapshot token")
	expect(call(owner, "PUT", "/api/projects", `{"id":"p1","name":"P1","description":"settings only"}`), 200, "omitting members preserves live roster")
	if p, _ := s.Project("p1"); domain.ProjectMemberRole(p, user("alice")) != domain.ProjectRoleViewer || p.Description != "settings only" {
		t.Fatalf("settings save lost approved membership: %+v", p)
	}
	expect(call(owner, "POST", approve, ""), 409, "approve twice")

	bobReq := expect(call(bob, "POST", "/api/projects/p1/join-requests", `{"role":"viewer"}`), 200, "bob applies")
	withdraw := fmt.Sprintf("/api/project-join-requests/%d/withdraw", bobReq.ID)
	expect(call(other, "POST", withdraw, ""), 404, "others cannot withdraw")
	expect(call(bob, "POST", withdraw, ""), 200, "bob withdraws")
	expect(call(owner, "POST", fmt.Sprintf("/api/project-join-requests/%d/approve", bobReq.ID), ""), 409, "approve withdrawn")

	carolReq := expect(call(carol, "POST", "/api/projects/p1/join-requests", `{}`), 200, "carol applies")
	rejected := expect(call(owner, "POST", fmt.Sprintf("/api/project-join-requests/%d/reject", carolReq.ID), ""), 200, "owner rejects")
	if rejected.Status != domain.JoinRequestRejected {
		t.Fatalf("rejected request: %+v", rejected)
	}
	if p, _ := s.Project("p1"); domain.ProjectMemberRole(p, user("carol")) != "" {
		t.Fatal("rejected applicant was added")
	}
	again := expect(call(carol, "POST", "/api/projects/p1/join-requests", `{}`), 200, "carol applies again after rejection")
	expect(call(owner, "POST", fmt.Sprintf("/api/project-join-requests/%d/approve", again.ID), `{"role":"maintainer"}`), 200, "owner grants maintainer")
	if p, _ := s.Project("p1"); domain.ProjectMemberRole(p, user("carol")) != domain.ProjectRoleMaintainer {
		t.Fatalf("owner-granted maintainer missing: %+v", p.Members)
	}

	// Another session may add a pending applicant directly before review.
	bobReq = expect(call(bob, "POST", "/api/projects/p1/join-requests", `{}`), 200, "bob reapplies")
	p, _ := s.Project("p1")
	p.Members = append(p.Members, domain.ProjectMember{UserID: user("bob"), Role: "viewer"})
	body, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	expect(call(owner, "PUT", "/api/projects", string(body)), 200, "current member token permits direct addition")
	expect(call(owner, "POST", fmt.Sprintf("/api/project-join-requests/%d/approve", bobReq.ID), ""), 409, "already-member approval must not report a different role")
	stored, _ := s.ProjectJoinRequest(bobReq.ID)
	p, _ = s.Project("p1")
	if stored.Status != domain.JoinRequestPending || domain.ProjectMemberRole(p, user("bob")) != "viewer" {
		t.Fatalf("conflicting approval changed request or existing role: %+v %+v", stored, p.Members)
	}

	otherReq := expect(call(other, "POST", "/api/projects/p1/join-requests", `{}`), 200, "other applies before archive")
	expect(call(owner, "DELETE", "/api/projects/p1", ""), 200, "owner archives project")
	for _, action := range []string{"approve", "reject"} {
		expect(call(owner, "POST", fmt.Sprintf("/api/project-join-requests/%d/%s", otherReq.ID, action), ""), 404, "archived project cannot be reviewed")
	}
	stored, _ = s.ProjectJoinRequest(otherReq.ID)
	p, _ = s.Project("p1")
	if stored.Status != domain.JoinRequestPending || domain.ProjectMemberRole(p, user("other")) != "" {
		t.Fatal("archived review changed pending request or members")
	}
}
