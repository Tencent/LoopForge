package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func TestKnowledgeHTTPBranches(t *testing.T) {
	_, h, _, _ := newKnowledgeTestServer(t)
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{http.MethodPost, "/api/knowledge/check", `{}`, 200},
		{http.MethodGet, "/api/knowledge/sources?projectId=p1", "", 200},
		{http.MethodPost, "/api/knowledge/search", `{"query":"x","projectId":"p1"}`, 200},
		{http.MethodPost, "/api/knowledge/search", `{`, 400},
		{http.MethodGet, "/api/knowledge/document?projectId=p1&source=a&path=x.md&start=1&end=2", "", 200},
		{http.MethodGet, "/api/knowledge/submissions?projectId=p1", "", 200},
		{http.MethodGet, "/api/knowledge/submissions/missing", "", 404},
		{http.MethodPut, "/api/knowledge/submissions/missing", `{}`, 404},
		{http.MethodDelete, "/api/knowledge/submissions/missing", "", 404},
		{http.MethodPost, "/api/knowledge/submissions/missing/reject", `{}`, 404},
	} {
		rec := doJSON(t, h, tc.method, tc.path, tc.body, nil)
		if rec.Code != tc.code {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.code, rec.Body.String())
		}
	}
}

func TestGateMCPBinaryConfiguredArtifactAndErrors(t *testing.T) {
	run := func(query string) (int, string) {
		c, rec := cvAContext(t, http.MethodGet, "/api/gate?"+query, "", nil)
		serveGateMCPBinary(c)
		return rec.Code, rec.Body.String()
	}
	if code, _ := run("os=windows&arch=amd64"); code != http.StatusBadRequest {
		t.Fatalf("unsupported OS=%d", code)
	}
	if code, _ := run("os=linux&arch=mips"); code != http.StatusBadRequest {
		t.Fatalf("unsupported arch=%d", code)
	}
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", filepath.Join(t.TempDir(), "{os}-{arch}"))
	if code, _ := run("os=linux&arch=amd64"); code != http.StatusNotFound {
		t.Fatalf("missing artifact=%d", code)
	}
	path := filepath.Join(t.TempDir(), "gate-linux-amd64")
	if err := os.WriteFile(path, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", strings.ReplaceAll(path, "linux-amd64", "{os}-{arch}"))
	code, body := run("os=linux&arch=amd64")
	if code != http.StatusOK || body != "binary" {
		t.Fatalf("artifact=%d %q", code, body)
	}
}

func TestMCPMarketFileAndBotIdentityErrors(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutProject(domain.Project{ID: "mcp-p", Name: "MCP"})
	market := &marketHandlers{deps: &deps{repo: st}}
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"scope":"bad","server":{"id":"x"}}`, 400},
		{`{"scope":"project","server":{"id":"x"}}`, 400},
		{`{"scope":"project","projectId":"missing","server":{"id":"x"}}`, 404},
		{`{"scope":"project","projectId":"mcp-p","server":{}}`, 400},
		{`{"scope":"personal","server":{"id":"x"}}`, 401},
	} {
		c, rec := cvAContext(t, http.MethodPost, "/api/mcp-servers/custom", tc.body, nil)
		market.mcpCustomInstall(c)
		if rec.Code != tc.code {
			t.Errorf("custom %s = %d: %s", tc.body, rec.Code, rec.Body.String())
		}
	}
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "market", Name: "market", MarketID: "70", Scope: "project", ProjectID: "mcp-p", Transport: "http", URL: "https://example.test/mcp"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "custom", Name: "custom", Scope: "project", ProjectID: "mcp-p", Transport: "http", URL: "https://example.test/custom"}); err != nil {
		t.Fatal(err)
	}
	mcp := &mcpHandlers{deps: &deps{repo: st}}
	for _, tc := range []struct {
		id, path, method string
		code             int
	}{
		{"missing", "x", http.MethodGet, 404},
		{"market", "x", http.MethodGet, 400},
		{"custom", "../x", http.MethodGet, 400},
		{"custom", "missing.txt", http.MethodGet, 404},
		{"custom", "x", http.MethodPatch, 405},
	} {
		c, rec := cvAContext(t, tc.method, "/api/mcp/"+tc.id+"/"+tc.path, "", nil)
		c.Params = ginParams(tc.id, tc.path)
		mcp.mcpFile(c)
		if rec.Code != tc.code {
			t.Errorf("mcp file %s = %d: %s", tc.path, rec.Code, rec.Body.String())
		}
	}

	_ = st.PutUser(domain.User{LoginName: "bot-user"})
	user, _ := st.UserByLoginName("bot-user")
	ident := &botIdentityHandlers{deps: &deps{repo: st}, publicURL: "https://example.com"}
	if botIdentityCommand("@bot /bind abc") != "/bind abc" {
		t.Fatal("bot command")
	}
	for _, tc := range []struct {
		user         *domain.User
		body, origin string
		code         int
	}{
		{nil, `{}`, "", 401},
		{&user, `{"token":"short"}`, "", 400},
	} {
		c, rec := cvAContext(t, http.MethodPost, "/api/bot/claim", tc.body, tc.user)
		c.Request.Host = "host"
		c.Request.Header.Set("Origin", tc.origin)
		ident.binding(c)
		if rec.Code != tc.code {
			t.Errorf("binding=%d %s", rec.Code, rec.Body.String())
		}
	}
	c, rec := cvAContext(t, http.MethodGet, "/api/bot/identities", "", &user)
	ident.list(c)
	if rec.Code != 200 {
		t.Fatalf("identity list=%d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodDelete, "/api/bot/identities/missing", "", &user)
	c.Params = ginParams("missing", "")
	ident.remove(c)
	if rec.Code != 403 {
		t.Fatalf("cross origin remove=%d", rec.Code)
	}
	if ok, msg, err := ident.message(domain.NotificationBot{BotID: "b"}, domain.BotInteraction{SenderID: "s", Content: "hello"}); !ok || err != nil || !strings.Contains(msg, "关联") {
		t.Fatalf("unbound message=%v %q %v", ok, msg, err)
	}
	ident.publicURL = "bad"
	if ok, msg, _ := ident.message(domain.NotificationBot{BotID: "b"}, domain.BotInteraction{SenderID: "s2", Content: "hello"}); !ok || !strings.Contains(msg, "尚未配置") {
		t.Fatalf("bad public URL=%v %q", ok, msg)
	}
}

func ginParams(id, path string) []gin.Param {
	out := []gin.Param{{Key: "id", Value: id}}
	if path != "" {
		out = append(out, gin.Param{Key: "path", Value: path})
	}
	return out
}

func TestInsightsValidationAndHTTP(t *testing.T) {
	now := time.Now().UTC()
	cost := 1.0
	base := domain.InsightExecution{
		ID: "id", Type: "workflow_execution", ExternalExecutionID: "external",
		Workflow:  domain.InsightWorkflow{ID: "wf", Name: "Workflow"},
		StartedAt: now.Add(-time.Minute), FinishedAt: now, Status: "succeeded",
		Usage: []domain.InsightUsage{{Provider: "p", Model: "m", InputTokens: 10, OutputTokens: 5, CostStatus: "reported", CostUSD: &cost}},
	}
	if reason := validateInsightExecution(&base, now); reason != "" {
		t.Fatalf("valid insight: %s", reason)
	}
	cases := []func(*domain.InsightExecution){
		func(v *domain.InsightExecution) { v.Type = "bad" },
		func(v *domain.InsightExecution) { v.ID = "" },
		func(v *domain.InsightExecution) { v.Status = "running" },
		func(v *domain.InsightExecution) { v.FinishedAt = v.StartedAt.Add(-time.Second) },
		func(v *domain.InsightExecution) { x := int64(-1); v.ExecutionDurationMs = &x },
		func(v *domain.InsightExecution) { v.URL = "ftp://bad" },
		func(v *domain.InsightExecution) { v.Usage = nil },
		func(v *domain.InsightExecution) { v.Usage[0].Provider = "" },
		func(v *domain.InsightExecution) { v.Usage = append(v.Usage, v.Usage[0]) },
		func(v *domain.InsightExecution) { v.Usage[0].InputTokens = -1 },
		func(v *domain.InsightExecution) { v.Usage[0].CacheReadTokens = 11 },
		func(v *domain.InsightExecution) { v.Usage[0].CostStatus = "unknown" },
		func(v *domain.InsightExecution) { v.Usage[0].CostStatus = "bad" },
	}
	for i, mutate := range cases {
		copy := base
		copy.Usage = append([]domain.InsightUsage{}, base.Usage...)
		mutate(&copy)
		if reason := validateInsightExecution(&copy, now); reason == "" {
			t.Errorf("invalid insight case %d accepted", i)
		}
	}

	st := cvAStore(t)
	_ = st.PutUser(domain.User{LoginName: "owner"})
	owner, _ := st.UserByLoginName("owner")
	_ = st.PutProject(domain.Project{ID: "insight-p", Name: "P", OwnerID: owner.ID})
	h := &insightsHandlers{deps: &deps{repo: st}}
	c, rec := cvAContext(t, http.MethodGet, "/api/insights/sources?projectId=insight-p", "", nil)
	h.sources(c)
	if rec.Code != 401 {
		t.Fatalf("anonymous sources=%d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodPost, "/api/insights/sources", `{"projectId":"insight-p","name":""}`, &owner)
	h.sources(c)
	if rec.Code != 400 {
		t.Fatalf("blank source=%d %s", rec.Code, rec.Body.String())
	}
	c, rec = cvAContext(t, http.MethodPost, "/api/insights/v1/ingest", `{}`, nil)
	h.ingest(c)
	if rec.Code != 401 {
		t.Fatalf("anonymous ingest=%d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodDelete, "/api/insights/sources/missing", "", &owner)
	c.Params = ginParams("missing", "")
	h.revokeSource(c)
	if rec.Code != 404 {
		t.Fatalf("missing revoke=%d", rec.Code)
	}
	raw, _ := json.Marshal(base)
	if !strings.Contains(string(raw), "workflow_execution") {
		t.Fatal("insight marshal")
	}
}

func TestBrowserHandlerAccessBranches(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutUser(domain.User{LoginName: "owner"})
	owner, _ := st.UserByLoginName("owner")
	_ = st.PutUser(domain.User{LoginName: "viewer"})
	viewer, _ := st.UserByLoginName("viewer")
	project := domain.Project{ID: "browser-p", Name: "P", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: viewer.ID, Role: "viewer"}}}
	_ = st.PutProject(project)
	run := domain.Run{ID: "browser-run", ExecutionContext: domain.TelemetryContext{ProjectID: project.ID}}
	_ = st.PutRun(run)
	h := &browserHandlers{deps: &deps{repo: st}}
	c, rec := cvAContext(t, http.MethodPost, "/api/runs/missing/browser", `{}`, &owner)
	c.Params = ginParams("missing", "")
	h.browser(c)
	if rec.Code != 404 {
		t.Fatalf("missing run=%d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodPost, "/api/runs/browser-run/browser", `{}`, nil)
	c.Params = ginParams(run.ID, "")
	h.browser(c)
	if rec.Code != 401 {
		t.Fatalf("anonymous browser=%d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodPost, "/api/runs/browser-run/browser", `{"operation":"navigate"}`, &viewer)
	c.Params = ginParams(run.ID, "")
	h.browser(c)
	if rec.Code != 403 {
		t.Fatalf("viewer browser=%d", rec.Code)
	}
	if h.canControl(httptest.NewRequest("GET", "/", nil), run, viewer.ID) || !h.canControl(httptest.NewRequest("GET", "/", nil), run, owner.ID) {
		t.Fatal("browser control policy")
	}
	if !strings.Contains(string(browserReason("why")), "why") {
		t.Fatal("browser reason")
	}
	h.touchWorkspace(run.ID)
	h.touchWorkspace(run.ID)
	_ = context.Background()
}

func TestBrowserStreamPublishReadAndDeniedExecute(t *testing.T) {
	streams := make(chan *browserStream, 1)
	done := make(chan struct{})
	router := gin.New()
	router.GET("/ws", func(c *gin.Context) {
		ws, err := browserStreamUpgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		s := &browserStream{ws: ws}
		streams <- s
		<-done
	})
	server := httptest.NewServer(router)
	defer server.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	defer close(done)
	s := <-streams
	frame := base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff})
	payload, _ := json.Marshal(domain.BrowserState{Available: true, URL: "https://example.test", Seq: 1, Frame: frame})
	available, err := s.publish(payload)
	if err != nil || !available {
		t.Fatalf("publish=%v %v", available, err)
	}
	kind, raw, err := ws.ReadMessage()
	if err != nil || kind != websocket.TextMessage || !strings.Contains(string(raw), `"type":"state"`) {
		t.Fatalf("state message kind=%d raw=%s err=%v", kind, raw, err)
	}
	kind, raw, err = ws.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || len(raw) != 3 {
		t.Fatalf("frame kind=%d bytes=%d err=%v", kind, len(raw), err)
	}
	if available, err = s.publish(json.RawMessage(`not-json`)); err != nil || available {
		t.Fatalf("invalid publish=%v %v", available, err)
	}
	if err := s.send(browserStreamMessage{Type: "error", Error: "denied"}); err != nil {
		t.Fatal(err)
	}
	_, raw, _ = ws.ReadMessage()
	if !strings.Contains(string(raw), "denied") {
		t.Fatalf("send=%s", raw)
	}
	commands := make(chan domain.BrowserRequest, 2)
	commands <- domain.BrowserRequest{Operation: "navigate"}
	close(commands)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.execute(ctx, cancel, commands, false)
	_, raw, _ = ws.ReadMessage()
	if !strings.Contains(string(raw), "无权") {
		t.Fatalf("denied execute=%s", raw)
	}
}

func TestBrowserStreamReadRefreshAndCanceledLoops(t *testing.T) {
	streams := make(chan *browserStream, 1)
	commands := make(chan domain.BrowserRequest, 4)
	done := make(chan struct{})
	router := gin.New()
	router.GET("/read", func(c *gin.Context) {
		ws, err := browserStreamUpgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		s := &browserStream{ws: ws}
		streams <- s
		ctx, cancel := context.WithCancel(context.Background())
		go s.read(cancel, commands)
		<-ctx.Done()
		_ = ws.Close()
		close(done)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/read", nil)
	if err != nil {
		t.Fatal(err)
	}
	s := <-streams
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte("ignored")); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, []byte("{bad")); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteJSON(domain.BrowserRequest{Operation: "frame"}); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteJSON(domain.BrowserRequest{Operation: "navigate", URL: "https://example.test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case command := <-commands:
		if command.Operation != "navigate" {
			t.Fatalf("command=%+v", command)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream command timeout")
	}
	_ = ws.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not stop")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.frames(ctx, func() {})
	s.ping(ctx, func() {})

	st := cvAStore(t)
	run := domain.Run{ID: "cva-refresh-run", Status: "running"}
	_ = st.PutRun(run)
	s.h = &browserHandlers{deps: &deps{repo: st}}
	s.runID = run.ID
	s.run = domain.Run{ID: run.ID, Status: "old"}
	if refreshed := s.currentRun(true); refreshed.Status != "running" {
		t.Fatalf("refreshed=%+v", refreshed)
	}
}
