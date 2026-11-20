package httpapi

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// stubChannel verifies credentials against a fixed secret.
type stubChannel struct{ kind string }

func (s stubChannel) Kind() string { return s.kind }
func (s stubChannel) Verify(_ context.Context, _ domain.NotificationBot, cred botinteraction.Credential) (botinteraction.Verification, error) {
	if cred.Secret != "good" {
		return botinteraction.Verification{}, errors.New("钉钉拒绝了 Client ID 或 Client Secret（invalid）")
	}
	return botinteraction.Verification{}, nil
}
func (s stubChannel) Connect(context.Context, domain.NotificationBot, botinteraction.Credential, botinteraction.InboundSink) (botinteraction.Conn, error) {
	return nil, errors.New("not used")
}

func TestDingTalkBotSaveRequiresCorpIdAndVerifiesCredentials(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	nh := &notificationHandlers{deps: &deps{repo: f.s, engine: f.e}, service: notification.New(f.s)}
	nh.service.Interactions.RegisterChannel(stubChannel{botinteraction.ChannelDingTalk})
	create := func(body map[string]any) (int, map[string]any) {
		rec := cvBDirectHandlerRequest(t, f, http.MethodPost, "/api/notification-bots", body, nh.bots, nil, true)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	base := func() map[string]any {
		return map[string]any{"projectId": "platform", "name": "钉钉", "channel": "dingtalk", "botId": "ding-app", "secret": "good", "enabled": true, "interactionEnabled": true}
	}
	if code, out := create(base()); code != 400 || out["code"] != "tenant_required" {
		t.Fatalf("missing CorpId = %d %v", code, out)
	}
	bad := base()
	bad["tenantKey"], bad["secret"] = "ding-corp", "wrong"
	if code, out := create(bad); code != 400 || out["code"] != "credential_invalid" {
		t.Fatalf("bad secret = %d %v", code, out)
	}
	if bots, _ := f.s.NotificationBots("platform"); len(bots) != 0 {
		t.Fatalf("saved after a failed verification: %+v", bots)
	}
	auto := base()
	auto["tenantKey"], auto["writeApproval"] = "ding-corp", "auto"
	if code, _ := create(auto); code != 400 {
		t.Fatalf("auto write on DingTalk = %d", code)
	}
	badTemplate := base()
	badTemplate["tenantKey"] = "ding-corp"
	badTemplate["cardTemplateId"] = "tpl id"
	if code, _ := create(badTemplate); code != 400 {
		t.Fatalf("bad template id = %d", code)
	}
	ok := base()
	ok["tenantKey"] = " ding-corp "
	ok["writeApproval"] = "confirm"
	ok["cardTemplateId"] = " tpl-123.schema "
	code, out := create(ok)
	if code != 200 || out["tenantExpected"] != "ding-corp" || out["tenantState"] != domain.BotTenantAwaiting || out["writeApproval"] != "confirm" || out["credentialFormat"] != "encrypted" || out["cardTemplateId"] != "tpl-123.schema" {
		t.Fatalf("create = %d %v", code, out)
	}
	id := out["id"].(string)
	// Locked: the enterprise can no longer be changed.
	stored, _ := f.s.NotificationBot(id)
	if _, err := f.s.RecordBotTenantCandidate(id, "ding-corp", "e1", stored.CredentialVersion); err != nil {
		t.Fatal(err)
	}
	stored, _ = f.s.NotificationBot(id)
	update := map[string]any{"projectId": "platform", "name": "钉钉", "channel": "dingtalk", "botId": "ding-app", "enabled": true, "interactionEnabled": true, "tenantKey": "other-corp", "configVersion": stored.ConfigVersion}
	rec := cvBDirectHandlerRequest(t, f, http.MethodPut, "/api/notification-bots/"+id, update, nh.bot, map[string]string{"id": id}, true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "immutable_field") {
		t.Fatalf("locked CorpId change = %d %s", rec.Code, rec.Body.String())
	}
	update["tenantKey"] = "ding-corp"
	update["name"] = "钉钉改名"
	rec = cvBDirectHandlerRequest(t, f, http.MethodPut, "/api/notification-bots/"+id, update, nh.bot, map[string]string{"id": id}, true)
	if rec.Code != 200 {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body.String())
	}
}
