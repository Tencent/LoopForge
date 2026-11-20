package botinteraction

import (
	"agentflow/internal/domain"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestFeishuPushCreatesOpenIDMessageWithUUID(t *testing.T) {
	var mu sync.Mutex
	var got struct {
		path, idType, receiveID, uuid, text string
	}
	code := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "t-token", "expire": 7200})
	})
	mux.HandleFunc("/open-apis/im/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiveID string `json:"receive_id"`
			MsgType   string `json:"msg_type"`
			Content   string `json:"content"`
			UUID      string `json:"uuid"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		var content struct {
			Text string `json:"text"`
		}
		json.Unmarshal([]byte(body.Content), &content)
		mu.Lock()
		got.path, got.idType, got.receiveID, got.uuid, got.text = r.URL.Path, r.URL.Query().Get("receive_id_type"), body.ReceiveID, body.UUID, content.Text
		c := code
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if c != 0 {
			json.NewEncoder(w).Encode(map[string]any{"code": c, "msg": "fail"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"message_id": "om_push"}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ch := newFeishuChannel()
	ch.base = srv.URL
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelFeishu, BotID: "cli_app"}
	msg := OutboundMessage{ChatID: "ou_target", Text: "SOP 已完成", SendKey: "send-key-for-test"}
	res := ch.Push(context.Background(), bot, Credential{Secret: "s"}, msg)
	if res.Outcome != Delivered || res.PlatformMessageID != "om_push" {
		t.Fatalf("push = %+v", res)
	}
	if got.idType != "open_id" || got.receiveID != "ou_target" || got.uuid != msg.SendKey || got.text != "SOP 已完成" {
		t.Fatalf("request = %+v", got)
	}
	code = 230013 // bot not available to the user
	if res := ch.Push(context.Background(), bot, Credential{Secret: "s"}, msg); res.Outcome != Rejected {
		t.Fatalf("rejected = %+v", res)
	}
	if res := ch.Push(context.Background(), bot, Credential{Secret: "s"}, OutboundMessage{Text: "x"}); res.Outcome != Rejected || res.Code != "target_missing" {
		t.Fatalf("no target = %+v", res)
	}
}

func TestDingTalkPushTokenCacheAndClassification(t *testing.T) {
	var mu sync.Mutex
	tokens, sends := 0, 0
	var lastBody map[string]any
	var answer func(w http.ResponseWriter)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokens++
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"accessToken": "at-1", "expireIn": 7200})
	})
	mux.HandleFunc("/v1.0/robot/oToMessages/batchSend", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-acs-dingtalk-access-token") != "at-1" {
			t.Errorf("token header = %q", r.Header.Get("x-acs-dingtalk-access-token"))
		}
		mu.Lock()
		sends++
		lastBody = map[string]any{}
		json.NewDecoder(r.Body).Decode(&lastBody)
		a := answer
		mu.Unlock()
		a(w)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ch := newDingTalkChannel()
	ch.api = srv.URL
	bot := domain.NotificationBot{ID: "cfg-push-test", Channel: domain.BotChannelDingTalk, BotID: "ding-client", CredentialVersion: 1}
	forgetDingTalkToken(bot)
	cred := Credential{Secret: "s"}
	ok := func(w http.ResponseWriter) {
		json.NewEncoder(w).Encode(map[string]any{"processQueryKey": "pqk-1", "invalidStaffIdList": []string{}, "flowControlledStaffIdList": []string{}})
	}
	answer = ok
	msg := OutboundMessage{ChatID: "staff-1", Text: "等待处理"}
	if res := ch.Push(context.Background(), bot, cred, msg); res.Outcome != Delivered || res.PlatformMessageID != "pqk-1" {
		t.Fatalf("push = %+v", res)
	}
	if lastBody["robotCode"] != "ding-client" || lastBody["msgKey"] != "sampleText" || !strings.Contains(lastBody["msgParam"].(string), "等待处理") {
		t.Fatalf("body = %v", lastBody)
	}
	if ids, _ := lastBody["userIds"].([]any); len(ids) != 1 || ids[0] != "staff-1" {
		t.Fatalf("userIds = %v", lastBody["userIds"])
	}
	ch.Push(context.Background(), bot, cred, msg)
	if tokens != 1 || sends != 2 {
		t.Fatalf("token not cached: tokens=%d sends=%d", tokens, sends)
	}
	cases := []struct {
		name   string
		answer func(http.ResponseWriter)
		want   SendOutcome
		code   string
	}{
		{"invalid staff", func(w http.ResponseWriter) {
			json.NewEncoder(w).Encode(map[string]any{"processQueryKey": "k", "invalidStaffIdList": []string{"staff-1"}})
		}, Rejected, "invalid_staff"},
		{"flow controlled", func(w http.ResponseWriter) {
			json.NewEncoder(w).Encode(map[string]any{"processQueryKey": "k", "flowControlledStaffIdList": []string{"staff-1"}})
		}, NotDelivered, "flow_controlled"},
		{"server error", func(w http.ResponseWriter) { w.WriteHeader(502) }, Unknown, "502"},
		{"rate limit", func(w http.ResponseWriter) { w.WriteHeader(429) }, NotDelivered, "429"},
		{"robot missing", func(w http.ResponseWriter) {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"code": "resource.not.found", "message": "robot 不存在"})
		}, Rejected, "resource.not.found"},
		{"no key", func(w http.ResponseWriter) { json.NewEncoder(w).Encode(map[string]any{}) }, Unknown, "unparsed"},
	}
	for _, tc := range cases {
		answer = tc.answer
		if res := ch.Push(context.Background(), bot, cred, msg); res.Outcome != tc.want || res.Code != tc.code {
			t.Errorf("%s = %+v", tc.name, res)
		}
	}
	// An invalid token is not delivered and is dropped from the cache.
	answer = func(w http.ResponseWriter) {
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]any{"code": "InvalidAuthentication"})
	}
	if res := ch.Push(context.Background(), bot, cred, msg); res.Outcome != NotDelivered || res.Code != "token_invalid" {
		t.Fatalf("token invalid = %+v", res)
	}
	answer = ok
	ch.Push(context.Background(), bot, cred, msg)
	if tokens != 2 {
		t.Fatalf("token not refreshed after 401: %d", tokens)
	}
	// A rotated credential uses a new token.
	bot.CredentialVersion = 2
	ch.Push(context.Background(), bot, cred, msg)
	if tokens != 3 {
		t.Fatalf("rotation reused token: %d", tokens)
	}
	if res := ch.Push(context.Background(), bot, cred, OutboundMessage{Text: "x"}); res.Code != "target_missing" {
		t.Fatalf("no target = %+v", res)
	}
}

func TestDingTalkPushTokenRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"code": "invalidClientSecret"})
	}))
	defer srv.Close()
	ch := newDingTalkChannel()
	ch.api = srv.URL
	bot := domain.NotificationBot{ID: "cfg-push-bad", Channel: domain.BotChannelDingTalk, BotID: "ding-client"}
	forgetDingTalkToken(bot)
	if res := ch.Push(context.Background(), bot, Credential{Secret: "bad"}, OutboundMessage{ChatID: "s", Text: "x"}); res.Outcome != Rejected || res.Code != "credential_invalid" {
		t.Fatalf("rejected token = %+v", res)
	}
	srv.Close()
	if res := ch.Push(context.Background(), bot, Credential{Secret: "s"}, OutboundMessage{ChatID: "s", Text: "x"}); res.Outcome != NotDelivered || !res.Retryable {
		t.Fatalf("token unreachable = %+v", res)
	}
}
