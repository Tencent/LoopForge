package httpapi

import (
	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type botAPIProvider struct{ sent int }

func (f *botAPIProvider) Sessions(context.Context, string, string) ([]domain.BotChat, error) {
	return []domain.BotChat{{ID: "group", Name: "研发群", Type: "group"}, {ID: "single", Name: "张三", Type: "single"}}, nil
}
func (f *botAPIProvider) Send(context.Context, string, string, string, string) error {
	f.sent++
	return nil
}
func TestNotificationBotAPIAuthBindingAndSecrets(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "public-model")
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	users := map[string]domain.User{}
	for _, name := range []string{"owner", "member", "viewer", "outsider"} {
		if err = s.PutUser(domain.User{LoginName: name}); err != nil {
			t.Fatal(err)
		}
		users[name], _ = s.UserByLoginName(name)
	}
	project := domain.Project{ID: "notify-project", Name: "测试项目", OwnerID: users["owner"].ID, Members: []domain.ProjectMember{{UserID: users["member"].ID, Role: "member"}, {UserID: users["viewer"].ID, Role: "viewer"}}}
	if err = s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	d := &deps{repo: s, authClient: auth.NewClient(auth.Config{SessionSecret: "notification-test-secret"})}
	svc := notification.New(s)
	provider := &botAPIProvider{}
	svc.Provider = provider
	h := &notificationHandlers{deps: d, service: svc}
	g := gin.New()
	g.GET("/bots", h.bots)
	g.POST("/bots", h.bots)
	g.PUT("/bots/:id", h.bot)
	g.DELETE("/bots/:id", h.bot)
	g.GET("/bots/:id/sessions", h.sessions)
	g.POST("/bots/:id/test", h.test)
	request := func(login, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if login != "" {
			u := users[login]
			token, err := d.authClient.SignUser(&auth.User{ID: u.ID, LoginName: u.LoginName, AuthTime: time.Now().UTC()})
			if err != nil {
				t.Fatal(err)
			}
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
		}
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		return rec
	}
	for _, tc := range []struct {
		login string
		want  int
	}{{"", 401}, {"viewer", 403}, {"outsider", 403}} {
		rsp := request(tc.login, "POST", "/bots", `{"projectId":"notify-project","name":"Bot","botId":"wecom","secret":"super-secret","enabled":true}`)
		if rsp.Code != tc.want {
			t.Fatalf("%s got %d %s", tc.login, rsp.Code, rsp.Body.String())
		}
	}
	rsp := request("owner", "POST", "/bots", `{"projectId":"notify-project","name":"Bot","botId":"wecom","secret":"super-secret","enabled":true}`)
	if rsp.Code != 200 {
		t.Fatalf("create: %d %s", rsp.Code, rsp.Body.String())
	}
	if strings.Contains(rsp.Body.String(), "super-secret") {
		t.Fatal("secret leaked")
	}
	var bot domain.NotificationBot
	if err = json.Unmarshal(rsp.Body.Bytes(), &bot); err != nil {
		t.Fatal(err)
	}
	if bot.Model != domain.DefaultNotificationReportModel {
		t.Fatal("missing default Bot model")
	}
	storedSecret, err := s.BotCredential(bot.ID)
	if err != nil || storedSecret != "super-secret" {
		t.Fatal("secret was not stored as plaintext")
	}
	if rsp = request("outsider", "GET", "/bots?projectId=notify-project", ""); rsp.Code != 403 {
		t.Fatal("outsider read project bot")
	}
	if rsp = request("viewer", "GET", "/bots?projectId=notify-project", ""); rsp.Code != 200 || strings.Contains(rsp.Body.String(), "super-secret") {
		t.Fatal("viewer public response")
	}
	if rsp = request("member", "GET", "/bots/"+bot.ID+"/sessions", ""); rsp.Code != 403 {
		t.Fatal("member queried someone else's private chats")
	}
	rsp = request("owner", "GET", "/bots/"+bot.ID+"/sessions?q=研发", "")
	if rsp.Code != 200 || strings.Contains(rsp.Body.String(), "张三") || !strings.Contains(rsp.Body.String(), "group") {
		t.Fatalf("search: %s", rsp.Body.String())
	}
	body := `{"name":"Bot","botId":"wecom","enabled":true,"targetId":"forged"}`
	if rsp = request("owner", "PUT", "/bots/"+bot.ID, body); rsp.Code != 409 {
		t.Fatal("forged recipient accepted")
	}
	body = `{"name":"Bot","botId":"wecom","enabled":true,"targetId":"group"}`
	if rsp = request("owner", "PUT", "/bots/"+bot.ID, body); rsp.Code != 200 {
		t.Fatalf("bind %s", rsp.Body.String())
	}
	bot, _ = s.NotificationBot(bot.ID)
	if bot.Target == nil || bot.Target.Name != "研发群" || bot.Target.Type != "group" {
		t.Fatal("target missing")
	}
	after, _ := s.BotCredential(bot.ID)
	if after != storedSecret {
		t.Fatal("blank secret erased credential")
	}
	if rsp = request("owner", "PUT", "/bots/"+bot.ID, `{"name":"Bot","botId":"wecom","enabled":true,"model":"public-model"}`); rsp.Code != 200 {
		t.Fatal(rsp.Body.String())
	}
	bot, _ = s.NotificationBot(bot.ID)
	if bot.Model != "public-model" || bot.Target == nil {
		t.Fatal("model update lost target")
	}
	if rsp = request("owner", "PUT", "/bots/"+bot.ID, `{"name":"Bot","botId":"wecom","enabled":true,"model":"unknown-model"}`); rsp.Code != 400 {
		t.Fatal("invalid model accepted")
	}
	rsp = request("owner", "POST", "/bots/"+bot.ID+"/test", "")
	if rsp.Code != 200 || provider.sent != 1 {
		t.Fatalf("test send %d %s", rsp.Code, rsp.Body.String())
	}
	records, _ := s.NotificationDeliveries(project.ID)
	if len(records) != 1 || records[0].Status != "sent" || records[0].EventType != "test" {
		t.Fatal("test not recorded")
	}
	if rsp = request("viewer", "DELETE", "/bots/"+bot.ID, ""); rsp.Code != 403 {
		t.Fatal("viewer deleted bot")
	}
	if rsp = request("owner", "DELETE", "/bots/"+bot.ID, ""); rsp.Code != 200 {
		t.Fatalf("delete %s", rsp.Body.String())
	}
	if _, err = s.BotCredential(bot.ID); err == nil {
		t.Fatal("deleted bot retained credentials")
	}
}
