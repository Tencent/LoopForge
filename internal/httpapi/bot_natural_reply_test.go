package httpapi

import (
	"agentflow/internal/domain"
	"context"
	"errors"
	"strings"
	"testing"
)

// Scope of the "Bots accept a natural-language final answer" change. Before
// it, a Bot turn only ended through submit_assistant_reply, which refuses a
// reply without drafts, so a plain query answer was retried twice and then
// failed with 助手未提交回复 (three model runs, the user got the generic
// failure text). The cases below pin what is now accepted and what still
// goes through the terminal retry, for read-only and legacy auto Bots alike.
func TestBotNaturalAnswerAcceptanceMatrix(t *testing.T) {
	const answer = "项目当前有 2 个运行中的 SOP，均在实现节点。"
	draftJSON := `{"message":"将创建需求","drafts":[{"type":"issues_save","payload":{"title":"x"}}]}`
	cases := []struct {
		name      string
		readOnly  bool
		outputs   []string
		cliErr    error
		wantCalls int
		wantReply string
		wantErr   bool
	}{
		{name: "answer accepted on first run (legacy auto)", outputs: []string{answer}, wantCalls: 1, wantReply: answer},
		{name: "answer accepted on first run (read-only)", readOnly: true, outputs: []string{answer}, wantCalls: 1, wantReply: answer},
		{name: "progress-only text reruns the terminal step", outputs: []string{"我先查询一下项目状态", answer}, wantCalls: 2, wantReply: answer},
		{name: "empty output reruns the terminal step", outputs: []string{"", answer}, wantCalls: 2, wantReply: answer},
		{name: "protocol JSON with drafts is not taken as text", outputs: []string{draftJSON, draftJSON, draftJSON}, wantCalls: 3, wantErr: true},
		{name: "protocol JSON without drafts is not taken as text", outputs: []string{`{"message":"已处理","drafts":[]}`, answer}, wantCalls: 2, wantReply: answer},
		{name: "output of a failed CLI run is never accepted", outputs: []string{answer, answer, answer}, cliErr: errors.New("exit status 1"), wantCalls: 3, wantErr: true},
		{name: "never answering still fails after three runs", outputs: []string{"", "", ""}, wantCalls: 3, wantErr: true},
		{name: "credentials in the answer are redacted", outputs: []string{"密钥是 other-key"}, wantCalls: 1, wantReply: "密钥是 [redacted]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, h, bot := botFixture(t)
			if tc.readOnly {
				bot.WriteApproval = domain.BotWriteReadOnly
			}
			calls := 0
			h.generate = func(ctx context.Context, prompt string, user domain.User) (string, error) {
				out := ""
				if calls < len(tc.outputs) {
					out = tc.outputs[calls]
				}
				calls++
				return out, tc.cliErr
			}
			reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{SessionID: "bot-session", ChatType: "single"}, nil, domain.BotInteraction{SenderID: "new-wecom-user", Content: "项目状态"})
			if calls != tc.wantCalls || (err != nil) != tc.wantErr {
				t.Fatalf("calls=%d err=%v reply=%q", calls, err, reply)
			}
			if !tc.wantErr && strings.TrimSpace(reply) != tc.wantReply {
				t.Fatalf("reply = %q", reply)
			}
		})
	}
}
