package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func TestWorkspaceFilesystemAndGitBranches(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "binary"), []byte{'a', 0, 'b'}, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "src", "a.txt"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	path, rel, err := resolveWorkspacePath(root, "src/a.txt", false)
	if err != nil || rel != "src/a.txt" || path == "" {
		t.Fatalf("resolve = %q %q %v", path, rel, err)
	}
	for _, tc := range []struct {
		raw string
		dir bool
	}{{"../x", false}, {".env", false}, {"link", false}, {"src/a.txt", true}, {"src", false}, {"missing", false}} {
		if _, _, err := resolveWorkspacePath(root, tc.raw, tc.dir); err == nil {
			t.Errorf("resolveWorkspacePath(%q,%v) unexpectedly succeeded", tc.raw, tc.dir)
		}
	}
	body, size, err := readWorkspaceFile(root, "src/a.txt")
	if err != nil || body != "hello" || size != 5 {
		t.Fatalf("read file: %q %d %v", body, size, err)
	}
	if _, _, err := readWorkspaceFile(root, "src/binary"); err == nil {
		t.Fatal("binary preview unexpectedly succeeded")
	}
	entries, truncated, err := readWorkspaceTree(root, "")
	if err != nil || truncated || len(entries) != 3 {
		t.Fatalf("tree: %+v %v %v", entries, truncated, err)
	}
	for _, sensitive := range []string{".git/config", ".agentflow/runtime/x", ".ssh/x", ".env.local", "x/id_rsa"} {
		if !workspacePathSensitive(sensitive) {
			t.Errorf("%q not sensitive", sensitive)
		}
	}
	if workspacePathSensitive(".env.example") || workspacePathSensitive("src/a.txt") {
		t.Fatal("safe path marked sensitive")
	}
	for _, raw := range []string{"", ".", "workspace", " repo "} {
		_ = workspaceGitRepositoryQuery(raw)
		_ = workspaceGitRepositoryName(raw)
	}
	for _, raw := range []string{"abcdef0", strings.Repeat("a", 64)} {
		if _, ok := safeGitObjectID(raw); !ok {
			t.Errorf("safe id rejected: %q", raw)
		}
	}
	for _, raw := range []string{"short", "zzzzzzz", strings.Repeat("a", 65)} {
		if _, ok := safeGitObjectID(raw); ok {
			t.Errorf("unsafe id accepted: %q", raw)
		}
	}
	if _, err := workspaceLogicalPath("../x"); err == nil {
		t.Fatal("unsafe logical path accepted")
	}

	if out, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	_ = exec.Command("git", "-C", root, "config", "user.email", "cvb@example.test").Run()
	_ = exec.Command("git", "-C", root, "config", "user.name", "cvB").Run()
	_ = exec.Command("git", "-C", root, "add", "src/a.txt").Run()
	if out, err := exec.Command("git", "-C", root, "commit", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.txt"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{}
	changes, message := collectWorkspaceChanges(context.Background(), root, run)
	if message != "" || len(changes) == 0 {
		t.Fatalf("changes: %+v %q", changes, message)
	}
	diff, clipped, err := readWorkspaceDiff(context.Background(), root, run, "src/a.txt")
	if err != nil || clipped || !strings.Contains(diff, "changed") {
		t.Fatalf("diff: %q %v %v", diff, clipped, err)
	}
	base, clipped, err := readWorkspaceBaseFile(context.Background(), root, run, "src/a.txt")
	if err != nil || clipped || base != "hello" {
		t.Fatalf("base: %q %v %v", base, clipped, err)
	}
	log, err := collectWorkspaceLog(context.Background(), root, run, "")
	if err != nil || log["head"] == "" {
		t.Fatalf("log: %#v %v", log, err)
	}
	headRaw, _ := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	head := strings.TrimSpace(string(headRaw))
	files, err := collectWorkspaceCommitFiles(context.Background(), root, run, "", head)
	if err != nil || len(files) != 1 {
		t.Fatalf("commit files: %+v %v", files, err)
	}
	blob, _, err := readWorkspaceGitBlob(context.Background(), root, run, "src/a.txt", head)
	if err != nil || blob != "hello" {
		t.Fatalf("blob: %q %v", blob, err)
	}
}

func TestServerAuthBinaryAndSSEHelpers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for input, want := range map[string]string{" Linux ": "linux", "ARM64": "arm64", "bad-value": "", strings.Repeat("a", 17): ""} {
		if got := sanitizePlatformParam(input); got != want {
			t.Errorf("sanitize(%q)=%q want %q", input, got, want)
		}
	}
	if got := clipBuildOutput([]byte(strings.Repeat("x", 500))); len(got) != 400 {
		t.Fatalf("clip len = %d", len(got))
	}
	t.Setenv("AGENTFLOW_SOURCE_ROOT", t.TempDir())
	if runnerSourceRoot() != "" {
		t.Fatal("invalid source root accepted")
	}
	src := t.TempDir()
	for _, path := range []string{"cmd/agentflow-runner", "cmd/agentflow-gate-mcp", "internal/x"} {
		if err := os.MkdirAll(filepath.Join(src, path), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"go.mod": "module x", "go.sum": "", "cmd/agentflow-runner/main.go": "package main", "internal/x/x.go": "package x", "internal/x/x_test.go": "ignored"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AGENTFLOW_SOURCE_ROOT", src)
	if runnerSourceRoot() != src {
		t.Fatal("source root not found")
	}
	if digest, err := runnerSourceDigest(src); err != nil || len(digest) != 64 {
		t.Fatalf("digest: %q %v", digest, err)
	}

	for remote, want := range map[string]bool{"127.0.0.1:1": true, "::1": true, "8.8.8.8:1": false, "bad": false} {
		if got := isLoopbackAddr(remote); got != want {
			t.Errorf("loopback(%q)=%v", remote, got)
		}
	}
	t.Setenv("AGENTFLOW_TRUST_IDENTITY_HEADERS", "true")
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.RemoteAddr = "127.0.0.1:123"
	req.Header["sTaFfNaMe"] = []string{"alice"}
	if gatewayLoginName(req) != "alice" || getHeaderCaseInsensitive(req, "STAFFNAME") != "alice" {
		t.Fatal("gateway case-insensitive lookup failed")
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	if !secureRequest(req) {
		t.Fatal("forwarded https not secure")
	}
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	if schedulerEnabled() {
		t.Fatal("scheduler false ignored")
	}
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "invalid")
	if !schedulerEnabled() {
		t.Fatal("invalid scheduler value should enable")
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	writeSSE(c, "done", map[string]int{"n": 1})
	if !strings.Contains(rec.Body.String(), "event: done") {
		t.Fatalf("SSE = %q", rec.Body.String())
	}
	job := &runEvalJob{subs: map[chan runEvalJobEvent]struct{}{}}
	ch, replay := job.subscribe()
	if len(replay) != 0 {
		t.Fatal("unexpected replay")
	}
	job.publish("progress", "x")
	if ev := <-ch; ev.Type != "progress" {
		t.Fatalf("event = %+v", ev)
	}
	job.unsubscribe(ch)
}

func TestTrailingSlashAndMiddlewareBranches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodPost, "/api/users/", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/users/", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/auth/tokens/", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/api/auth/tokens/", http.StatusUnauthorized},
		{http.MethodPost, "/api/otel/", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(tc.method, tc.path, nil)
		if !masterTrailingSlashCompat(c, &Server{deps: &deps{}}) || rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/not-api/", nil)
	if masterTrailingSlashCompat(c, &Server{deps: &deps{}}) {
		t.Fatal("non-api path handled")
	}

	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	respondWorkspaceError(c, os.ErrNotExist)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("workspace not found status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	respondWorkspaceError(c, errors.New("bad"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("workspace bad status %d", rec.Code)
	}
}

func TestStreamProtocolExtractionBranches(t *testing.T) {
	for input, want := range map[string]bool{
		"": false, "ordinary thought": false, `{"summary":"s","questions":[]}`: true,
		`[1,2]`: true, `prefix {"suggestedTitle":"x"} suffix`: true,
		`prefix {"other":1} suffix`: false, `prefix {broken} suffix`: false,
	} {
		if got := clarifyThoughtShouldHide(input); got != want {
			t.Errorf("hide(%q)=%v want %v", input, got, want)
		}
	}
	if got := extractClarifyProtocolObject("```json\n{\"summary\":\"s\",\"questions\":[]}\n```"); got == nil {
		t.Fatal("protocol object not extracted")
	}
	if extractClarifyProtocolObject(`{"other":1}`) != nil || extractClarifyProtocolObject("none") != nil {
		t.Fatal("non-protocol object extracted")
	}
	toolCases := []struct {
		name, input, want string
	}{
		{"Read", `{"file_path":"/tmp/a.go"}`, "a.go"},
		{"Grep", `{"pattern":"TODO","path":"/tmp/src"}`, "TODO @ src"},
		{"Search", `{"query":"hello"}`, "hello"},
		{mainBotBrowserPrefix + "click", `{"ref":"button","text":"a very long button label"}`, "button"},
		{mainBotBrowserPrefix + "open", `{"url":"https://example.test"}`, "https://example.test"},
		{"none", `{}`, ""},
	}
	for _, tc := range toolCases {
		got := clarifyToolDetail(tc.name, json.RawMessage(tc.input))
		if !strings.Contains(got, tc.want) {
			t.Errorf("detail(%s)=%q want contains %q", tc.name, got, tc.want)
		}
	}
	if clarifyToolDetail("bad", json.RawMessage("{")) != "" {
		t.Fatal("invalid tool JSON")
	}

	protocol := `{"summary":"done","questions":[],"ready":true}`
	streams := []string{
		protocol,
		"{\"type\":\"result\",\"result\":" + strconv.Quote(protocol) + "}",
		`{"type":"result","result":{"summary":"done","questions":[]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"` + strings.ReplaceAll(protocol, `"`, `\"`) + `"}]}}`,
	}
	for _, stream := range streams {
		if value, _ := extractClarifyResultJSON(stream); value == nil {
			t.Errorf("clarify result missing for %s", stream)
		}
		if value, _ := extractJSONObject(stream); value == nil {
			t.Errorf("object missing for %s", stream)
		}
	}
	if value, raw := extractClarifyResultJSON("plain"); value != nil || raw != "" {
		t.Fatalf("plain clarify = %#v %q", value, raw)
	}
	if value, raw := extractJSONObject("plain summary"); value == nil || raw == "" {
		t.Fatalf("plain object fallback = %#v %q", value, raw)
	}
	for _, wrapped := range []any{
		[]any{1, 2},
		map[string]any{"type": "result", "result": protocol},
		map[string]any{"type": "result", "result": map[string]any{"summary": "x"}},
		map[string]any{"type": "other", "value": 1},
	} {
		if value, ok := unwrapStreamJSONEnvelope(wrapped); !ok || value == nil {
			t.Errorf("unwrap %#v = %#v %v", wrapped, value, ok)
		}
	}
	try := func(s string) (any, bool) {
		var v any
		err := json.Unmarshal([]byte(s), &v)
		return v, err == nil
	}
	for _, event := range []map[string]any{
		{"content": []any{nil, map[string]any{"text": protocol}}},
		{"content": []any{map[string]any{"content": protocol}}},
		{"result": protocol},
		{"text": protocol},
	} {
		if _, _, ok := extractClarifyJSONFromEvent(event, try); !ok {
			t.Errorf("event not extracted: %#v", event)
		}
	}
}

func TestRunAndTaskPureBranches(t *testing.T) {
	for media, want := range map[string]string{"text/markdown": ".md", "application/json": ".json", "text/plain": ".txt"} {
		if got := artifactExtension(media); got != want {
			t.Errorf("extension(%q)=%q", media, got)
		}
	}
	run := domain.Run{
		ID: "r", Input: strings.Repeat("i", 700), Status: "succeeded",
		Artifacts: []domain.Artifact{{Name: "a", Content: "secret"}},
		Nodes: map[string]*domain.NodeRun{
			"nil": nil,
			"n":   {Status: "succeeded", Output: strings.Repeat("o", 3000), Error: strings.Repeat("e", 1500), Feedback: strings.Repeat("f", 1000), Activity: strings.Repeat("a", 500)},
		},
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID: "wf", Description: strings.Repeat("d", 300),
			Nodes: []domain.WorkflowNode{{ID: "n", Script: "secret", Command: "secret"}},
		},
	}
	detail := publicRun(run, false)
	if detail.Artifacts[0].Content != "" || detail.Nodes["n"].Output == "" || detail.WorkflowSnapshot.Nodes[0].Script != "" {
		t.Fatalf("public detail not slimmed: %+v", detail)
	}
	summary := publicRun(run, true)
	if summary.Nodes["n"].Output != "" || summary.WorkflowSnapshot.ID != "wf" || summary.Input == run.Input {
		t.Fatalf("public summary not slimmed: %+v", summary)
	}
	for status, want := range map[string]string{"queued": "queued", "running": "running", "waiting": "running", "succeeded": "completed", "failed": "failed", "cancelled": "cancelled", "other": ""} {
		if got := taskStatusFromRun(status); got != want {
			t.Errorf("task status %q=%q", status, got)
		}
	}
	if !runHasActiveNode(domain.Run{Nodes: map[string]*domain.NodeRun{"n": {Status: "running"}}}) ||
		!runHasActiveNode(domain.Run{Nodes: map[string]*domain.NodeRun{"n": {Status: "pending_input"}}}) ||
		runHasActiveNode(domain.Run{Nodes: map[string]*domain.NodeRun{"n": {Status: "succeeded"}}}) {
		t.Fatal("active node detection")
	}
	issue := domain.Issue{Status: "todo"}
	for status, want := range map[string]string{"running": "in_progress", "completed": "in_review", "failed": "blocked", "cancelled": "cancelled", "other": "todo"} {
		if got := issueStatusFromLatestTask(issue, status); got != want {
			t.Errorf("issue status %q=%q want %q", status, got, want)
		}
	}
	if got := issueStatusFromLatestTask(domain.Issue{Status: "closed"}, "running"); got != "closed" {
		t.Fatalf("closed issue = %q", got)
	}
	if agentIDFromRun(domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "n", AgentID: "a"}}}}, "n") != "a" {
		t.Fatal("agent id lookup")
	}
}

func TestRunnerOutputAndServerEndpointBranches(t *testing.T) {
	if validateRunnerTaskOutputs(nil) != "" || validateRunnerTaskOutputs(&domain.RunnerTaskOutputs{}) != "" {
		t.Fatal("empty outputs rejected")
	}
	tooManyFiles := map[string]string{}
	tooManyPatches := map[string]string{}
	for i := 0; i < 65; i++ {
		tooManyFiles[strconv.Itoa(i)] = "x"
		tooManyPatches[strconv.Itoa(i)] = "x"
	}
	outputCases := []*domain.RunnerTaskOutputs{
		{Files: tooManyFiles},
		{Patches: tooManyPatches},
		{Files: map[string]string{"../bad": "x"}},
		{Files: map[string]string{"good": strings.Repeat("x", (1<<20)+1)}},
		{Patches: map[string]string{"../bad": "x"}},
		{Patches: map[string]string{"repo": strings.Repeat("x", (1<<20)+1)}},
	}
	for i, output := range outputCases {
		if validateRunnerTaskOutputs(output) == "" {
			t.Errorf("invalid outputs case %d accepted", i)
		}
	}

	f := cvBNewHTTPFixture(t)
	binary := filepath.Join(t.TempDir(), "runner")
	if err := os.WriteFile(binary, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_RUNNER_BINARY", binary)
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/runners/agentflow-runner?os=linux&arch=amd64", 200},
		{"/runners/agentflow-runner?os=freebsd&arch=amd64", 400},
		{"/runners/agentflow-runner?os=linux&arch=mips", 400},
	} {
		rec := cvBRequest(t, f, http.MethodGet, tc.path, nil, false)
		if rec.Code != tc.want {
			t.Errorf("%s = %d want %d", tc.path, rec.Code, tc.want)
		}
	}
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(t.TempDir(), "missing-{os}-{arch}"))
	if rec := cvBRequest(t, f, http.MethodGet, "/runners/agentflow-runner?os=linux&arch=arm64", nil, false); rec.Code != 404 {
		t.Errorf("missing binary = %d", rec.Code)
	}

	emptyReq := httptest.NewRequest(http.MethodPost, "/api/otel/v1/traces", nil)
	emptyReq.RemoteAddr = "127.0.0.1:1"
	emptyRec := httptest.NewRecorder()
	f.h.ServeHTTP(emptyRec, emptyReq)
	if emptyRec.Code != 200 {
		t.Errorf("empty OTLP = %d: %s", emptyRec.Code, emptyRec.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/otel/v1/traces", strings.NewReader("not gzip"))
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Content-Encoding", "gzip")
	badGzip := httptest.NewRecorder()
	f.h.ServeHTTP(badGzip, req)
	if badGzip.Code != 400 {
		t.Errorf("bad gzip = %d", badGzip.Code)
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, _ = gz.Write(nil)
	_ = gz.Close()
	req = httptest.NewRequest(http.MethodPost, "/api/otel/v1/traces", &compressed)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Content-Encoding", "gzip")
	goodGzip := httptest.NewRecorder()
	f.h.ServeHTTP(goodGzip, req)
	if goodGzip.Code != 200 {
		t.Errorf("good gzip = %d", goodGzip.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/otel/v1/traces", nil)
	req.RemoteAddr = "8.8.8.8:1"
	forbidden := httptest.NewRecorder()
	f.h.ServeHTTP(forbidden, req)
	if forbidden.Code != 403 {
		t.Errorf("remote OTLP = %d", forbidden.Code)
	}
}
