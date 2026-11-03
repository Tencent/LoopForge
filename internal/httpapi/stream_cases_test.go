package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

func TestStreamRunLargeDeltasCancellationAndMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	telemetry := make([]domain.TelemetryEvent, 100)
	logs := make([]domain.LogRecord, 100)
	for i := range telemetry {
		seq := int64(i + 1)
		telemetry[i] = domain.TelemetryEvent{ID: "telemetry", Sequence: seq, Message: "event"}
		logs[i] = domain.LogRecord{ID: "log", Sequence: seq, Body: "record"}
	}
	run := domain.Run{
		ID: "cvb-stream", WorkflowID: "generic-code", Status: "running", Revision: 7,
		Telemetry: telemetry, TelemetrySequence: 100, Logs: logs, LogSequence: 100,
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h := &streamHandlers{deps: &deps{repo: st, engine: e}}
	router := gin.New()
	router.GET("/runs/:id", h.streamRun)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/runs/cvb-stream?after=0&logsAfter=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(resp.Body)
		body <- string(raw)
	}()
	cancel()
	var raw string
	select {
	case raw = <-body:
	case <-time.After(10 * time.Second):
		t.Fatal("stream did not stop after request cancellation")
	}
	_ = resp.Body.Close()
	if !strings.Contains(raw, "event: run") || strings.Count(raw, `"sequence"`) == 0 {
		t.Fatalf("expected telemetry and log deltas, got %d sequences", strings.Count(raw, `"sequence"`))
	}

	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/runs/not-found", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing run status = %d", missing.Code)
	}
}

func TestStreamRunHeartbeat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutRun(domain.Run{ID: "cvb-heartbeat", WorkflowID: "generic-code", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h := &streamHandlers{deps: &deps{repo: st, engine: e}}
	router := gin.New()
	router.GET("/runs/:id", h.streamRun)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 17*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/runs/cvb-heartbeat", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(raw), ": heartbeat") {
		t.Fatalf("heartbeat missing from %q", raw)
	}
}

func TestStreamFilesystemResolutionBranches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", filepath.Join(home, "missing-override"))
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	bin := filepath.Join(home, ".local", "bin", "codebuddy")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveCodeBuddyBin(); err != nil || got != bin {
		t.Fatalf("fallback binary = %q, %v", got, err)
	}
	if !fileExecutable(bin) || fileExecutable(filepath.Dir(bin)) || fileExecutable(filepath.Join(home, "absent")) {
		t.Fatal("fileExecutable classification failed")
	}
	if err := os.Chmod(bin, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = resolveCodeBuddyBin()
	if got := clarifyRepoCacheRoot(); !strings.HasPrefix(got, home) {
		t.Fatalf("cache root = %q", got)
	}
	if got := sanitizeDirName(" .. ", "fallback"); got != "fallback" {
		t.Fatalf("sanitize fallback = %q", got)
	}
	if got := stripGitCLINoise("\x1b[31m fatal:  bad \x1b[0m"); got != "fatal: bad" {
		t.Fatalf("noise stripping = %q", got)
	}
	t.Setenv("HOME", " ")
	if got := clarifyRepoCacheRoot(); got != "/root/.agentflow/clarify-cache" {
		t.Fatalf("blank-home cache root = %q", got)
	}
	got, err := resolveCodeBuddyBin()
	if err == nil && got != "/usr/local/bin/codebuddy" && got != "/usr/bin/codebuddy" && got != "/root/.local/bin/codebuddy" {
		t.Fatalf("blank-home system binary = %q, %v", got, err)
	}
}

func TestStreamRepositorySourceAndLocks(t *testing.T) {
	plain := t.TempDir()
	repository := domain.Repository{ID: "r", LocalPath: plain}
	got, snapshot := clarifyRepositorySource(repository, "")
	if !snapshot || got.LocalPath != plain {
		t.Fatalf("plain source = %+v, snapshot=%v", got, snapshot)
	}
	repository.CloneURL = "https://example.invalid/repo.git"
	if _, snapshot := clarifyRepositorySource(repository, "main"); snapshot {
		t.Fatal("branched plain directory used as a snapshot")
	}
	repository.LocalPath = filepath.Join(plain, "missing")
	if _, snapshot := clarifyRepositorySource(repository, ""); snapshot {
		t.Fatal("missing local directory used as a snapshot")
	}

	h := &streamHandlers{}
	first := h.repoLock("same")
	if first != h.repoLock("same") || first == h.repoLock("other") {
		t.Fatal("repository mutexes are not stable per repository")
	}
	if statDir(filepath.Join(plain, "missing")) || !statDir(plain) {
		t.Fatal("statDir classification failed")
	}
}

func TestStreamPrepareRepositoryStaleDefaultCheckout(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	repository := domain.Repository{ID: "cvb-stale", Name: "stale", CloneURL: filepath.Join(home, "missing-source")}
	identityDir := sanitizeDirName(repository.Name, repository.ID)
	h := &streamHandlers{deps: &deps{repo: st}}
	// Create the same cache layout through the production hash expression.
	hashInput := repository.ID + "\x00master"
	identity := sha256.Sum256([]byte(hashInput))
	sum := fmt.Sprintf("%x", identity[:8])
	cache := filepath.Join(clarifyRepoCacheRoot(), identityDir+"-"+sum)
	if err := os.MkdirAll(filepath.Join(cache, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, analyzed, reason := h.prepareClarifyRepo(context.Background(), repository, "", 1, true)
	if !analyzed || dir != cache || reason != "" {
		t.Fatalf("stale default checkout = %q, %v, %q", dir, analyzed, reason)
	}
}

func TestStreamProtocolRemainingBranches(t *testing.T) {
	if got := clarifyStreamEvents("{"); got != nil {
		t.Fatalf("invalid stream event = %#v", got)
	}
	if got := clarifyStreamEvents(`{"type":"result"}`); got != nil {
		t.Fatalf("non-assistant stream event = %#v", got)
	}
	line := `{"type":"assistant","message":{"content":[` +
		`{"type":"thinking","thinking":"  useful thought  "},` +
		`{"type":"thinking","thinking":"{\"summary\":\"hidden\",\"questions\":[]}"},` +
		`{"type":"text","text":" visible text "},` +
		`{"type":"tool_use","name":"Grep","input":{"pattern":"needle"}}]}}`
	if got := clarifyStreamEvents(line); len(got) != 3 || got[2]["detail"] != "needle" {
		t.Fatalf("stream events = %#v", got)
	}
	if extractClarifyProtocolObject("{broken}") != nil {
		t.Fatal("broken protocol object accepted")
	}
	if got := clarifyToolDetail("none", json.RawMessage(`{"pattern":"p"}`)); got != "p" {
		t.Fatalf("pattern-only detail = %q", got)
	}
	if got := clarifyToolDetail(mainBotBrowserPrefix+"press", json.RawMessage(`{"ref":"r","text":"label"}`)); got != "r ← label" {
		t.Fatalf("browser ref detail = %q", got)
	}
	if got := clarifyToolDetail(mainBotBrowserPrefix+"press", json.RawMessage(`{"ref":"r"}`)); got != "r" {
		t.Fatalf("browser bare ref detail = %q", got)
	}

	protocol := `{"summary":"ok","questions":[]}`
	resultWithEventContent := `{"type":"result","content":[{"text":"` + strings.ReplaceAll(protocol, `"`, `\"`) + `"}]}`
	if value, _ := extractClarifyResultJSON(resultWithEventContent); value == nil {
		t.Fatal("result event content was not extracted")
	}
	assistantEnvelope := map[string]any{"type": "assistant", "message": map[string]any{
		"content": []any{map[string]any{"text": protocol}},
	}}
	if value, ok := unwrapStreamJSONEnvelope(assistantEnvelope); !ok || value == nil {
		t.Fatalf("assistant envelope = %#v, %v", value, ok)
	}
	for _, raw := range []string{
		"\nnot-json\n" + protocol,
		protocol + "\n\n" + protocol,
		`{"type":"result","result":"plain result"}`,
		`{"type":"result","result":{"type":"result","result":{"summary":"nested"}}}`,
		`{"message":{"content":[null,{"content":"` + strings.ReplaceAll(protocol, `"`, `\"`) + `"}]}}`,
		`{"message":{"content":[{"text":"` + strings.ReplaceAll(protocol, `"`, `\"`) + `"}]}}` + "\ntrailer",
		`{"content":[],"text":"` + strings.ReplaceAll(protocol, `"`, `\"`) + `"}`,
	} {
		if value, returned := extractJSONObject(raw); value == nil || returned == "" {
			t.Errorf("extractJSONObject(%q) = %#v, %q", raw, value, returned)
		}
	}
	if value, ok := unwrapStreamJSONEnvelope(map[string]any{"type": "result", "result": "without json"}); !ok || value == nil {
		t.Fatalf("non-JSON result envelope = %#v, %v", value, ok)
	}
	if value, ok := unwrapStreamJSONEnvelope(map[string]any{"type": "result", "result": "prefix {broken} suffix"}); !ok || value == nil {
		t.Fatalf("broken inner result envelope = %#v, %v", value, ok)
	}
	fallbackRaw := `{"type":"assistant","message":{"content":[{"text":"` + strings.ReplaceAll(protocol, `"`, `\"`) + `"}]}}` + "\n" + `{"other":1}`
	if value, raw := extractJSONObject(fallbackRaw); value == nil || raw == "" {
		t.Fatalf("fallback object = %#v, %q", value, raw)
	}
	brokenAssistant := map[string]any{"type": "assistant", "message": map[string]any{
		"content": []any{map[string]any{"text": "{broken}"}},
	}}
	if value, ok := unwrapStreamJSONEnvelope(brokenAssistant); !ok || value == nil {
		t.Fatalf("broken assistant envelope = %#v, %v", value, ok)
	}
	try := func(string) (any, bool) { return nil, false }
	if _, _, ok := extractClarifyJSONFromEvent(map[string]any{"content": []any{nil}}, try); ok {
		t.Fatal("nil content unexpectedly extracted")
	}
}

func TestStreamChatClarifyValidationBranches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "cvb-other-project", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRepository(domain.Repository{ID: "cvb-foreign", ProjectID: "cvb-other-project", Name: "Foreign"}); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: st}}
	router := gin.New()
	router.POST("/clarify", h.chatClarify)
	cases := []struct {
		name, body string
	}{
		{"malformed", "{"},
		{"missing project", `{"message":"x"}`},
		{"unknown project", `{"projectId":"does-not-exist","message":"x"}`},
		{"foreign repository", `{"projectId":"platform","repositoryId":"cvb-foreign","baseBranch":"main","message":"x"}`},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/clarify", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d: %s", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestStreamCancelAllAndSpecificJobs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	legacyCtx, legacyCancel := context.WithCancel(context.Background())
	composerCtx, composerCancel := context.WithCancel(context.Background())
	specificCtx, specificCancel := context.WithCancel(context.Background())
	h := &streamHandlers{
		clarifyCancels: map[string]context.CancelFunc{"legacy": legacyCancel},
		jobs: &clarifyHub{jobs: map[string]*clarifyJob{
			"composer:all": {issueID: "composer:all", cancel: composerCancel, done: make(chan struct{})},
			"composer:one": {issueID: "composer:one", cancel: specificCancel, done: make(chan struct{})},
		}},
	}
	router := gin.New()
	router.POST("/cancel", h.chatClarifyCancel)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/cancel?jobId=one", strings.NewReader("{}")))
	if rec.Code != http.StatusOK {
		t.Fatalf("specific cancel status = %d", rec.Code)
	}
	select {
	case <-specificCtx.Done():
	default:
		t.Fatal("specific composer job was not cancelled")
	}
	select {
	case <-legacyCtx.Done():
		t.Fatal("specific cancellation cancelled legacy jobs")
	default:
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/cancel", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel-all status = %d", rec.Code)
	}
	for name, ctx := range map[string]context.Context{"legacy": legacyCtx, "composer": composerCtx} {
		select {
		case <-ctx.Done():
		default:
			t.Errorf("%s context was not cancelled", name)
		}
	}
}

func TestStreamExecuteWithMixedRepositoryAvailability(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user := domain.User{ID: 881, LoginName: "cvb-mixed", ChineseName: "cvB mixed", CodeBuddyAPIKey: "cvb-test-key"}
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("AGENTFLOW_CLARIFY_SESSION_DIR", filepath.Join(root, "sessions"))
	bin := filepath.Join(root, "codebuddy")
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"result\":\"{\\\"summary\\\":\\\"ok\\\",\\\"ready\\\":true,\\\"questions\\\":[]}\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(root, "source")
	if err := os.Mkdir(local, 0o755); err != nil {
		t.Fatal(err)
	}
	repositories := []domain.Repository{
		{ID: "cvb-local", ProjectID: "platform", Name: "local", LocalPath: local},
		{ID: "cvb-inaccessible", ProjectID: "platform", Name: "inaccessible", LocalPath: filepath.Join(root, "missing")},
		{ID: "cvb-unconfigured", ProjectID: "platform", Name: "unconfigured"},
	}
	for _, repository := range repositories {
		if err := st.PutRepository(repository); err != nil {
			t.Fatal(err)
		}
	}
	h := &streamHandlers{deps: &deps{repo: st}}
	issue := domain.Issue{
		ProjectID: "platform", AssigneeUserID: user.ID, Title: "mixed repositories", Description: "test",
		RepositoryIDs: []string{"missing-repository", "cvb-local", "cvb-inaccessible", "cvb-unconfigured"},
	}
	events := make([]string, 0)
	var mu stdsync.Mutex
	emit := func(event string, _ any) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}
	request := chatClarifyRequest{}
	request.History = append(request.History, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "assistant", Content: strings.Repeat("history", 700)})
	parsed, _, info, err := h.executeClarify(context.Background(), issue, request, emit)
	if err != nil || parsed == nil || info["analyzed"] != false {
		t.Fatalf("mixed repository clarify = %#v, %#v, %v", parsed, info, err)
	}
	if len(events) == 0 {
		t.Fatal("clarification emitted no progress")
	}
}

func TestStreamExecuteEarlyErrorsAndMultiRepositoryPrompt(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user := domain.User{ID: 882, LoginName: "cvb-multi", ChineseName: "cvB multi", CodeBuddyAPIKey: "cvb-test-key"}
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: st}}
	if _, _, _, err := h.executeClarify(context.Background(), domain.Issue{
		ProjectID: "platform", AssigneeUserID: 999999,
	}, chatClarifyRequest{}, nil); err == nil {
		t.Fatal("invalid responsible user unexpectedly succeeded")
	}

	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("AGENTFLOW_CLARIFY_SESSION_DIR", filepath.Join(root, "sessions"))
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", filepath.Join(root, "missing-inventory"))
	if _, _, _, err := h.executeClarify(context.Background(), domain.Issue{
		ProjectID: "platform", AssigneeUserID: user.ID, Kind: "issue", Title: "bug", Description: "broken",
	}, chatClarifyRequest{}, nil); err == nil {
		t.Fatal("missing managed inventory unexpectedly succeeded")
	}
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	bin := filepath.Join(root, "codebuddy")
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"result\":\"{\\\"summary\\\":\\\"ok\\\",\\\"ready\\\":true,\\\"questions\\\":[]}\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cvb-multi-a", "cvb-multi-b"} {
		dir := filepath.Join(root, id)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := st.PutRepository(domain.Repository{
			ID: id, ProjectID: "platform", Name: "duplicate name", LocalPath: dir,
		}); err != nil {
			t.Fatal(err)
		}
	}
	issue := domain.Issue{
		ProjectID: "platform", AssigneeUserID: user.ID, Kind: "issue", Title: "bug", Description: "broken",
		RepositoryIDs: []string{"cvb-multi-a", "cvb-multi-b", "cvb-multi-missing"},
	}
	if parsed, _, _, err := h.executeClarify(context.Background(), issue, chatClarifyRequest{}, nil); err != nil || parsed == nil {
		t.Fatalf("multi repository clarify = %#v, %v", parsed, err)
	}
}

func TestStreamExecuteDirectorySessionAndTerminalErrors(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user := domain.User{ID: 883, LoginName: "cvb-errors", ChineseName: "cvB errors", CodeBuddyAPIKey: "cvb-test-key"}
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: st}}
	issue := domain.Issue{ProjectID: "platform", AssigneeUserID: user.ID, Title: "errors"}

	blockedBase := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(filepath.Dir(blockedBase), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedBase, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CLARIFY_SESSION_DIR", blockedBase)
	if _, _, _, err := h.executeClarify(context.Background(), issue, chatClarifyRequest{}, nil); err == nil {
		t.Fatal("blocked session base unexpectedly succeeded")
	}

	sessionBase := filepath.Join(t.TempDir(), "sessions")
	t.Setenv("AGENTFLOW_CLARIFY_SESSION_DIR", sessionBase)
	mcpSessions := filepath.Join(clarifyCLIHome(user.ID), "mcp-sessions")
	if err := os.MkdirAll(filepath.Dir(mcpSessions), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpSessions, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.executeClarify(context.Background(), issue, chatClarifyRequest{}, nil); err == nil {
		t.Fatal("blocked MCP session directory unexpectedly succeeded")
	}

	if err := os.Remove(mcpSessions); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "codebuddy")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' 'no terminal object'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	if _, _, _, err := h.executeClarify(context.Background(), issue, chatClarifyRequest{}, nil); err == nil {
		t.Fatal("missing terminal clarification unexpectedly succeeded")
	}

	for _, id := range []string{"cvb-ws-a", "cvb-ws-b"} {
		dir := filepath.Join(t.TempDir(), id)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := st.PutRepository(domain.Repository{ID: id, ProjectID: "platform", Name: id, LocalPath: dir}); err != nil {
			t.Fatal(err)
		}
	}
	blockedWorkspaceIssue := issue
	blockedWorkspaceIssue.ID = "cvb-blocked-workspace"
	blockedWorkspaceIssue.RepositoryIDs = []string{"cvb-ws-a", "cvb-ws-b"}
	blockedWorkspace := clarifyWorkspaceDir(user.ID, blockedWorkspaceIssue.ID)
	if err := os.MkdirAll(filepath.Dir(blockedWorkspace), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedWorkspace, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = h.executeClarify(context.Background(), blockedWorkspaceIssue, chatClarifyRequest{}, nil)

	blockedEmptyIssue := issue
	blockedEmptyIssue.ID = "cvb-blocked-empty"
	blockedEmpty := clarifyWorkspaceDir(user.ID, blockedEmptyIssue.ID+"-empty")
	if err := os.MkdirAll(filepath.Dir(blockedEmpty), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedEmpty, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = h.executeClarify(context.Background(), blockedEmptyIssue, chatClarifyRequest{}, nil)

	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.executeClarify(context.Background(), issue, chatClarifyRequest{}, nil); err == nil {
		t.Fatal("failed clarify command unexpectedly succeeded")
	}

	var nilHandler *streamHandlers
	nilHandler.rememberClarifySession("issue", "session", user.ID)
}

func TestStreamRunClarifyTurnOutputAndFailure(t *testing.T) {
	root := t.TempDir()
	success := filepath.Join(root, "success")
	successScript := `#!/bin/sh
printf '%s\n' 'plain output'
printf '%s\n' '{"type":"assistant","session_id":"captured-session","message":{"content":[{"type":"thinking","thinking":"working"}]}}'
`
	if err := os.WriteFile(success, []byte(successScript), 0o755); err != nil {
		t.Fatal(err)
	}
	var emitted []any
	h := &streamHandlers{}
	_, raw, captured, err := h.runClarifyTurn(
		context.Background(), root, success, "claude-sonnet-4", "prompt", "run",
		"initial", "", false, runtime.RunCredentials{CodeBuddy: runtime.CodeBuddyCredential{APIKey: "cvb-test-key"}},
		func(_ string, payload any) { emitted = append(emitted, payload) }, clarifyTurnOpts{Native: true},
	)
	if err != nil || !strings.Contains(raw, "plain output") || captured != "captured-session" || len(emitted) == 0 {
		t.Fatalf("successful turn = %q, %q, %#v, %v", raw, captured, emitted, err)
	}

	failure := filepath.Join(root, "failure")
	if err := os.WriteFile(failure, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.runClarifyTurn(
		context.Background(), root, failure, "claude-sonnet-4", "prompt", "run",
		"session", "", false, runtime.RunCredentials{CodeBuddy: runtime.CodeBuddyCredential{APIKey: "cvb-test-key"}}, nil, clarifyTurnOpts{Native: true},
	); err == nil {
		t.Fatal("failed command unexpectedly succeeded")
	}
}
