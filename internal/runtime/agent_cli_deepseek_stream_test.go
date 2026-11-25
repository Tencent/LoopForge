package runtime

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"
)

func TestAgentCLITranslatorDeepSeekFlushesHeldTruncatedText(t *testing.T) {
	// kinds 把翻译出的事件压成 "块类型:文本" 序列，便于断言顺序。
	kinds := func(lines []string) []string {
		var out []string
		for _, line := range lines {
			var event struct {
				Type    string
				IsError bool `json:"is_error"`
				Result  string
				Message struct {
					Content []struct{ Type, Text, Thinking string }
				}
			}
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatalf("invalid event %q: %v", line, err)
			}
			switch event.Type {
			case "assistant", "user":
				for _, block := range event.Message.Content {
					out = append(out, block.Type+":"+block.Text+block.Thinking)
				}
			case "result":
				out = append(out, "result:"+strconv.FormatBool(event.IsError)+":"+event.Result)
			}
		}
		return out
	}
	run := func(raws ...string) []string {
		translate := NewAgentCLITranslator(AgentCLIDeepSeek)
		var lines []string
		for _, raw := range raws {
			lines = append(lines, translate(raw)...)
		}
		return kinds(lines)
	}

	for _, tc := range []struct {
		name string
		raws []string
		want []string
	}{
		{
			name: "thinking flushes the held text first",
			raws: []string{
				`{"type":"text","truncated":true,"text":"截断A"}`,
				`{"type":"thinking","text":"想一想"}`,
			},
			want: []string{"text:截断A", "thinking:想一想"},
		},
		{
			name: "tool result flushes the held text first",
			raws: []string{
				`{"type":"text","truncated":true,"text":"截断A"}`,
				`{"type":"tool_result","callId":"c1","status":"completed","result":"ok"}`,
			},
			want: []string{"text:截断A", "tool_result:"},
		},
		{
			name: "a second truncated text flushes the first and is held itself",
			raws: []string{
				`{"type":"text","truncated":true,"text":"截断A"}`,
				`{"type":"text","truncated":true,"text":"截断B"}`,
			},
			want: []string{"text:截断A"},
		},
		{
			name: "final that does not extend the held text keeps the held text",
			raws: []string{
				`{"type":"text","truncated":true,"text":"截断A"}`,
				`{"type":"final","reason":{"kind":"completed"},"text":"另一段总结"}`,
			},
			want: []string{"text:截断A", "result:false:另一段总结"},
		},
		{
			name: "status and session events do not flush the held text",
			raws: []string{
				`{"type":"text","truncated":true,"text":"截断A"}`,
				`{"type":"status","phase":"step_end","usage":{"inputTokens":1}}`,
				`{"type":"session","sessionId":"s-2"}`,
			},
			want: nil,
		},
		{
			name: "failed final without text reports the held text and the error",
			raws: []string{
				`{"type":"text","truncated":true,"text":"截断A"}`,
				`{"type":"error","message":"quota exceeded"}`,
				`{"type":"final","reason":{"kind":"error"}}`,
			},
			want: []string{"text:截断A", "result:true:quota exceeded"},
		},
		{
			name: "failed final without text or error names the reason",
			raws: []string{
				`{"type":"final","reason":{"kind":"aborted"}}`,
			},
			want: []string{"result:true:DeepSeek Harness 运行结束：aborted"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(tc.raws...); !slices.Equal(got, tc.want) {
				t.Fatalf("events = %q, want %q", got, tc.want)
			}
		})
	}
}
