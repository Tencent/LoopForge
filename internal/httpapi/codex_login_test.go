package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/codexoauth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

type fakeCodexDevice struct {
	startErr   error
	authorized bool
	pollErr    error
	polls      int
}

func (f *fakeCodexDevice) StartDevice(context.Context) (codexoauth.DeviceStart, error) {
	if f.startErr != nil {
		return codexoauth.DeviceStart{}, f.startErr
	}
	return codexoauth.DeviceStart{DeviceAuthID: "server-only", UserCode: "ABCD-1234", VerificationURL: "https://auth.openai.com/codex/device", Interval: 5 * time.Second, ExpiresIn: 15 * time.Minute}, nil
}

func (f *fakeCodexDevice) PollDevice(context.Context, codexoauth.DeviceStart) (codexoauth.Tokens, bool, error) {
	f.polls++
	if f.pollErr != nil || !f.authorized {
		return codexoauth.Tokens{}, false, f.pollErr
	}
	id := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"dev@example.com"}`)) + ".sig"
	return codexoauth.Tokens{IDToken: id, AccessToken: "access-secret", RefreshToken: "refresh-secret"}, true, nil
}

func codexLoginTestHandler(t *testing.T, client codexDeviceClient) (*codexLoginHandlers, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "")
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.PutUser(domain.User{LoginName: "github-owner"}); err != nil {
		t.Fatal(err)
	}
	user, _ := store.UserByLoginName("github-owner")
	handler := newCodexLoginHandlers(&deps{repo: store, authClient: auth.NewClient(auth.Config{SessionSecret: "codex-test-secret"})})
	handler.client = client
	return handler, user.ID
}

func (h *codexLoginHandlers) makeDue(userID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if grant := h.grants[userID]; grant != nil {
		grant.next = time.Now().Add(-time.Second)
	}
}

func TestCodexDeviceLoginSavesLoginWithoutExposingTokens(t *testing.T) {
	device := &fakeCodexDevice{}
	handler, userID := codexLoginTestHandler(t, device)
	current, recorder := githubTestContext(userID, http.MethodPost, "/api/auth/codex/device", "{}")
	handler.start(current)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "server-only") {
		t.Fatalf("start: %d %s", recorder.Code, recorder.Body)
	}
	var started struct {
		FlowID   string `json:"flowId"`
		UserCode string `json:"userCode"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &started)
	if started.UserCode != "ABCD-1234" || started.FlowID == "" {
		t.Fatalf("start body: %s", recorder.Body)
	}
	poll := func() string {
		handler.makeDue(userID)
		current, recorder := githubTestContext(userID, http.MethodPost, "/api/auth/codex/poll", `{"flowId":"`+started.FlowID+`"}`)
		handler.poll(current)
		return recorder.Body.String()
	}
	if body := poll(); !strings.Contains(body, `"pending"`) {
		t.Fatalf("pending poll: %s", body)
	}
	// An early poll does not reach OpenAI.
	before := device.polls
	current, recorder = githubTestContext(userID, http.MethodPost, "/api/auth/codex/poll", `{"flowId":"`+started.FlowID+`"}`)
	handler.poll(current)
	if device.polls != before || !strings.Contains(recorder.Body.String(), "pending") {
		t.Fatalf("early poll reached OpenAI: %s", recorder.Body)
	}
	device.authorized = true
	body := poll()
	if !strings.Contains(body, `"connected"`) || !strings.Contains(body, `"email":"dev@example.com"`) || !strings.Contains(body, `"codexChatGptLoginConfigured":true`) {
		t.Fatalf("connected poll: %s", body)
	}
	directory, _ := json.Marshal(domain.PublicUsers(handler.repo.Users()))
	if strings.Contains(string(directory), "dev@example.com") {
		t.Fatalf("user directory exposes the ChatGPT account: %s", directory)
	}
	if strings.Contains(body, "secret") {
		t.Fatalf("poll response leaks tokens: %s", body)
	}
	stored, _ := handler.repo.User(userID)
	if !strings.Contains(stored.CodexChatGPTLogin, "refresh-secret") {
		t.Fatal("login not stored with its refresh token")
	}
	current, recorder = githubTestContext(userID, http.MethodGet, "/api/auth/codex", "")
	handler.status(current)
	if !strings.Contains(recorder.Body.String(), `"configured":true`) || !strings.Contains(recorder.Body.String(), "dev@example.com") || strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("status: %s", recorder.Body)
	}
	// The finished flow is gone.
	current, recorder = githubTestContext(userID, http.MethodPost, "/api/auth/codex/poll", `{"flowId":"`+started.FlowID+`"}`)
	handler.poll(current)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("reused flow: %d %s", recorder.Code, recorder.Body)
	}
}

func TestCodexDeviceLoginFailures(t *testing.T) {
	handler, userID := codexLoginTestHandler(t, &fakeCodexDevice{startErr: codexoauth.ErrDeviceNotEnabled})
	current, recorder := githubTestContext(userID, http.MethodPost, "/api/auth/codex/device", "{}")
	handler.start(current)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "not_enabled") {
		t.Fatalf("not enabled: %d %s", recorder.Code, recorder.Body)
	}

	device := &fakeCodexDevice{pollErr: &codexoauth.Error{Status: 400, Code: "access_denied"}}
	handler.client = device
	current, recorder = githubTestContext(userID, http.MethodPost, "/api/auth/codex/device", "{}")
	handler.start(current)
	var started struct {
		FlowID string `json:"flowId"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &started)
	handler.makeDue(userID)
	current, recorder = githubTestContext(userID, http.MethodPost, "/api/auth/codex/poll", `{"flowId":"`+started.FlowID+`"}`)
	handler.poll(current)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "denied") {
		t.Fatalf("denied: %d %s", recorder.Code, recorder.Body)
	}

	current, recorder = githubTestContext(userID, http.MethodPost, "/api/auth/codex/device", "{}")
	current.Request.Header.Set("Origin", "https://evil.example")
	handler.start(current)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin start: %d", recorder.Code)
	}
	current, recorder = githubTestContext(0, http.MethodGet, "/api/auth/codex", "")
	handler.status(current)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status: %d", recorder.Code)
	}
}

func TestCodexLoginPasteAndSignOut(t *testing.T) {
	handler, userID := codexLoginTestHandler(t, &fakeCodexDevice{})
	current, recorder := githubTestContext(userID, http.MethodPut, "/api/auth/codex", `{"authJson":"{\"tokens\":{\"access_token\":\"a\"}}"}`)
	handler.paste(current)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "refresh_token") {
		t.Fatalf("incomplete paste: %d %s", recorder.Code, recorder.Body)
	}
	authJSON, _ := json.Marshal(map[string]string{"authJson": `{"auth_mode":"chatgpt","tokens":{"access_token":"a","refresh_token":"pasted-refresh"}}`})
	current, recorder = githubTestContext(userID, http.MethodPut, "/api/auth/codex", string(authJSON))
	handler.paste(current)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "pasted-refresh") {
		t.Fatalf("paste: %d %s", recorder.Code, recorder.Body)
	}
	current, recorder = githubTestContext(userID, http.MethodDelete, "/api/auth/codex", "")
	handler.signOut(current)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), `"codexChatGptLoginConfigured":true`) {
		t.Fatalf("sign out: %d %s", recorder.Code, recorder.Body)
	}
	if stored, _ := handler.repo.User(userID); stored.CodexChatGPTLogin != "" {
		t.Fatal("login kept after sign out")
	}
}
