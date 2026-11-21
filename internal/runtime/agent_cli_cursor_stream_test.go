package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func cursorPartial(text string, ts int) string {
	payload, _ := json.Marshal(text)
	return fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":%s}]},"session_id":"s","timestamp_ms":%d}`, payload, ts)
}

func translateCursor(raws ...string) []string {
	translate := NewAgentCLITranslator(AgentCLICursor)
	var lines []string
	for _, raw := range raws {
		lines = append(lines, translate(raw)...)
	}
	return lines
}

func cursorVisibleText(lines []string) (deltas, blocks []string, result string) {
	for _, line := range lines {
		var ev struct {
			Type   string `json:"type"`
			Result string `json:"result"`
			Event  struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			} `json:"event"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type == "result" {
			result = ev.Result
		}
		if ev.Event.Delta.Type == "text_delta" {
			deltas = append(deltas, ev.Event.Delta.Text)
		}
		for _, block := range ev.Message.Content {
			if block.Type == "text" {
				blocks = append(blocks, block.Text)
			}
		}
	}
	return deltas, blocks, result
}

func TestCursorStreamDropsAutoRouteNoticeAndRepeatedProgress(t *testing.T) {
	sentence := "我先查看截图内容，确认你说的「不能展开」具体指哪一处，再给你简短回复。"
	route := "> Auto routed to GPT-5.6 Luna"
	accumulated := sentence + route + "\n" + sentence
	lines := translateCursor(
		cursorPartial(sentence, 1),
		cursorPartial(route+"\n", 2),
		cursorPartial(sentence, 3),
		cursorPartial(accumulated, 4),
		`{"type":"tool_call","subtype":"started","call_id":"c1","tool_call":{"readToolCall":{"args":{"path":"a"}}},"timestamp_ms":5}`,
		cursorPartial("Auto routed to Grok 4.7", 6),
		fmt.Sprintf(`{"type":"result","is_error":false,"result":%q}`, accumulated+"\nAuto routed to Grok 4.7"),
	)
	deltas, blocks, result := cursorVisibleText(lines)
	if strings.Join(deltas, "") != sentence {
		t.Fatalf("deltas = %#v", deltas)
	}
	if len(blocks) != 1 || strings.TrimSpace(blocks[0]) != sentence {
		t.Fatalf("blocks = %#v", blocks)
	}
	if strings.TrimSpace(result) != sentence || strings.Contains(result, "Auto routed") {
		t.Fatalf("result = %q", result)
	}
}

func TestCursorStreamKeepsRepeatedTokensThatAreNotAToolFlush(t *testing.T) {
	lines := translateCursor(
		cursorPartial("哈", 1),
		cursorPartial("哈", 2),
		cursorPartial("!", 3),
	)
	deltas, _, _ := cursorVisibleText(lines)
	if strings.Join(deltas, "") != "哈哈!" {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestCursorStreamKeepsIncrementsThatStartWithTheBuffer(t *testing.T) {
	lines := translateCursor(
		cursorPartial("哈", 1),
		cursorPartial("哈哈", 2),
		cursorPartial("！", 3),
	)
	deltas, _, _ := cursorVisibleText(lines)
	if strings.Join(deltas, "") != "哈哈哈！" {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestCursorStreamKeepsRepeatedLinesAwayFromAutoRouteNotice(t *testing.T) {
	text := "输出如下：\n```\nOK\nOK\n```\n日志里如果看到 `Auto routed to` 只是普通说明。"
	lines := translateCursor(
		cursorPartial("输出如下：\n```\nOK\n", 1),
		cursorPartial("OK\n```\n", 2),
		cursorPartial("日志里如果看到 `Auto routed to` 只是普通说明。", 3),
		fmt.Sprintf(`{"type":"result","is_error":false,"result":%q}`, text),
	)
	deltas, _, result := cursorVisibleText(lines)
	if strings.Join(deltas, "") != text {
		t.Fatalf("deltas = %#v", deltas)
	}
	if result != text {
		t.Fatalf("result = %q", result)
	}
}

func TestCursorStreamKeepsRepeatedLinesAfterTheRouteRepeat(t *testing.T) {
	text := "进度。> Auto routed to GPT-5.6 Luna\n进度。\nOK\nOK"
	if got := stripCursorAutoRoute(text); got != "进度。\nOK\nOK" {
		t.Fatalf("strip = %q", got)
	}
}

func TestCursorStreamDropsSplitNoticeAndRepeat(t *testing.T) {
	sentence := "我先查看截图内容，再回复。"
	lines := translateCursor(
		cursorPartial(sentence, 1),
		cursorPartial("> Auto rou", 2),
		cursorPartial("ted to GPT-5.6 Luna\n", 3),
		cursorPartial("我先查看", 4),
		cursorPartial("截图内容，再回复。", 5),
		cursorPartial("\n下一步。", 6),
	)
	deltas, _, _ := cursorVisibleText(lines)
	if got := strings.Join(deltas, ""); got != sentence+"\n下一步。" {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestCursorStreamKeepsProseAroundAutoRouteWords(t *testing.T) {
	for _, text := range []string{
		"比较 a > Auto routed to b 的情况",
		"说明：\nAuto routed to the fallback model when quota is exhausted.\n结束",
		"路由会显示 > Auto routed to GPT-5 这样的提示，然后继续。",
	} {
		if got := stripCursorAutoRoute(text); got != text {
			t.Errorf("strip(%q) = %q", text, got)
		}
		var raws []string
		for i, r := range []rune(text) {
			raws = append(raws, cursorPartial(string(r), i+1))
		}
		deltas, _, _ := cursorVisibleText(translateCursor(raws...))
		if got := strings.Join(deltas, ""); got != text {
			t.Errorf("deltas for %q = %q", text, got)
		}
	}
}

func TestCursorRouteFilterStreamsLikeStrip(t *testing.T) {
	for _, text := range []string{
		"进度。> Auto routed to GPT-5.6 Luna\n进度。\nOK\nOK",
		"A\n\n> Auto routed to Grok 4.7\n\n\nA\nB",
		"A\nAuto routed to Claude 4.5 Sonnet\n\nB\n",
		"x > y\n  > Auto routed to Composer 2.5  \nx > y",
	} {
		want := stripCursorAutoRoute(text)
		var f cursorRouteFilter
		var got strings.Builder
		for _, r := range text {
			streamed := f.feed(string(r))
			got.WriteString(streamed)
			if !strings.HasPrefix(want, got.String()) {
				t.Fatalf("%q: streamed %q is not a prefix of %q", text, got.String(), want)
			}
		}
		got.WriteString(f.finish())
		if got.String() != want || strings.Contains(want, "Auto routed") {
			t.Errorf("%q: streamed %q, strip %q", text, got.String(), want)
		}
	}
}

func TestCursorStreamKeepsProseThatMentionsAutoRoute(t *testing.T) {
	text := "请解释 Auto routed to 这句提示是什么意思"
	lines := translateCursor(cursorPartial(text, 1))
	deltas, _, _ := cursorVisibleText(lines)
	if strings.Join(deltas, "") != text {
		t.Fatalf("deltas = %#v", deltas)
	}
}
