package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestNotificationBotMutationBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutProject(domain.Project{ID: "cvb-notify", Key: "NOT", Name: "Notify", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	created := cvBRequest(t, f, http.MethodPost, "/api/notification-bots", map[string]any{
		"projectId": "cvb-notify", "name": "Bot", "botId": "bot-one", "secret": "secret",
	}, true)
	if created.Code != 200 {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	var bot domain.NotificationBot
	if err := json.Unmarshal(created.Body.Bytes(), &bot); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPut, "/api/notification-bots/missing", map[string]any{}, 404},
		{http.MethodDelete, "/api/notification-bots/missing", nil, 404},
		{http.MethodPost, "/api/notification-bots/" + bot.ID + "/test", nil, 400},
		{http.MethodPut, "/api/notification-bots/" + bot.ID, map[string]any{"projectId": "cvb-notify", "name": "Bot", "botId": "changed"}, 400},
		{http.MethodPut, "/api/notification-bots/" + bot.ID, map[string]any{"projectId": "cvb-notify", "name": "Bot", "botId": "bot-one", "model": "invalid"}, 400},
		{http.MethodPut, "/api/notification-bots/" + bot.ID, map[string]any{"projectId": "cvb-notify", "name": "Bot2", "botId": "bot-one", "targetId": "missing", "secret": "new"}, 400},
		{http.MethodPut, "/api/notification-bots/" + bot.ID, map[string]any{"projectId": "cvb-notify", "name": "Bot2", "botId": "bot-one", "targetId": ""}, 200},
		{http.MethodGet, "/api/notification-bots/" + bot.ID + "/sessions?q=x", nil, 502},
		{http.MethodDelete, "/api/notification-bots/" + bot.ID, nil, 200},
	}
	for i, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%d %s %s = %d want %d: %s", i, tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
		if tc.want == http.StatusBadGateway && !strings.Contains(rec.Body.String(), f.notificationProvider.err.Error()) {
			t.Errorf("sessions response did not contain the fake provider error: %s", rec.Body.String())
		}
	}
	if sessions, sends := f.notificationProvider.sessionsCalls.Load(), f.notificationProvider.sendCalls.Load(); sessions != 1 || sends != 0 {
		t.Fatalf("provider calls: sessions=%d send=%d, want 1 and 0", sessions, sends)
	}
}

func TestNotificationProviderErrorBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &notificationHandlers{deps: &deps{repo: f.s}}
	for _, err := range []error{errors.New("plain"), &notification.ProviderError{Code: 404, Message: "missing"}} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		h.providerError(c, err)
		if rec.Code != 502 {
			t.Errorf("provider error = %d", rec.Code)
		}
	}
}

func TestNotificationCollectionPreviewAndRetryBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	const projectID = "cvb-notify-more"
	if err := f.s.PutProject(domain.Project{ID: projectID, Key: "NBM", Name: "Notify More", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/api/notification-bots?projectId=missing", nil, 404},
		{http.MethodGet, "/api/notification-bots?projectId=" + projectID, nil, 200},
		{http.MethodPatch, "/api/notification-bots", nil, 405},
		{http.MethodGet, "/api/notification-deliveries?projectId=" + projectID, nil, 200},
		{http.MethodPost, "/api/notification-deliveries/missing/retry?projectId=" + projectID, nil, 404},
		{http.MethodPost, "/api/notification-preview", map[string]any{
			"projectId": projectID, "sourceType": "run",
			"binding": map[string]any{"scope": "run", "events": []string{"succeeded"}, "template": "{{project}} {{status}} {{url}}"},
		}, 200},
		{http.MethodPost, "/api/notification-preview", map[string]any{
			"projectId": projectID, "sourceType": "schedule_job",
			"binding": map[string]any{"events": []string{"succeeded"}, "template": "{{task.name}} {{status}}"},
		}, 200},
		{http.MethodPost, "/api/notification-preview", map[string]any{
			"projectId": projectID, "sourceType": "assistant_task",
			"binding": map[string]any{"events": []string{"succeeded"}, "contentMode": "ai"},
		}, 200},
		{http.MethodPost, "/api/notification-preview", map[string]any{
			"projectId": projectID, "binding": map[string]any{"events": []string{"succeeded"}, "template": "{{secret}}"},
		}, 400},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestNotificationInteractiveAndUpdateBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	const projectID = "cvb-notify-interactive"
	if err := f.s.PutProject(domain.Project{ID: projectID, Key: "NBI", Name: "Interactive", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	create := func(name, botID string, interaction bool) *domain.NotificationBot {
		rec := cvBRequest(t, f, http.MethodPost, "/api/notification-bots", map[string]any{
			"projectId": projectID, "name": name, "botId": botID, "secret": "secret",
			"enabled": true, "interactionEnabled": interaction, "model": "",
		}, true)
		if rec.Code != 200 {
			t.Fatalf("create %s = %d: %s", name, rec.Code, rec.Body.String())
		}
		var bot domain.NotificationBot
		if err := json.Unmarshal(rec.Body.Bytes(), &bot); err != nil {
			t.Fatal(err)
		}
		return &bot
	}
	first := create("First", "same-bot", true)
	if rec := cvBRequest(t, f, http.MethodPost, "/api/notification-bots", map[string]any{
		"projectId": projectID, "name": "Second", "botId": "same-bot", "secret": "secret",
		"enabled": true, "interactionEnabled": true,
	}, true); rec.Code != 409 {
		t.Errorf("duplicate interactive = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodPut, "/api/notification-bots/"+first.ID, map[string]any{
		"projectId": projectID, "name": strings.Repeat("x", 81), "botId": "same-bot",
	}, true); rec.Code != 400 {
		t.Errorf("long name = %d", rec.Code)
	}
	if rec := cvBRequest(t, f, http.MethodPut, "/api/notification-bots/"+first.ID, map[string]any{
		"projectId": projectID, "name": "First", "botId": "same-bot", "interactionEnabled": false,
	}, true); rec.Code != 200 {
		t.Errorf("disable interaction = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNotificationDeliveryRetryBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	const projectID = "cvb-notify-retry"
	if err := f.s.PutProject(domain.Project{ID: projectID, Key: "NBR", Name: "Retry", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	create := cvBRequest(t, f, http.MethodPost, "/api/notification-bots", map[string]any{
		"projectId": projectID, "name": "Retry Bot", "botId": "retry-bot", "secret": "secret",
	}, true)
	var bot domain.NotificationBot
	if create.Code != 200 || json.Unmarshal(create.Body.Bytes(), &bot) != nil {
		t.Fatalf("bot = %d: %s", create.Code, create.Body.String())
	}
	now := time.Now().UTC()
	delivery := domain.NotificationDelivery{ID: "cvb-delivery", ProjectID: projectID, BotID: bot.ID, Status: "failed", CreatedAt: now, NextAttemptAt: now}
	if err := f.s.CreateNotificationDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodPost, "/api/notification-deliveries/"+delivery.ID+"/retry?projectId="+projectID, nil, true); rec.Code != 200 {
		t.Errorf("retry = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodPost, "/api/notification-deliveries/"+delivery.ID+"/retry?projectId="+projectID, nil, true); rec.Code != 409 {
		t.Errorf("second retry = %d: %s", rec.Code, rec.Body.String())
	}
}

func cvBJSONReaderRound2(t *testing.T, body any) *bytes.Reader {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(raw)
}

type cvBNotificationProviderRound2 struct {
	sessions      []domain.BotChat
	err           error
	sendErr       error
	sessionsCalls atomic.Int32
	sendCalls     atomic.Int32
}

func (p *cvBNotificationProviderRound2) Sessions(context.Context, string, string) ([]domain.BotChat, error) {
	p.sessionsCalls.Add(1)
	return p.sessions, p.err
}

func (p *cvBNotificationProviderRound2) Send(context.Context, string, string, string, string) error {
	p.sendCalls.Add(1)
	return p.sendErr
}

func TestNotificationSessionsTargetAndSend(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	project := domain.Project{ID: "cvb-r2-notify", Key: "R2N", Name: "Notify", OwnerID: f.user.ID}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	bot := domain.NotificationBot{ID: "cvb-r2-bot", ProjectID: project.ID, Name: "Bot", BotID: "provider-bot",
		OwnerID: f.user.ID, Enabled: true}
	if err := f.s.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	provider := &cvBNotificationProviderRound2{sessions: []domain.BotChat{{ID: "chat-one", Name: "Alpha"}, {ID: "chat-two", Name: "Beta"}}}
	service := notification.New(f.s)
	service.Provider = provider
	h := &notificationHandlers{deps: &deps{repo: f.s}, service: service}
	router := gin.New()
	router.GET("/bots/:id/sessions", h.sessions)
	router.POST("/bots/:id/test", h.test)
	router.PUT("/bots/:id", h.bot)
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		var req *http.Request
		if body == nil {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, cvBJSONReaderRound2(t, body))
			req.Header.Set("Content-Type", "application/json")
		}
		req = authedRequest(t, req, f.user)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(http.MethodGet, "/bots/"+bot.ID+"/sessions?q=alp", nil); rec.Code != http.StatusOK {
		t.Fatalf("sessions = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPut, "/bots/"+bot.ID, map[string]any{
		"projectId": project.ID, "name": "Bot", "botId": bot.BotID, "targetId": "chat-one", "enabled": true,
	}); rec.Code != http.StatusOK {
		t.Fatalf("target update = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPost, "/bots/"+bot.ID+"/test", nil); rec.Code != http.StatusOK {
		t.Fatalf("test send = %d: %s", rec.Code, rec.Body.String())
	}
	provider.sendErr = &notification.ProviderError{Code: 850003, Message: "expired"}
	if rec := request(http.MethodPost, "/bots/"+bot.ID+"/test", nil); rec.Code != http.StatusBadGateway {
		t.Fatalf("provider send error = %d: %s", rec.Code, rec.Body.String())
	}
	provider.sendErr = &notification.ProviderError{Code: 404, Message: "gone"}
	if rec := request(http.MethodPost, "/bots/"+bot.ID+"/test", nil); rec.Code != http.StatusBadGateway {
		t.Fatalf("missing target send = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNotificationProviderSessionErrors(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	project := domain.Project{ID: "cvb-r2-notify-errors", Key: "R2E", Name: "Notify Errors", OwnerID: f.user.ID}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	bot := domain.NotificationBot{ID: "cvb-r2-error-bot", ProjectID: project.ID, Name: "Bot", BotID: "provider-bot",
		OwnerID: f.user.ID, Enabled: true}
	if err := f.s.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	provider := &cvBNotificationProviderRound2{err: errors.New("provider unavailable")}
	service := notification.New(f.s)
	service.Provider = provider
	h := &notificationHandlers{deps: &deps{repo: f.s}, service: service}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: bot.ID}}
	c.Request = authedRequest(t, httptest.NewRequest(http.MethodGet, "/?q=x", nil), f.user)
	h.sessions(c)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("session provider error = %d: %s", rec.Code, rec.Body.String())
	}
}
