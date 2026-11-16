package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func TestMissingCLIErrorOffersInstall(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", "")
	t.Setenv("AGENTFLOW_OPENCODE_BIN", "")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", t.TempDir())

	_, err := resolveAssistantEngineBin("opencode")
	if hostHas("opencode") {
		t.Log("host already has opencode; checking the error text directly")
		err = assistantCLIMissingError{engine: "opencode", binary: "opencode"}
	}
	if !errors.Is(err, errAssistantCLIMissing) || err.Error() != "OpenCode（opencode）没有安装在当前服务器上。安装后即可在对话里使用。" {
		t.Fatalf("opencode: %v", err)
	}
	_, err = resolveCodeBuddyBin()
	if hostHas("codebuddy") {
		err = assistantCLIMissingError{engine: "codebuddy", binary: "codebuddy"}
	}
	if !errors.Is(err, errAssistantCLIMissing) || err.Error() != "CodeBuddy（codebuddy）没有安装在当前服务器上。安装后即可在对话里使用。" {
		t.Fatalf("codebuddy: %v", err)
	}
}

func hostHas(binary string) bool {
	for _, dir := range []string{"/usr/local/bin", "/usr/bin", "/opt/homebrew/bin"} {
		if fileExecutable(filepath.Join(dir, binary)) {
			return true
		}
	}
	return false
}

func TestInstallCLIRequiresLoginAndAllowlist(t *testing.T) {
	h := &assistantHandlers{deps: &deps{}}
	rec := postCLIInstall(t, h, nil, `{"engine":"opencode"}`)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "请先登录") {
		t.Fatalf("guest: %d %s", rec.Code, rec.Body.String())
	}
	user := domainUserToAuth(domain.User{ID: 1, LoginName: "lab"})
	rec = postCLIInstall(t, h, user, `{"engine":"not-a-cli"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "不能从对话里安装") {
		t.Fatalf("unknown: %d %s", rec.Code, rec.Body.String())
	}
	if assistantInstallEngine("claude") != "claude-code" {
		t.Fatal("claude alias")
	}
}

func TestInstallCLIPutsOpenCodeOnTheConversationPath(t *testing.T) {
	if hostHas("opencode") {
		t.Skip("host opencode would hide the on-demand installation")
	}
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "npm.log")
	npm := t.TempDir()
	if err := os.WriteFile(filepath.Join(npm, "npm"), []byte(genericInstallNPM), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", npm+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	t.Setenv("AGENTFLOW_OPENCODE_BIN", "")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("NPM_TEST_LOG", log)
	t.Setenv("NPM_TEST_BIN", "opencode")

	h := &assistantHandlers{deps: &deps{}}
	user := domainUserToAuth(domain.User{ID: 1, LoginName: "lab"})
	rec := postCLIInstall(t, h, user, `{"engine":"opencode"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Engine    string `json:"engine"`
		Installed bool   `json:"installed"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Engine != "opencode" || !body.Installed {
		t.Fatalf("body %s", rec.Body.String())
	}
	path, err := resolveAssistantEngineBin("opencode")
	if err != nil || path != filepath.Join(root, "opencode", "current", "node_modules", ".bin", "opencode") {
		t.Fatalf("resolve %q %v", path, err)
	}
}

func TestInstallCLIContinuesAfterTheBrowserGoesAway(t *testing.T) {
	root := t.TempDir()
	npm := t.TempDir()
	script := `#!/bin/sh
sleep 0.4
prefix=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--prefix" ]; then prefix=$arg; fi
  prev=$arg
done
mkdir -p "$prefix/node_modules/.bin"
printf '#!/bin/sh\necho ok\n' > "$prefix/node_modules/.bin/$NPM_TEST_BIN"
chmod 755 "$prefix/node_modules/.bin/$NPM_TEST_BIN"
`
	if err := os.WriteFile(filepath.Join(npm, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", npm+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	t.Setenv("AGENTFLOW_PI_BIN", "")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("NPM_TEST_BIN", "pi")

	h := &assistantHandlers{deps: &deps{}}
	user := domainUserToAuth(domain.User{ID: 1, LoginName: "lab"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/cli-install", strings.NewReader(`{"engine":"pi"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(ctx, requestUserContextKey{}, user))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	returned := make(chan struct{})
	go func() {
		h.installCLI(c)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("handler waited for the install after the browser left")
	}
	binary := filepath.Join(root, "pi", "current", "node_modules", ".bin", "pi")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fileExecutable(binary) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("install stopped when the browser left")
}

func TestInstallStatusSurvivesAReload(t *testing.T) {
	resetCLIInstallJobs(t)
	root := t.TempDir()
	npm := t.TempDir()
	script := `#!/bin/sh
sleep 0.4
prefix=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--prefix" ]; then prefix=$arg; fi
  prev=$arg
done
mkdir -p "$prefix/node_modules/.bin"
printf '#!/bin/sh\necho ok\n' > "$prefix/node_modules/.bin/$NPM_TEST_BIN"
chmod 755 "$prefix/node_modules/.bin/$NPM_TEST_BIN"
`
	if err := os.WriteFile(filepath.Join(npm, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", npm+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	t.Setenv("AGENTFLOW_DSH_BIN", "")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("NPM_TEST_BIN", "dsh")

	h := &assistantHandlers{deps: &deps{}}
	user := domainUserToAuth(domain.User{ID: 1, LoginName: "lab"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/cli-install", strings.NewReader(`{"engine":"dsh"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(ctx, requestUserContextKey{}, user))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	returned := make(chan struct{})
	go func() {
		h.installCLI(c)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("handler waited for the install after the browser left")
	}
	status := getCLIInstall(t, h, user, "dsh")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"status":"installing"`) {
		t.Fatalf("reload during install: %d %s", status.Code, status.Body.String())
	}
	binary := filepath.Join(root, "dsh", "current", "node_modules", ".bin", "dsh")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fileExecutable(binary) {
			status = getCLIInstall(t, h, user, "dsh")
			if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"status":"installed"`) {
				t.Fatalf("after install: %d %s", status.Code, status.Body.String())
			}
			// A finished install does not outlive its executable: once it is
			// removed the status is missing again, so the button can reinstall.
			cliInstallJobsMu.Lock()
			job := cliInstallJobs["dsh"]
			cliInstallJobsMu.Unlock()
			<-job.done
			if err := os.RemoveAll(filepath.Join(root, "dsh", "current")); err != nil {
				t.Fatal(err)
			}
			status = getCLIInstall(t, h, user, "dsh")
			if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"status":"missing"`) {
				t.Fatalf("after the executable was removed: %d %s", status.Code, status.Body.String())
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("install stopped when the page reloaded")
}

func TestInstallCLIClearsTheStoredGenerationFailure(t *testing.T) {
	resetCLIInstallJobs(t)
	if hostHas("kimi") {
		t.Skip("host kimi would hide the on-demand installation")
	}
	root := t.TempDir()
	npm := t.TempDir()
	if err := os.WriteFile(filepath.Join(npm, "npm"), []byte(genericInstallNPM), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", npm+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	t.Setenv("AGENTFLOW_KIMI_BIN", "")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("NPM_TEST_BIN", "kimi")

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	user, err := store.CreateLocalAccount("cli-notice", "secret1", true)
	if err != nil {
		t.Fatal(err)
	}
	missing := "Kimi Code（kimi）没有安装在当前服务器上。安装后即可在对话里使用。"
	notice := "Kimi Code 已安装，请重新发送。"
	if err = store.PutAssistantSession(user.ID, domain.AssistantSession{
		ID: "cli-notice", Model: "kimi:test-model", Executor: "kimi", Title: "hello",
		Messages: []domain.AssistantMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: `{"message":""}`, Unfinished: assistantGenerationFailedMessage},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err = store.PutAssistantSession(user.ID, domain.AssistantSession{
		ID: "other-chat", Model: "codebuddy", Executor: "codebuddy", Title: "other",
		Messages: []domain.AssistantMessage{{Role: "assistant", Content: `{"message":""}`, Unfinished: assistantGenerationFailedMessage}},
	}); err != nil {
		t.Fatal(err)
	}
	failed := func(id, sessionID, text string) {
		t.Helper()
		g := domain.AssistantGeneration{ID: id, SessionID: sessionID, UserID: user.ID, Status: "running", StartedAt: time.Now().UTC()}
		if err := store.BeginAssistantGeneration(g); err != nil {
			t.Fatal(err)
		}
		g.Status = "failed"
		g.Error = text
		if err := store.CheckpointAssistantGeneration(g); err != nil {
			t.Fatal(err)
		}
	}
	failed("g1", "cli-notice", missing)
	failed("g2", "other-chat", assistantGenerationFailedMessage)

	h := &assistantHandlers{deps: &deps{repo: store}}
	actor := domainUserToAuth(user)
	before := getCLIInstall(t, h, actor, "kimi")
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), `"status":"missing"`) {
		t.Fatalf("before: %d %s", before.Code, before.Body.String())
	}
	rec := postCLIInstall(t, h, actor, `{"engine":"kimi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	saved, ok := store.AssistantGeneration(user.ID, "cli-notice")
	if !ok || saved.Error != notice {
		t.Fatalf("generation failure stayed %q", saved.Error)
	}
	session, ok := store.AssistantSession(user.ID, "cli-notice")
	if !ok || len(session.Messages) != 2 || session.Messages[1].Unfinished != notice {
		t.Fatalf("transcript failure stayed %+v", session.Messages)
	}
	otherGen, _ := store.AssistantGeneration(user.ID, "other-chat")
	otherSession, _ := store.AssistantSession(user.ID, "other-chat")
	if otherGen.Error != assistantGenerationFailedMessage || otherSession.Messages[0].Unfinished != assistantGenerationFailedMessage {
		t.Fatalf("unrelated failure changed: %q %+v", otherGen.Error, otherSession.Messages)
	}
	after := getCLIInstall(t, h, actor, "kimi")
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), `"status":"installed"`) {
		t.Fatalf("after: %d %s", after.Code, after.Body.String())
	}
}

func resetCLIInstallJobs(t *testing.T) {
	t.Helper()
	cliInstallJobsMu.Lock()
	cliInstallJobs = map[string]*cliInstallJob{}
	cliInstallJobsMu.Unlock()
	t.Cleanup(func() {
		cliInstallJobsMu.Lock()
		cliInstallJobs = map[string]*cliInstallJob{}
		cliInstallJobsMu.Unlock()
	})
}

func getCLIInstall(t *testing.T, h *assistantHandlers, user *auth.User, engine string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/assistant/cli-install?engine="+engine, nil)
	if user != nil {
		req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, user))
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	h.cliInstallStatus(c)
	return rec
}

func postCLIInstall(t *testing.T, h *assistantHandlers, user *auth.User, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/cli-install", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user != nil {
		req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, user))
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	h.installCLI(c)
	return rec
}

const genericInstallNPM = `#!/bin/sh
printf '%s\n' "$*" >> "$NPM_TEST_LOG"
prefix=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--prefix" ]; then prefix=$arg; fi
  prev=$arg
done
mkdir -p "$prefix/node_modules/.bin"
printf '#!/bin/sh\necho ok\n' > "$prefix/node_modules/.bin/$NPM_TEST_BIN"
chmod 755 "$prefix/node_modules/.bin/$NPM_TEST_BIN"
`
