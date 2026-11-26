package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
)

func TestKnowledgeProjectAuthorization(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "authz-regression")
	s, h := newKBMgmtServer(t)
	register := func(login string) *http.Cookie {
		r := httptest.NewRequest("POST", "http://localhost/api/auth/register", strings.NewReader(fmt.Sprintf(`{"login":%q,"password":"secret1"}`, login)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		r.RemoteAddr = "192.0.2.20:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		return w.Result().Cookies()[0]
	}
	admin, a, b, viewer := register("admin"), register("user-a"), register("user-b"), register("viewer")
	au, _ := s.UserByLoginName("user-a")
	bu, _ := s.UserByLoginName("user-b")
	vu, _ := s.UserByLoginName("viewer")
	for _, p := range []domain.Project{{ID: "a", Name: "A", OwnerID: au.ID, Members: []domain.ProjectMember{{UserID: vu.ID, Role: "viewer"}}}, {ID: "b", Name: "B", OwnerID: bu.ID}} {
		if err := s.PutProject(p); err != nil {
			t.Fatal(err)
		}
	}
	svc := knowledge.NewService(s, nil, nil)
	base, err := svc.EnsureHostedBase("a")
	if err != nil {
		t.Fatal(err)
	}
	source := s.KnowledgeSources(base.ID)[0].ID
	request := func(cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	body := fmt.Sprintf(`{"projectId":"a","sourceId":%q,"path":"private.md","content":"# Private A"}`, source)
	if w := request(a, "POST", "/api/knowledge/bases/documents", body); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, cookie := range []*http.Cookie{b, viewer} {
		if w := request(cookie, "POST", "/api/knowledge/bases/documents", strings.ReplaceAll(body, "Private A", "Poisoned")); w.Code != 403 {
			t.Errorf("P0-1 unauthorized write: %d %s", w.Code, w.Body.String())
		}
	}
	item := domain.KnowledgeSubmission{ID: "secret-a", ProjectID: "a", Source: source, Path: "private.md", Content: "private draft", Status: domain.KnowledgeSubmissionPending}
	if err := s.PutKnowledgeSubmission(item); err != nil {
		t.Fatal(err)
	}
	reads := []struct{ method, path, body string }{
		{"GET", "/api/knowledge/bases?projectId=a", ""}, {"GET", "/api/knowledge/sources?projectId=a", ""},
		{"GET", "/api/knowledge/bases/documents?projectId=a&source=" + source, ""}, {"GET", "/api/knowledge/bases/document?projectId=a&source=" + source + "&path=private.md", ""},
		{"GET", "/api/knowledge/document?projectId=a&source=" + source + "&path=private.md", ""}, {"GET", "/api/knowledge/usage?projectId=a", ""},
		{"GET", "/api/knowledge/candidates?projectId=a", ""}, {"GET", "/api/knowledge/governance?projectId=a", ""}, {"GET", "/api/knowledge/citations?projectId=a", ""},
		{"GET", "/api/knowledge/bases/bindings?projectId=a", ""}, {"GET", "/api/knowledge/bases/sediment?projectId=a&baseId=" + base.ID, ""},
		{"GET", "/api/knowledge/submissions?projectId=a", ""}, {"GET", "/api/knowledge/submissions/secret-a", ""},
		{"POST", "/api/knowledge/search", `{"projectId":"a","query":"Private"}`}, {"POST", "/api/knowledge/bases/search", `{"projectId":"a","query":"Private"}`},
		{"POST", "/api/knowledge/citations/freshness", `{"projectId":"a","citations":[]}`}, {"POST", "/api/knowledge/feedback", `{"projectId":"a","kind":"helpful"}`},
		{"POST", "/api/knowledge/governance/gaps", `{"projectId":"a","query":"x","status":"ignored"}`},
		{"POST", "/api/knowledge/bases/sources/metadata", `{"projectId":"a","sourceId":"` + source + `","enabled":false}`},
	}
	for _, tc := range reads {
		t.Run(tc.path, func(t *testing.T) {
			if w := request(b, tc.method, tc.path, tc.body); w.Code != 403 {
				t.Errorf("P1-1 B reading A: %d %s", w.Code, w.Body.String())
			}
		})
	}
	if w := request(viewer, "POST", "/api/knowledge/feedback", `{"projectId":"a","kind":"helpful"}`); w.Code != 403 {
		t.Errorf("viewer feedback write: %d %s", w.Code, w.Body.String())
	}
	if w := request(viewer, "POST", "/api/knowledge/bases/sources/metadata", fmt.Sprintf(`{"projectId":"a","sourceId":%q,"enabled":false}`, source)); w.Code != 403 {
		t.Errorf("viewer metadata switch: %d %s", w.Code, w.Body.String())
	}
	if w := request(viewer, "POST", "/api/knowledge/governance/gaps", `{"projectId":"a","query":"x","status":"ignored"}`); w.Code != 403 {
		t.Errorf("viewer gap dismissal: %d %s", w.Code, w.Body.String())
	}
	w := request(b, "GET", "/api/knowledge/submissions", "")
	if strings.Contains(w.Body.String(), "secret-a") {
		t.Errorf("unscoped list leaks A: %s", w.Body.String())
	}
	for _, cookie := range []*http.Cookie{a, viewer, admin} {
		if w := request(cookie, "GET", "/api/knowledge/bases/document?projectId=a&source="+source+"&path=private.md", ""); w.Code != 200 {
			t.Errorf("authorized reader: %d %s", w.Code, w.Body.String())
		}
	}
	// A project B owner may read a bound public base, but never dispose its content.
	w = request(admin, "POST", "/api/knowledge/bases/shared", `{"name":"Shared"}`)
	var shared domain.KnowledgeBase
	if err := json.Unmarshal(w.Body.Bytes(), &shared); err != nil {
		t.Fatal(err)
	}
	if w := request(b, "POST", "/api/projects", fmt.Sprintf(`{"id":%q,"name":"Hijack"}`, shared.ID)); w.Code != 409 {
		t.Errorf("P1-2 B chose A's base ID: %d %s", w.Code, w.Body.String())
	}
	sharedSource := s.KnowledgeSources(shared.ID)[0].ID
	payload := fmt.Sprintf(`{"sourceId":%q,"path":"public.md","content":"# Public"}`, sharedSource)
	if w := request(admin, "POST", "/api/knowledge/bases/documents", payload); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err := svc.SetSharedBinding("b", shared.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"update", "retract"} {
		payload := fmt.Sprintf(`{"projectId":"b","source":%q,"path":"public.md","action":%q}`, sharedSource, action)
		if w := request(b, "POST", "/api/knowledge/governance/dispose", payload); w.Code != 403 {
			t.Errorf("P0-2 public disposition: %d %s", w.Code, w.Body.String())
		}
	}
	forged := domain.KnowledgeSubmission{ID: "forged", ProjectID: "b", Source: sharedSource, Path: "public.md", Action: "retract", Status: domain.KnowledgeSubmissionPending}
	if err := s.PutKnowledgeSubmission(forged); err != nil {
		t.Fatal(err)
	}
	if w := request(b, "POST", "/api/knowledge/submissions/forged/publish", ""); w.Code != 403 {
		t.Errorf("P0-2 forged publish: %d %s", w.Code, w.Body.String())
	}
	if _, ok := s.KnowledgeDocument(sharedSource, "public.md"); !ok {
		t.Error("public body deleted")
	}
	// The same public retraction succeeds in the explicit platform-admin scope.
	payload = fmt.Sprintf(`{"source":%q,"path":"public.md","action":"retract"}`, sharedSource)
	w = request(admin, "POST", "/api/knowledge/governance/dispose", payload)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var adminItem domain.KnowledgeSubmission
	if err := json.Unmarshal(w.Body.Bytes(), &adminItem); err != nil {
		t.Fatal(err)
	}
	if w := request(admin, "POST", "/api/knowledge/submissions/"+adminItem.ID+"/publish", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, ok := s.KnowledgeDocument(sharedSource, "public.md"); ok {
		t.Error("admin retraction did not remove public body")
	}
}
