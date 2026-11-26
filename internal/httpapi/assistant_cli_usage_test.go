package httpapi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestCodexCLIUsageAccumulatesAndSurvivesFailure(t *testing.T) {
	for _, totals := range []bool{false, true} {
		t.Run(fmt.Sprintf("totals_%v", totals), func(t *testing.T) {
			root := t.TempDir()
			for _, tc := range []struct {
				name                   string
				resume, failed, abrupt bool
				want                   int64
			}{
				{name: "two_calls", want: 140000},
				{name: "fixed_resume", resume: true, want: 30000},
				{name: "failed", resume: true, failed: true, want: 110000},
				{name: "process_exit", resume: true, abrupt: true, want: 110000},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var got []domain.TokenUsage
					execute := func(_ context.Context, _ string, command runtime.CommandSpec, _ runtime.RunCredentials, sink runtime.EventSink) (string, error) {
						stdin := bufio.NewReader(command.Stdin)
						send := func(line string) { sink(domain.TelemetryEvent{Name: "process.stdout", Message: line}) }
						readCodexTestRPC(t, stdin)
						send(`{"id":1,"result":{}}`)
						readCodexTestRPC(t, stdin)
						readCodexTestRPC(t, stdin)
						send(`{"id":2,"result":{"thread":{"id":"usage-thread"}}}`)
						readCodexTestRPC(t, stdin)
						send(`{"method":"turn/completed","params":{"threadId":"usage-thread","turn":{"id":"old-turn","status":"failed","error":{"message":"old failure"}}}}`)
						send(`{"method":"turn/started","params":{"threadId":"usage-thread","turn":{"id":"current-turn"}}}`)
						send(`{"id":3,"result":{"turn":{"id":"current-turn"}}}`)
						usage := func(thread, turn string, input, output, accumulated int64) {
							data := map[string]any{"last": codexTokenUsage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}}
							if totals {
								data["total"] = codexTokenUsage{InputTokens: accumulated - output, OutputTokens: output, TotalTokens: accumulated}
							}
							send(codexLine(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": data}}))
						}
						usage("other-thread", "current-turn", 999000, 1000, 1000000)
						usage("usage-thread", "old-turn", 999000, 1000, 1000000)
						if tc.name == "fixed_resume" {
							usage("usage-thread", "current-turn", 20000, 10000, 170000)
						} else {
							usage("usage-thread", "current-turn", 100000, 10000, 110000)
							if tc.name == "two_calls" {
								if totals {
									usage("usage-thread", "current-turn", 100000, 10000, 110000)
									send(`{"method":"thread/tokenUsage/updated","params":{"threadId":"usage-thread","turnId":"current-turn","tokenUsage":{"last":{"inputTokens":20000,"outputTokens":10000,"totalTokens":30000},"total":{"inputTokens":120000,"outputTokens":20000,"totalTokens":140000}}}}`)
								} else {
									usage("usage-thread", "current-turn", 20000, 10000, 140000)
								}
							}
						}
						if tc.abrupt {
							return "", errors.New("process exited")
						}
						if tc.failed {
							send(`{"method":"turn/completed","params":{"threadId":"usage-thread","turn":{"id":"current-turn","status":"failed","error":{"message":"request failed"}}}}`)
						} else {
							send(`{"method":"item/completed","params":{"threadId":"usage-thread","turnId":"current-turn","item":{"id":"answer","type":"agentMessage","text":"检查完成。"}}}`)
							send(`{"method":"turn/completed","params":{"threadId":"usage-thread","turn":{"id":"current-turn","status":"completed"}}}`)
						}
						return "", nil
					}
					_, _, err := runAssistantCLI(context.Background(), assistantCLIRequest{Model: "codex:gpt-5.5", Native: true, RemoteBinary: true, SessionRoot: root, SessionID: "fixed-session", Resume: tc.resume, Execute: execute, OnUsage: func(u domain.TokenUsage) { got = append(got, u) }}, domain.User{ID: 1, CodexAPIKey: "test"}, nil)
					if (err != nil) != (tc.failed || tc.abrupt) {
						t.Fatalf("err=%v", err)
					}
					if len(got) != 1 || got[0].TotalTokens != tc.want {
						t.Fatalf("usage=%+v want=%d", got, tc.want)
					}
				})
			}
		})
	}
}

func TestCodeBuddyUsageSnapshotsSurviveErrors(t *testing.T) {
	for _, tc := range []struct {
		name, frames         string
		failed               bool
		input, output, cache int64
	}{
		{name: "anonymous_complete_messages", frames: `{"type":"assistant","message":{"usage":{"input_tokens":10}}}
{"type":"assistant","message":{"usage":{"input_tokens":20}}}
{"type":"error","error":"failed"}`, failed: true, input: 30},
		{name: "anonymous_stream_complete", frames: `{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":10}}}}
{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":10}}}
{"type":"error","error":"failed"}`, failed: true, input: 10},
		{name: "result_cache_failure", frames: `{"type":"result","is_error":true,"result":"request failed","usage":{"cache_read_input_tokens":40000}}`, failed: true, cache: 40000},
		{name: "stream_cache_failure", frames: "{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"cache_read_input_tokens\":40000}}}}\n{\"type\":\"error\",\"error\":\"request failed\"}", failed: true, cache: 40000},
		{name: "terminal_snapshot", frames: "{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}}\n{\"type\":\"stream_event\",\"event\":{\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}}\n{\"type\":\"assistant\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n{\"type\":\"result\",\"result\":\"完成\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}", input: 10, output: 5},
		{name: "missing_terminal_usage", frames: "{\"type\":\"assistant\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n{\"type\":\"assistant\",\"message\":{\"id\":\"m2\",\"usage\":{\"input_tokens\":20,\"output_tokens\":6}}}\n{\"type\":\"result\",\"result\":\"完成\"}", input: 30, output: 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := dir + "/codebuddy"
			script := "#!/bin/sh\ncat <<'JSON'\n" + tc.frames + "\nJSON\n"
			if tc.failed {
				script += "exit 1\n"
			}
			if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
			var got []domain.TokenUsage
			_, _, err := runAssistantCLI(context.Background(), assistantCLIRequest{Prompt: "检查", SessionRoot: dir, OnUsage: func(u domain.TokenUsage) { got = append(got, u) }}, domain.User{ID: 1, CodeBuddyAPIKey: "test"}, nil)
			if (err != nil) != tc.failed {
				t.Fatalf("err=%v", err)
			}
			if len(got) != 1 || got[0].InputTokens != tc.input || got[0].OutputTokens != tc.output || got[0].CacheReadTokens != tc.cache {
				t.Fatalf("usage=%+v", got)
			}
		})
	}
}
