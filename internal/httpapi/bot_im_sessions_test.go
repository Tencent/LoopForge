package httpapi

import (
	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBotIMConversationsListDetailAndContinue(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	owner := domain.User{ID: 10, LoginName: "alice", ChineseName: "Alice"}
	outsider := domain.User{ID: 11, LoginName: "bob", ChineseName: "Bob"}
	viewer := domain.User{ID: 12, LoginName: "carol", ChineseName: "Carol"}
	_ = st.PutUser(owner)
	_ = st.PutUser(outsider)
	_ = st.PutUser(viewer)
	_ = st.PutProject(domain.Project{
		ID:      "proj-im",
		Name:    "IM 项目",
		OwnerID: owner.ID,
		Members: []domain.ProjectMember{{UserID: viewer.ID, Role: "viewer"}},
	})
	_ = st.PutProject(domain.Project{ID: "proj-other", Name: "其他项目", OwnerID: outsider.ID})

	bot := domain.NotificationBot{
		ID:                 "bot-feishu-1",
		Name:               "飞书研发助手",
		Channel:            domain.BotChannelFeishu,
		BotID:              "cli_demo",
		ProjectID:          "proj-im",
		OwnerID:            owner.ID,
		Enabled:            true,
		InteractionEnabled: true,
		WriteApproval:      domain.BotWriteConfirm,
	}
	if err := st.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	convID := repo.BotConversationID(bot.ID, "group", "oc_group_1")
	now := time.Now().UTC()
	if err := st.DB.Create(&domain.BotConversation{
		ID:        convID,
		BotID:     bot.ID,
		ChatID:    "oc_group_1",
		ChatType:  "group",
		SessionID: "bot-sess-1",
		UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB.Create(&domain.BotInteraction{
		ID:             "int-bind",
		BotID:          bot.ID,
		ProjectID:      bot.ProjectID,
		ConversationID: convID,
		SessionID:      "bot-sess-1",
		SenderID:       "ou_1",
		Content:        "/bind",
		Reply:          "请打开 /bot-bind.html#request=abc",
		Status:         domain.BotReplied,
		CreatedAt:      now.Add(-2 * time.Minute),
		UpdatedAt:      now.Add(-2 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB.Create(&domain.BotInteraction{
		ID:             "int-msg-1",
		BotID:          bot.ID,
		ProjectID:      bot.ProjectID,
		ConversationID: convID,
		SessionID:      "bot-sess-1",
		SenderID:       "ou_zhang",
		Content:        "帮我查下最近构建失败原因",
		Reply:          "最近一次失败原因是单测超时",
		Status:         domain.BotReplied,
		CreatedAt:      now.Add(-time.Minute),
		UpdatedAt:      now.Add(-time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	// A newer /bind exchange must be skipped in list previews so bind codes/links never leak.
	if err := st.DB.Create(&domain.BotInteraction{
		ID:             "int-bind-latest",
		BotID:          bot.ID,
		ProjectID:      bot.ProjectID,
		ConversationID: convID,
		SessionID:      "bot-sess-1",
		SenderID:       "ou_lisi",
		Content:        "/bind BIND-CODE-MARK",
		Reply:          "请打开 /bot-bind.html#request=BIND-LINK-MARK",
		Status:         domain.BotReplied,
		CreatedAt:      now,
		UpdatedAt:      now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "bot-im-sessions-test-secret")
	e := engine.New(st, nil)
	router := NewWithNotifications(st, e, tracker.NewService(), "", "", notification.New(st))

	authClient := auth.NewClientFromEnv()
	ownerToken, err := authClient.SignUser(&auth.User{ID: owner.ID, LoginName: owner.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	outsiderToken, err := authClient.SignUser(&auth.User{ID: outsider.ID, LoginName: outsider.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, err := authClient.SignUser(&auth.User{ID: viewer.ID, LoginName: viewer.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}

	reqWithCookie := func(method, target, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		if token != "" {
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// Unauthenticated user rejected.
	if w := reqWithCookie("GET", "/api/bot-interactions/conversations", ""); w.Code != 401 {
		t.Fatalf("unauth list = %d, want 401", w.Code)
	}

	// Outsider cannot see proj-im's IM conversation.
	w := reqWithCookie("GET", "/api/bot-interactions/conversations", outsiderToken)
	if w.Code != 200 {
		t.Fatalf("outsider list = %d %s", w.Code, w.Body.String())
	}
	var outsiderList []imConversationSummary
	if err := json.Unmarshal(w.Body.Bytes(), &outsiderList); err != nil || len(outsiderList) != 0 {
		t.Fatalf("outsider list = %+v (err=%v), want empty", outsiderList, err)
	}
	if w := reqWithCookie("GET", "/api/bot-interactions/conversations/"+convID, outsiderToken); w.Code != 403 {
		t.Fatalf("outsider detail = %d, want 403", w.Code)
	}

	// Project viewer is denied parity with /api/notification-bots/:id/interactions.
	w = reqWithCookie("GET", "/api/bot-interactions/conversations", viewerToken)
	if w.Code != 200 {
		t.Fatalf("viewer list = %d %s", w.Code, w.Body.String())
	}
	var viewerList []imConversationSummary
	if err := json.Unmarshal(w.Body.Bytes(), &viewerList); err != nil || len(viewerList) != 0 {
		t.Fatalf("viewer list = %+v (err=%v), want empty", viewerList, err)
	}
	if w := reqWithCookie("GET", "/api/bot-interactions/conversations/"+convID, viewerToken); w.Code != 403 {
		t.Fatalf("viewer detail = %d, want 403", w.Code)
	}
	if w := reqWithCookie("POST", "/api/bot-interactions/conversations/"+convID+"/continue", viewerToken); w.Code != 403 {
		t.Fatalf("viewer continue = %d, want 403", w.Code)
	}

	// Owner sees the IM conversation with Feishu metadata and preview (skipping newer /bind).
	w = reqWithCookie("GET", "/api/bot-interactions/conversations", ownerToken)
	if w.Code != 200 {
		t.Fatalf("owner list = %d %s", w.Code, w.Body.String())
	}
	var list []imConversationSummary
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != convID || list[0].Channel != domain.BotChannelFeishu || list[0].ChatType != "group" || !strings.Contains(list[0].Preview, "构建失败") || strings.Contains(list[0].Preview, "BIND-CODE-MARK") || list[0].SenderID != "ou_zhang" {
		t.Fatalf("owner list = %+v", list)
	}

	// Detail omits binding exchanges and keeps real messages.
	w = reqWithCookie("GET", "/api/bot-interactions/conversations/"+convID, ownerToken)
	if w.Code != 200 {
		t.Fatalf("owner detail = %d %s", w.Code, w.Body.String())
	}
	var detail imConversationDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Messages) != 1 || detail.Messages[0].ID != "int-msg-1" {
		t.Fatalf("detail messages = %+v", detail.Messages)
	}

	// Continue in workbench creates a new personal chat session carrying the IM transcript.
	w = reqWithCookie("POST", "/api/bot-interactions/conversations/"+convID+"/continue", ownerToken)
	if w.Code != 201 {
		t.Fatalf("continue = %d %s", w.Code, w.Body.String())
	}
	var created domain.AssistantSession
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Source != domain.AssistantSourceChat || created.ProjectID != "proj-im" || len(created.Messages) != 2 {
		t.Fatalf("created session = %+v", created)
	}
	if !strings.Contains(created.Messages[0].Content, "飞书") || !strings.Contains(created.Messages[1].Content, "单测超时") {
		t.Fatalf("created messages = %+v", created.Messages)
	}
}
