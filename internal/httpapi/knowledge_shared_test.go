package httpapi

import (
	"agentflow/internal/knowledge"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestSharedKnowledgeManagementPermissions(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "shared-test-key")
	s, h := newKBMgmtServer(t)
	registrationID := 100
	register := func(login string) *http.Cookie {
		r := httptest.NewRequest("POST", "http://localhost/api/auth/register", strings.NewReader(`{"login":"`+login+`","password":"secret1"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		registrationID++
		r.RemoteAddr = fmt.Sprintf("198.51.100.%d:1234", registrationID)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		return w.Result().Cookies()[0]
	}
	admin, owner, member := register("admin"), register("owner"), register("member")
	maintainer, viewer, outsider := register("maintainer"), register("viewer"), register("outsider")
	ou, _ := s.UserByLoginName("owner")
	mu, _ := s.UserByLoginName("member")
	managerUser, _ := s.UserByLoginName("maintainer")
	viewerUser, _ := s.UserByLoginName("viewer")
	if err := s.PutProject(domain.Project{ID: "team", Name: "Team", OwnerID: ou.ID, Members: []domain.ProjectMember{{UserID: mu.ID, Role: "member"}, {UserID: managerUser.ID, Role: "maintainer"}, {UserID: viewerUser.ID, Role: "viewer"}}}); err != nil {
		t.Fatal(err)
	}
	request := func(cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(member, "POST", "/api/knowledge/bases/shared", `{"name":"公共规范"}`); w.Code != 403 {
		t.Fatalf("non-admin create: %d %s", w.Code, w.Body.String())
	}
	w := request(admin, "POST", "/api/knowledge/bases/shared", `{"name":"公共规范"}`)
	if w.Code != 200 {
		t.Fatalf("admin create: %d %s", w.Code, w.Body.String())
	}
	var base domain.KnowledgeBase
	if err := json.Unmarshal(w.Body.Bytes(), &base); err != nil {
		t.Fatal(err)
	}
	if base.Layer != "shared" || base.ProjectID != "" {
		t.Fatalf("shared base: %+v", base)
	}
	payload := `{"projectId":"team","baseId":"` + base.ID + `","enabled":true}`
	for _, cookie := range []*http.Cookie{member, viewer, outsider} {
		if w := request(cookie, "PUT", "/api/knowledge/bases/bindings", payload); w.Code != 403 {
			t.Fatalf("non-manager binding: %d %s", w.Code, w.Body.String())
		}
	}
	if w := request(maintainer, "PUT", "/api/knowledge/bases/bindings", payload); w.Code != 200 {
		t.Fatalf("maintainer binding: %d %s", w.Code, w.Body.String())
	}
	if w := request(owner, "PUT", "/api/knowledge/bases/bindings", payload); w.Code != 200 {
		t.Fatalf("owner binding: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/knowledge/bases", ""},
		{"POST", "/api/knowledge/bases/sources", `{"baseId":"` + base.ID + `","gitUrl":"https://example.com/shared.git"}`},
		{"POST", "/api/knowledge/bases/documents", `{"sourceId":"` + knowledge.HostedSourceID(base.ID) + `","path":"x.md","content":"overwrite"}`},
	} {
		if w := request(member, tc.method, tc.path, tc.body); w.Code != 403 {
			t.Fatalf("non-admin shared management: %d %s", w.Code, w.Body.String())
		}
	}
	// An unscoped review submission must not become a public-library write.
	submission := request(member, "POST", "/api/knowledge/submissions", `{"source":"`+knowledge.HostedSourceID(base.ID)+`","path":"rules.md","content":"# Unauthorized replacement"}`)
	if submission.Code != 200 {
		t.Fatalf("queue unscoped submission: %d %s", submission.Code, submission.Body.String())
	}
	var item domain.KnowledgeSubmission
	if err := json.Unmarshal(submission.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if w := request(member, "POST", "/api/knowledge/submissions/"+item.ID+"/publish", ""); w.Code != 403 {
		t.Fatalf("unscoped shared publish: %d %s", w.Code, w.Body.String())
	}
	bindings := s.KnowledgeBindings("team")
	if len(bindings) != 1 || bindings[0].Permission != "read" {
		t.Fatalf("shared binding: %+v", bindings)
	}
}

func TestSharedKnowledgeBindingVisibilityAndReadOnly(t *testing.T) {
	s, h := newKBMgmtServer(t)
	createProject(t, h, "one")
	createProject(t, h, "two")
	w := doJSON(t, h, "POST", "/api/knowledge/bases/shared", `{"name":"通用规范"}`, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var base domain.KnowledgeBase
	json.Unmarshal(w.Body.Bytes(), &base)
	// The public administrator manages shared sources with an empty project scope.
	ov := doJSON(t, h, "GET", "/api/knowledge/bases", "", nil)
	if ov.Code != 200 || !strings.Contains(ov.Body.String(), base.ID) {
		t.Fatalf("shared overview: %d %s", ov.Code, ov.Body.String())
	}
	source := knowledge.HostedSourceID(base.ID)
	writeDoc(t, h, "", source, "统一规范")
	writeDoc(t, h, "one", knowledge.HostedSourceID("kb-one"), "统一规范")
	check := func(project string, want int) {
		t.Helper()
		_, out := searchWord(t, h, project, "统一规范", nil)
		if len(out.Result.Results) != want {
			t.Fatalf("%s hits want %d: %+v", project, want, out)
		}
	}
	check("one", 1)
	check("two", 0)
	url := "/api/knowledge/bases/document?projectId=one&source=" + source + "&path=" + kbDocPath
	if w := doJSON(t, h, "GET", url, "", nil); w.Code != 403 {
		t.Fatalf("unbound fetch: %d %s", w.Code, w.Body.String())
	}
	payload := `{"projectId":"one","baseId":"` + base.ID + `","enabled":true}`
	if w := doJSON(t, h, "PUT", "/api/knowledge/bases/bindings", payload, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	check("one", 2)
	check("two", 0)
	if w := doJSON(t, h, "GET", url, "", nil); w.Code != 200 {
		t.Fatalf("bound fetch: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/knowledge/bases/documents", `{"projectId":"one","sourceId":"` + source + `","path":"x.md","content":"overwrite"}`},
		{"POST", "/api/knowledge/bases/sync", `{"projectId":"one","sourceId":"` + source + `"}`},
		{"DELETE", "/api/knowledge/bases/sources?projectId=one&sourceId=" + source, ""},
	} {
		if w := doJSON(t, h, tc.method, tc.path, tc.body, nil); w.Code != 403 {
			t.Fatalf("shared read only %s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	payload = strings.Replace(payload, `true`, `false`, 1)
	if w := doJSON(t, h, "PUT", "/api/knowledge/bases/bindings", payload, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	check("one", 1)
	if w := doJSON(t, h, "GET", url, "", nil); w.Code != 403 {
		t.Fatalf("unloaded fetch: %d", w.Code)
	}
	_ = s
}

func TestSharedKnowledgeBothAgentChannelsFollowLiveBindings(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "shared-agent-key")
	resetAgentToolSessionsForTest()
	s, h := newKBMgmtServer(t)
	createProject(t, h, "one")
	createProject(t, h, "two")
	// B owns one; A owns two. Agent tokens/sessions are issued for B's project.
	for _, identity := range []struct {
		project, login string
		id             int64
	}{{"one", "kb-user-b", 1002}, {"two", "kb-user-a", 1001}} {
		if err := s.PutUser(domain.User{ID: identity.id, LoginName: identity.login}); err != nil {
			t.Fatal(err)
		}
		p, _ := s.Project(identity.project)
		p.OwnerID = identity.id
		if err := s.PutProject(p); err != nil {
			t.Fatal(err)
		}
	}
	w := doJSON(t, h, "POST", "/api/knowledge/bases/shared", `{"name":"通用知识"}`, nil)
	var base domain.KnowledgeBase
	json.Unmarshal(w.Body.Bytes(), &base)
	source := knowledge.HostedSourceID(base.ID)
	writeDoc(t, h, "", source, "公共检索")
	writeDoc(t, h, "two", knowledge.HostedSourceID("kb-two"), "其他项目")
	token, err := knowledge.SignProxyToken(knowledge.SessionSecret(), knowledge.ProxyClaims{ProjectID: "one", UserID: 1002, AgentID: "dev"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	session := newKnowledgeAgentToolSession(t, "one", AgentToolAssistant, false)
	session.UserID = 1002
	calls := map[string]func(string, string) (string, bool){
		"MainBot": func(name, args string) (string, bool) { return agentToolCall(t, h, session, name, args) },
		"SOP": func(name, args string) (string, bool) {
			w := doJSON(t, h, "POST", knowledge.ProxyPath, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`, map[string]string{"Authorization": "Bearer " + token})
			text, bad, protocol := rpcText(t, w)
			if protocol != "" {
				t.Fatal(protocol)
			}
			return text, bad
		},
	}
	for _, loaded := range []bool{false, true, false} {
		payload := fmt.Sprintf(`{"projectId":"one","baseId":%q,"enabled":%t}`, base.ID, loaded)
		if w := doJSON(t, h, "PUT", "/api/knowledge/bases/bindings", payload, nil); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		for name, call := range calls {
			t.Run(fmt.Sprintf("%s/loaded=%t", name, loaded), func(t *testing.T) {
				for _, tool := range []string{knowledge.ToolListSources, knowledge.ToolSearch} {
					text, bad := call(tool, `{"query":"公共检索","projectId":"two"}`)
					if bad || strings.Contains(text, source) != loaded || strings.Contains(text, knowledge.HostedSourceID("kb-two")) {
						t.Fatalf("%s visibility loaded=%t: %s", tool, loaded, text)
					}
					if loaded && (!strings.Contains(text, `"layer":"shared"`) || !strings.Contains(text, base.ID)) {
						t.Fatalf("origin: %s", text)
					}
				}
				for _, tool := range []string{knowledge.ToolFetch, knowledge.ToolGetSource} {
					args := fmt.Sprintf(`{"source":%q,"path":%q,"projectId":"two"}`, source, kbDocPath)
					text, bad := call(tool, args)
					if bad == loaded {
						t.Fatalf("%s loaded=%t bad=%t %s", tool, loaded, bad, text)
					}
					text, bad = call(tool, fmt.Sprintf(`{"source":%q,"path":"guides/deploy.md"}`, knowledge.HostedSourceID("kb-two")))
					if !bad {
						t.Fatalf("cross-project %s: %s", tool, text)
					}
				}
			})
		}
	}
	// User B's project/session (one) cannot select user A's project (two),
	// including explicit source filters and tools that would modify knowledge.
	for name, call := range calls {
		for _, tool := range []string{knowledge.ToolSearch, knowledge.ToolGrep, knowledge.ToolReportIssue} {
			args := `{"projectId":"two","query":"其他项目","pattern":"其他项目","sources":["SOURCE_A"],"source":"SOURCE_A","path":"guides/deploy.md","issue":"cross-project"}`
			args = strings.ReplaceAll(args, "SOURCE_A", knowledge.HostedSourceID("kb-two"))
			if text, bad := call(tool, args); !bad {
				t.Errorf("%s B accesses A using %s: %s", name, tool, text)
			}
		}
	}
	for name, endpoint := range map[string]string{"MainBot": "/internal/agent-tools/" + session.Token + "/mcp", "SOP": knowledge.ProxyPath} {
		for _, tool := range []string{"kb_write_document", "kb_retract_document", "kb_register_source"} {
			headers := map[string]string{}
			if name == "SOP" {
				headers["Authorization"] = "Bearer " + token
			}
			payload := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":{"projectId":"two","content":"poisoned"}}}`, tool)
			w := doJSON(t, h, "POST", endpoint, payload, headers)
			var result struct {
				Error  *json.RawMessage `json:"error"`
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Error == nil && !result.Result.IsError {
				t.Errorf("%s exposed write tool %s: %s", name, tool, w.Body.String())
			}
		}
	}
	// Even an invalid legacy binding must not expose another project's library.
	if err := s.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "one", BaseID: "kb-two", Permission: "read"}); err != nil {
		t.Fatal(err)
	}
	for name, call := range calls {
		if text, bad := call(knowledge.ToolListSources, `{}`); bad || strings.Contains(text, knowledge.HostedSourceID("kb-two")) {
			t.Fatalf("%s corrupt binding: %s", name, text)
		}
	}
}
