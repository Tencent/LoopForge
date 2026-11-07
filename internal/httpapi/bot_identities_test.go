package httpapi

import (
	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBotBindingHTTPRequiresBrowserLoginOriginAndOriginalSender(t *testing.T) {
	s, assistant, bot := botFixture(t)
	assistant.deps.authClient = auth.NewClient(auth.Config{SessionSecret: "bot-binding-http-test"})
	h := &botIdentityHandlers{deps: assistant.deps, publicURL: "http://platform.example"}
	router := gin.New()
	router.POST("/api/bot-bindings/claim", h.binding)
	router.POST("/api/bot-bindings/status", h.binding)
	router.GET("/api/bot-bindings", h.list)
	router.DELETE("/api/bot-bindings/:id", h.remove)
	token, err := s.NewBotBindingChallenge(bot.BotID, "original", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := h.getAuthClient().SignUser(&auth.User{ID: 43, LoginName: "other-user", AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, origin, bearer string, logged bool, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://platform.example"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		if logged {
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: cookie})
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	body := `{"token":"` + token + `"}`
	if out := call("POST", "/api/bot-bindings/claim", "http://platform.example", "", false, body); out.Code != 401 {
		t.Fatal(out.Code, out.Body.String())
	}
	if out := call("POST", "/api/bot-bindings/claim", "http://evil.example", "", true, body); out.Code != 403 {
		t.Fatal(out.Code, out.Body.String())
	}
	if out := call("POST", "/api/bot-bindings/claim", "http://platform.example", "token", true, body); out.Code != 401 {
		t.Fatal(out.Code, out.Body.String())
	}
	out := call("POST", "/api/bot-bindings/claim", "http://platform.example", "", true, body)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var data struct {
		Command string `json:"command"`
		Login   string `json:"loginName"`
	}
	if err = json.Unmarshal(out.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.Login != "other-user" || !strings.HasPrefix(data.Command, "/bind ") {
		t.Fatal(out.Body.String())
	}
	if _, err = s.BotUserBinding(bot.BotID, "original"); err == nil {
		t.Fatal("browser claim activated binding")
	}
	if _, err = s.ConfirmBotUserBinding(bot.BotID, "group-attacker", strings.TrimPrefix(data.Command, "/bind "), time.Now().UTC()); err == nil {
		t.Fatal("wrong group sender confirmed")
	}
	handled, reply, err := h.message(bot, domain.BotInteraction{SenderID: "original", Content: data.Command})
	if err != nil || !handled || !strings.Contains(reply, "other-user") {
		t.Fatal(reply, err)
	}
	out = call("POST", "/api/bot-bindings/status", "http://platform.example", "", true, body)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "confirmed") {
		t.Fatal(out.Code, out.Body.String())
	}
	binding, err := s.BotUserBinding(bot.BotID, "original")
	if err != nil || binding.UserID != 43 {
		t.Fatal(binding, err)
	}
	out = call("DELETE", "/api/bot-bindings/"+binding.ID, "http://platform.example", "", true, "")
	if out.Code != 204 {
		t.Fatal(out.Code, out.Body.String())
	}
	if _, err = assistant.botExecutionUser(bot, "original"); err == nil {
		t.Fatal("revoked actor still executes")
	}
}

func TestBotBindingLinkSurvivesNotificationRedaction(t *testing.T) {
	_, assistant, bot := botFixture(t)
	h := &botIdentityHandlers{deps: assistant.deps, publicURL: "http://localhost:33328"}
	handled, reply, err := h.message(bot, domain.BotInteraction{SenderID: "link-test", Content: "绑定账号"})
	if err != nil || !handled {
		t.Fatal(err)
	}
	rendered := domain.NotificationText(reply, 16000)
	if strings.Contains(rendered, "[redacted]") || !strings.Contains(rendered, "/bot-bind.html#request=") || !strings.Contains(rendered, ")（15 分钟内有效）") {
		t.Fatal("binding URL damaged by reply pipeline", rendered)
	}
}

func TestBotUnbindOnlyRevokesCurrentSender(t *testing.T) {
	s, assistant, bot := botFixture(t)
	h := &botIdentityHandlers{deps: assistant.deps}
	now := time.Now().UTC()
	token, err := s.NewBotBindingChallenge(bot.BotID, "unbind-test", now)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.ClaimBotBindingChallenge(token, 43, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmBotUserBinding(bot.BotID, "unbind-test", code, now); err != nil {
		t.Fatal(err)
	}
	handled, reply, err := h.message(bot, domain.BotInteraction{SenderID: "unbound-other", Content: "/unbind"})
	if !handled || err != nil || !strings.Contains(reply, "尚未绑定") {
		t.Fatal(reply, err)
	}
	if _, err = s.BotUserBinding(bot.BotID, "unbind-test"); err != nil {
		t.Fatal("other sender revoked binding", err)
	}
	handled, reply, err = h.message(bot, domain.BotInteraction{SenderID: "unbind-test", Content: "/unbind"})
	if !handled || err != nil || !strings.Contains(reply, "已解除") {
		t.Fatal(reply, err)
	}
	if _, err = s.BotUserBinding(bot.BotID, "unbind-test"); err == nil {
		t.Fatal("binding survived")
	}
	if _, err = s.ConfirmBotUserBinding(bot.BotID, "unbind-test", code, now); err == nil {
		t.Fatal("old code rebound")
	}
}
