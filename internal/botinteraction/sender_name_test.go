package botinteraction

import (
	"agentflow/internal/domain"
	"encoding/json"
	"strings"
	"testing"
)

// DingTalk is the only channel whose message carries the sender's display
// name. It is mapped onto the event and persisted with the interaction, so a
// chat-initiated platform action can be attributed to a person.
func TestDingTalkEventCarriesSenderName(t *testing.T) {
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelDingTalk, BotID: "ding-app"}
	ev, reason := dingtalkEvent(bot, dingtalkCallbackJSON("msg-n1", "1", "text", "建个需求", map[string]any{"senderNick": "  张三  "}), "frame-n1", officialDingTalkWebhook)
	if reason != "" || ev.SenderID != "staff-1" || ev.SenderName != "张三" {
		t.Fatalf("event = %+v %s", ev, reason)
	}
	item, drop := interactionFromEvent(bot, ev)
	if drop != "" || item.SenderName != "张三" {
		t.Fatalf("interaction = %+v %s", item, drop)
	}
	// A message without a nick must not invent one.
	bare, _ := dingtalkEvent(bot, dingtalkCallbackJSON("msg-n2", "1", "text", "x", nil), "frame-n2", officialDingTalkWebhook)
	if bare.SenderName != "" {
		t.Fatalf("sender name = %q", bare.SenderName)
	}
}

func TestDingTalkEventTruncatesSenderNameByCharacter(t *testing.T) {
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelDingTalk, BotID: "ding-app"}
	for _, count := range []int{64, domain.BotSenderNameMaxRunes + 1} {
		nick := strings.Repeat("张", count)
		ev, reason := dingtalkEvent(bot, dingtalkCallbackJSON("msg-long-nick", "1", "text", "建个需求", map[string]any{"senderNick": nick}), "frame-long-nick", officialDingTalkWebhook)
		item, drop := interactionFromEvent(bot, ev)
		want := strings.Repeat("张", min(count, domain.BotSenderNameMaxRunes))
		if reason != "" || drop != "" || item.SenderName != want {
			t.Errorf("name of %d characters = %q, reason = %q, drop = %q", count, item.SenderName, reason, drop)
		}
	}
}

// WeCom bot callbacks identify the sender by enterprise userid only, so no
// name is reported and the platform must not pretend otherwise.
func TestWeComEventReportsNoSenderName(t *testing.T) {
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelWeCom, BotID: "external"}
	body, _ := json.Marshal(map[string]any{
		"msgid": "m1", "aibotid": "external", "chatid": "c1", "chattype": "single",
		"from": map[string]string{"userid": "wangxx"}, "msgtype": "text",
		"text": map[string]string{"content": "建个需求"},
	})
	ev, ok := wecomEvent(bot, Frame{Cmd: "aibot_msg_callback", Body: body})
	if !ok || ev.SenderID != "wangxx" || ev.SenderName != "" {
		t.Fatalf("event = %+v ok=%t", ev, ok)
	}
	item, drop := interactionFromEvent(bot, ev)
	if drop != "" || item.SenderName != "" {
		t.Fatalf("interaction = %+v %s", item, drop)
	}
}
