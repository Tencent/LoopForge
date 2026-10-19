package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/preview"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestPreviewAPIAndPathGateway(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	root := t.TempDir()
	eng := engine.New(store, runtime.NewManager(filepath.Join(root, "workspaces")))
	svc := preview.NewService(store, preview.NewDBStore(store), preview.Config{
		PublicScheme: "http", FrameAncestors: "http://studio.test", MaxFiles: 20,
		MaxCompressed: 1 << 20, MaxExpanded: 1 << 20, DefaultRetention: time.Hour,
		FinalizationTimeout: time.Minute, SessionTTL: time.Hour,
	})
	eng.SetPreview(svc)
	run := domain.Run{
		ID: "run-preview-api", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist", SPAFallback: true}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := eng.RunWorkspace(run.ID)
	if err := os.MkdirAll(filepath.Join(workspace, "dist", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "index.html"), []byte("<html><head></head><body>hello<script src=\"/assets/app.js\"></script></body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !svc.BeginFinalization(run) {
		t.Fatal("finalization did not start")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p, ok := store.PreviewByRun(run.ID)
		if ok && p.Status == domain.PreviewReady {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	h := New(store, eng, tracker.NewService(), "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/runs/run-preview-api/preview", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("preview api %d %s", rec.Code, rec.Body.String())
	}

	sessionReq := httptest.NewRequest(http.MethodPost, "/api/runs/run-preview-api/preview/session", bytes.NewReader(nil))
	sessionReq.Host = "studio.test"
	sessionRec := httptest.NewRecorder()
	h.ServeHTTP(sessionRec, sessionReq)
	if sessionRec.Code != 200 {
		t.Fatalf("session %d %s", sessionRec.Code, sessionRec.Body.String())
	}
	var session struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(sessionRec.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(session.URL, "/preview/") || !strings.HasSuffix(session.URL, "/") {
		t.Fatalf("expected path preview URL, got %s", session.URL)
	}
	u, err := url.Parse(session.URL)
	if err != nil {
		t.Fatal(err)
	}
	token, _, ok := preview.ParsePreviewPath(u.Path)
	if !ok {
		t.Fatalf("could not parse token from %s", session.URL)
	}
	p, ok := store.PreviewByRun(run.ID)
	if !ok || p.ContentSHA256 == "" {
		t.Fatal("ready preview missing content hash")
	}
	if _, _, ok := store.PreviewBlob(p.ContentSHA256); !ok {
		t.Fatal("preview blob was not stored in the database")
	}

	pageReq := httptest.NewRequest(http.MethodGet, "/preview/"+token+"/", nil)
	pageReq.Host = "studio.test"
	pageReq.Header.Set("Accept", "text/html")
	pageRec := httptest.NewRecorder()
	h.ServeHTTP(pageRec, pageReq)
	if pageRec.Code != 200 || !strings.Contains(pageRec.Body.String(), "hello") {
		t.Fatalf("preview page %d %s", pageRec.Code, pageRec.Body.String())
	}
	if !strings.Contains(pageRec.Body.String(), `/preview/`+token+`/assets/app.js`) {
		t.Fatalf("path hosting should prefix root-relative assets: %s", pageRec.Body.String())
	}
	if strings.Contains(pageRec.Body.String(), `__AGENTFLOW_PREVIEW_BACKEND__`) {
		t.Fatalf("empty backend must not inject a backend global: %s", pageRec.Body.String())
	}
	if !strings.Contains(pageRec.Body.String(), `var root = "/preview/`+token+`";`) {
		t.Fatalf("preview page must keep client routes under the session path: %s", pageRec.Body.String())
	}
	if !strings.Contains(pageRec.Body.String(), `var routed = false;`) {
		t.Fatalf("a bundle without a router must keep the preview path from the start: %s", pageRec.Body.String())
	}
	routeReq := httptest.NewRequest(http.MethodGet, "/preview/"+token+"/skills/42", nil)
	routeReq.Host = "studio.test"
	routeReq.Header.Set("Accept", "text/html")
	routeRec := httptest.NewRecorder()
	h.ServeHTTP(routeRec, routeReq)
	if routeRec.Code != 200 || !strings.Contains(routeRec.Body.String(), "hello") || !strings.Contains(routeRec.Body.String(), `data-af-preview-history`) {
		t.Fatalf("refreshing a client route must serve the entry with the history shim: %d %s", routeRec.Code, routeRec.Body.String())
	}
	if csp := pageRec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "allow-same-origin") || !strings.Contains(csp, "frame-ancestors") || !strings.Contains(csp, "connect-src 'none'") {
		t.Fatalf("csp %q", csp)
	}
	if got := pageRec.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Fatalf("referrer %q", got)
	}
	strayReq := httptest.NewRequest(http.MethodGet, "/login?next=%2Fskills", nil)
	strayReq.Host = "studio.test"
	strayReq.Header.Set("Sec-Fetch-Mode", "navigate")
	strayReq.Header.Set("Sec-Fetch-Site", "same-origin")
	strayReq.Header.Set("Referer", "http://studio.test/preview/"+token+"/skills")
	strayRec := httptest.NewRecorder()
	h.ServeHTTP(strayRec, strayReq)
	if strayRec.Code != http.StatusSeeOther || strayRec.Header().Get("Location") != "/preview/"+token+"/login?next=%2Fskills" {
		t.Fatalf("navigation leaving the preview must return under its session path: %d %q", strayRec.Code, strayRec.Header().Get("Location"))
	}
	homeReq := httptest.NewRequest(http.MethodGet, "/", nil)
	homeReq.Host = "studio.test"
	homeReq.Header.Set("Sec-Fetch-Mode", "navigate")
	homeReq.Header.Set("Sec-Fetch-Site", "same-origin")
	homeReq.Header.Set("Referer", "http://studio.test/#/runs/run-preview-api")
	homeRec := httptest.NewRecorder()
	h.ServeHTTP(homeRec, homeReq)
	if homeRec.Code == http.StatusSeeOther || homeRec.Header().Get("Location") != "" {
		t.Fatalf("platform navigation must not be redirected: %d %q", homeRec.Code, homeRec.Header().Get("Location"))
	}

	assetReq := httptest.NewRequest(http.MethodGet, "/preview/"+token+"/assets/app.js", nil)
	assetReq.Host = "studio.test"
	assetRec := httptest.NewRecorder()
	h.ServeHTTP(assetRec, assetReq)
	if assetRec.Code != 200 || !strings.Contains(assetRec.Body.String(), "console.log") {
		t.Fatalf("asset %d %s", assetRec.Code, assetRec.Body.String())
	}

	guessReq := httptest.NewRequest(http.MethodGet, "/preview/deadbeefdeadbeefdeadbeefdeadbeef/", nil)
	guessReq.Host = "studio.test"
	guessRec := httptest.NewRecorder()
	h.ServeHTTP(guessRec, guessReq)
	if guessRec.Code == 200 {
		t.Fatal("guessed token must not serve preview")
	}

	badReq := httptest.NewRequest(http.MethodPost, "/api/runs/run-preview-api/preview/session", strings.NewReader(`{"backendUrl":"javascript:alert(1)"}`))
	badReq.Host = "studio.test"
	badReq.Header.Set("Content-Type", "application/json")
	badRec := httptest.NewRecorder()
	h.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid backend %d %s", badRec.Code, badRec.Body.String())
	}

	backendBody, _ := json.Marshal(map[string]string{"backendUrl": "https://api.example.com/v1"})
	backendReq := httptest.NewRequest(http.MethodPost, "/api/runs/run-preview-api/preview/session", bytes.NewReader(backendBody))
	backendReq.Host = "studio.test"
	backendReq.Header.Set("Content-Type", "application/json")
	backendRec := httptest.NewRecorder()
	h.ServeHTTP(backendRec, backendReq)
	if backendRec.Code != 200 {
		t.Fatalf("backend session %d %s", backendRec.Code, backendRec.Body.String())
	}
	var backendSession struct {
		URL        string `json:"url"`
		BackendURL string `json:"backendUrl"`
	}
	if err := json.Unmarshal(backendRec.Body.Bytes(), &backendSession); err != nil {
		t.Fatal(err)
	}
	if backendSession.BackendURL != "https://api.example.com/v1" {
		t.Fatalf("backendUrl %q", backendSession.BackendURL)
	}
	backendToken, _, ok := preview.ParsePreviewPath(mustParseURL(t, backendSession.URL).Path)
	if !ok {
		t.Fatalf("backend session url %s", backendSession.URL)
	}
	backedReq := httptest.NewRequest(http.MethodGet, "/preview/"+backendToken+"/", nil)
	backedReq.Host = "studio.test"
	backedReq.Header.Set("Accept", "text/html")
	backedRec := httptest.NewRecorder()
	h.ServeHTTP(backedRec, backedReq)
	if backedRec.Code != 200 || !strings.Contains(backedRec.Body.String(), `window.__AGENTFLOW_PREVIEW_BACKEND__="https://api.example.com/v1"`) {
		t.Fatalf("backend inject %d %s", backedRec.Code, backedRec.Body.String())
	}
	if !strings.Contains(backedRec.Body.String(), `window.fetch = function`) || !strings.Contains(backedRec.Body.String(), `XMLHttpRequest.prototype.open`) || !strings.Contains(backedRec.Body.String(), `navigator.sendBeacon`) {
		t.Fatalf("backend intercept missing: %s", backedRec.Body.String())
	}
	if csp := backedRec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self' https://api.example.com") || strings.Contains(csp, "connect-src 'none'") {
		t.Fatalf("backend csp %q", csp)
	}

	// Test persisting backend URL via PUT /api/runs/:id/preview/backend
	persistBody, _ := json.Marshal(map[string]string{"backendUrl": "https://persisted-api.example.com"})
	persistReq := httptest.NewRequest(http.MethodPut, "/api/runs/run-preview-api/preview/backend", bytes.NewReader(persistBody))
	persistReq.Host = "studio.test"
	persistReq.Header.Set("Content-Type", "application/json")
	persistRec := httptest.NewRecorder()
	h.ServeHTTP(persistRec, persistReq)
	if persistRec.Code != 200 {
		t.Fatalf("persist backend failed %d %s", persistRec.Code, persistRec.Body.String())
	}
	var persistResp struct {
		Preview struct {
			BackendURL string `json:"backendUrl"`
		} `json:"preview"`
		Session struct {
			BackendURL string `json:"backendUrl"`
		} `json:"session"`
	}
	if err := json.Unmarshal(persistRec.Body.Bytes(), &persistResp); err != nil {
		t.Fatal(err)
	}
	if persistResp.Preview.BackendURL != "https://persisted-api.example.com" {
		t.Fatalf("expected preview backendUrl https://persisted-api.example.com, got %q", persistResp.Preview.BackendURL)
	}
	if persistResp.Session.BackendURL != "https://persisted-api.example.com" {
		t.Fatalf("expected session backendUrl https://persisted-api.example.com, got %q", persistResp.Session.BackendURL)
	}

	// Verify GET /api/runs/:id/preview returns the persisted backendUrl
	getReq := httptest.NewRequest(http.MethodGet, "/api/runs/run-preview-api/preview", nil)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	if getRec.Code != 200 {
		t.Fatalf("get preview %d %s", getRec.Code, getRec.Body.String())
	}
	var getResp struct {
		BackendURL string `json:"backendUrl"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatal(err)
	}
	if getResp.BackendURL != "https://persisted-api.example.com" {
		t.Fatalf("expected persisted backendUrl from GET, got %q", getResp.BackendURL)
	}

	// Simulate another person visiting: POST /api/runs/:id/preview/session without backendUrl
	otherReq := httptest.NewRequest(http.MethodPost, "/api/runs/run-preview-api/preview/session", bytes.NewReader(nil))
	otherReq.Host = "studio.test"
	otherRec := httptest.NewRecorder()
	h.ServeHTTP(otherRec, otherReq)
	if otherRec.Code != 200 {
		t.Fatalf("other session %d %s", otherRec.Code, otherRec.Body.String())
	}
	var otherSession struct {
		URL        string `json:"url"`
		BackendURL string `json:"backendUrl"`
	}
	if err := json.Unmarshal(otherRec.Body.Bytes(), &otherSession); err != nil {
		t.Fatal(err)
	}
	if otherSession.BackendURL != "https://persisted-api.example.com" {
		t.Fatalf("other visitor must receive persisted backendUrl, got %q", otherSession.BackendURL)
	}
	otherToken, _, ok := preview.ParsePreviewPath(mustParseURL(t, otherSession.URL).Path)
	if !ok {
		t.Fatalf("parse token failed from %s", otherSession.URL)
	}
	otherPageReq := httptest.NewRequest(http.MethodGet, "/preview/"+otherToken+"/", nil)
	otherPageReq.Host = "studio.test"
	otherPageReq.Header.Set("Accept", "text/html")
	otherPageRec := httptest.NewRecorder()
	h.ServeHTTP(otherPageRec, otherPageReq)
	if otherPageRec.Code != 200 || !strings.Contains(otherPageRec.Body.String(), `window.__AGENTFLOW_PREVIEW_BACKEND__="https://persisted-api.example.com"`) {
		t.Fatalf("other visitor html must contain injected persisted backend: %s", otherPageRec.Body.String())
	}

	// Test clearing backend URL
	clearBody, _ := json.Marshal(map[string]string{"backendUrl": ""})
	clearReq := httptest.NewRequest(http.MethodPut, "/api/runs/run-preview-api/preview/backend", bytes.NewReader(clearBody))
	clearReq.Host = "studio.test"
	clearReq.Header.Set("Content-Type", "application/json")
	clearRec := httptest.NewRecorder()
	h.ServeHTTP(clearRec, clearReq)
	if clearRec.Code != 200 {
		t.Fatalf("clear backend %d %s", clearRec.Code, clearRec.Body.String())
	}
	pAfterClear, ok := store.PreviewByRun(run.ID)
	if !ok || pAfterClear.BackendURL != "" {
		t.Fatalf("expected backendUrl to be cleared, got %q", pAfterClear.BackendURL)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
