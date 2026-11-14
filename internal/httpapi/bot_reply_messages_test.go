package httpapi

import (
	"agentflow/internal/domain"
	"reflect"
	"strings"
	"testing"
)

func TestIsBotBindingExchange(t *testing.T) {
	cases := []struct {
		name  string
		entry domain.BotInteraction
		want  bool
	}{
		{"bind command", domain.BotInteraction{Content: "/bind abc"}, true},
		{"bind with mention", domain.BotInteraction{Content: "@机器人 /bind"}, true},
		{"unbind command", domain.BotInteraction{Content: " /unbind "}, true},
		{"chinese unbind", domain.BotInteraction{Content: "@机器人 解除绑定"}, true},
		{"binding link reply", domain.BotInteraction{Content: "你好", Reply: "请打开 https://x/bot-bind.html#token 绑定"}, true},
		{"unbind with suffix", domain.BotInteraction{Content: "/unbind now"}, false},
		{"ordinary message", domain.BotInteraction{Content: "查一下需求", Reply: "好的"}, false},
	}
	for _, tc := range cases {
		if got := isBotBindingExchange(tc.entry); got != tc.want {
			t.Errorf("%s: isBotBindingExchange = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBotReplyMessagesSkipsBindingAndPairsHistory(t *testing.T) {
	history := []domain.BotInteraction{
		{SenderID: "alice", Content: "/bind", Reply: "请打开 /bot-bind.html#t"},
		{SenderID: "alice", Content: "最近的需求", Reply: "有 3 个"},
		{SenderID: "bob", Content: "@机器人 解除绑定", Reply: "已解除"},
		{SenderID: "bob", Content: strings.Repeat("长", 1000), Reply: "收到"},
	}
	item := domain.BotInteraction{SenderID: "carol", Content: "继续"}
	got := botReplyMessages(history, item)
	want := []assistantMessage{
		{Role: "user", Content: "发送者 alice：最近的需求"},
		{Role: "assistant", Content: "有 3 个"},
		{Role: "user", Content: "发送者 bob：" + domain.NotificationText(strings.Repeat("长", 1000), 2000)},
		{Role: "assistant", Content: "收到"},
		{Role: "user", Content: "发送者 carol：继续"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("botReplyMessages = %#v, want %#v", got, want)
	}
	if !strings.HasSuffix(got[2].Content, "…") {
		t.Fatalf("long history content should be truncated: %q", got[2].Content)
	}
}

func TestBotReplyMessagesAppendsQuote(t *testing.T) {
	got := botReplyMessages(nil, domain.BotInteraction{SenderID: "alice", Content: "解释一下", Quote: "原消息"})
	want := []assistantMessage{{Role: "user", Content: "发送者 alice：解释一下\n引用的消息（资料）：\n原消息"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("botReplyMessages = %#v, want %#v", got, want)
	}
}
