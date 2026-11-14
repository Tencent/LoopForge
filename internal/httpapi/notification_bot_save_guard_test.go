package httpapi

import (
	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestNotificationBotModelRequestVersusCurrent pins how a request interacts
// with a non-default stored model: no request keeps it (even one that has left
// the catalog), a known request replaces it, a blank request resets it to the
// default and an unknown request never falls back to it.
func TestNotificationBotModelRequestVersusCurrent(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "public-model,other-model")
	str := func(v string) *string { return &v }
	cases := []struct {
		name      string
		requested *string
		current   string
		want      string
		wantOK    bool
	}{
		{name: "no request keeps non-default current", requested: nil, current: "public-model", want: "public-model", wantOK: true},
		{name: "no request keeps a model that left the catalog", requested: nil, current: "retired-model", want: "retired-model", wantOK: true},
		{name: "known request replaces current", requested: str("other-model"), current: "public-model", want: "other-model", wantOK: true},
		{name: "known request replaces default", requested: str("\tpublic-model\n"), current: domain.DefaultNotificationReportModel, want: "public-model", wantOK: true},
		{name: "blank request resets non-default current", requested: str(""), current: "public-model", want: domain.DefaultNotificationReportModel, wantOK: true},
		{name: "unknown request does not fall back to current", requested: str("unknown-model"), current: "public-model", want: "", wantOK: false},
		{name: "request is case sensitive", requested: str("PUBLIC-MODEL"), current: "", want: "", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := notificationBotModel(tc.requested, tc.current)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("notificationBotModel() = %q, %v; want %q, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestFindNotificationTargetExactFirstMatch keeps the original loop semantics:
// IDs match exactly (no trimming or case folding) and the first match wins.
func TestFindNotificationTargetExactFirstMatch(t *testing.T) {
	chats := []domain.BotChat{
		{ID: "dup", Name: "first", Type: "group"},
		{ID: "dup", Name: "second", Type: "single"},
		{ID: "Group", Name: "upper", Type: "group"},
	}
	target, ok := findNotificationTarget(chats, "dup")
	if !ok || target == nil || target.Name != "first" {
		t.Fatalf("findNotificationTarget(dup) = %+v, %v; want first match", target, ok)
	}
	for _, id := range []string{"group", " Group", "Group ", ""} {
		if target, ok := findNotificationTarget(chats, id); ok || target != nil {
			t.Fatalf("findNotificationTarget(%q) = %+v, %v; want nil, false", id, target, ok)
		}
	}
}

// TestNotificationBotSaveModelPersistence drives the save handler to check
// that the resolved model is what gets stored: omitting the model keeps the
// stored one, an invalid model is rejected without touching it, and a blank
// model resets it to the default.
func TestNotificationBotSaveModelPersistence(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "public-model")
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.PutUser(domain.User{LoginName: "owner"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := s.UserByLoginName("owner")
	if err = s.PutProject(domain.Project{ID: "notify-model", Name: "模型项目", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	d := &deps{repo: s, authClient: auth.NewClient(auth.Config{SessionSecret: "notification-model-secret"})}
	svc := notification.New(s)
	svc.Provider = &botAPIProvider{}
	h := &notificationHandlers{deps: d, service: svc}
	g := gin.New()
	g.POST("/bots", h.bots)
	g.PUT("/bots/:id", h.bot)
	token, err := d.authClient.SignUser(&auth.User{ID: owner.ID, LoginName: owner.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		return rec
	}
	storedModel := func(id string) string {
		t.Helper()
		bot, err := s.NotificationBot(id)
		if err != nil {
			t.Fatal(err)
		}
		return bot.Model
	}

	rsp := request("POST", "/bots", `{"projectId":"notify-model","name":"Bot","botId":"wecom","secret":"s","enabled":true,"model":"public-model"}`)
	if rsp.Code != 200 {
		t.Fatalf("create: %d %s", rsp.Code, rsp.Body.String())
	}
	bots, err := s.NotificationBots("notify-model")
	if err != nil || len(bots) != 1 {
		t.Fatalf("bots = %+v, %v", bots, err)
	}
	id := bots[0].ID
	if got := storedModel(id); got != "public-model" {
		t.Fatalf("created model = %q, want public-model", got)
	}

	if rsp = request("PUT", "/bots/"+id, `{"name":"Bot","botId":"wecom","enabled":true}`); rsp.Code != 200 {
		t.Fatalf("update without model: %d %s", rsp.Code, rsp.Body.String())
	}
	if got := storedModel(id); got != "public-model" {
		t.Fatalf("model after update without model = %q, want public-model kept", got)
	}

	rsp = request("PUT", "/bots/"+id, `{"name":"Renamed","botId":"wecom","enabled":true,"model":"unknown-model"}`)
	if rsp.Code != 400 || !strings.Contains(rsp.Body.String(), "请选择有效的 CodeBuddy 模型") {
		t.Fatalf("invalid model: %d %s", rsp.Code, rsp.Body.String())
	}
	bot, _ := s.NotificationBot(id)
	if bot.Model != "public-model" || bot.Name != "Bot" {
		t.Fatalf("rejected update changed bot: model=%q name=%q", bot.Model, bot.Name)
	}

	if rsp = request("PUT", "/bots/"+id, `{"name":"Bot","botId":"wecom","enabled":true,"model":"  "}`); rsp.Code != 200 {
		t.Fatalf("blank model: %d %s", rsp.Code, rsp.Body.String())
	}
	if got := storedModel(id); got != domain.DefaultNotificationReportModel {
		t.Fatalf("model after blank request = %q, want default", got)
	}
}
