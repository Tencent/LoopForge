package httpapi

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func codeBuddyUsageInvocation(t *testing.T, root, session string, resume bool, frames string, processErr error) []domain.TokenUsage {
	t.Helper()
	var got []domain.TokenUsage
	_, _, _ = runAssistantCLI(context.Background(), assistantCLIRequest{
		Prompt: "检查", SessionRoot: root, SessionID: session, Resume: resume, RemoteBinary: true,
		Execute: func(_ context.Context, _ string, _ runtime.CommandSpec, _ runtime.RunCredentials, sink runtime.EventSink) (string, error) {
			for _, line := range strings.Split(frames, "\n") {
				sink(domain.TelemetryEvent{Name: "process.stdout", Message: line})
			}
			return "", processErr
		},
		OnUsage: func(u domain.TokenUsage) { got = append(got, u) },
	}, domain.User{ID: 1, CodeBuddyAPIKey: "test"}, nil)
	return got
}

func TestCodeBuddyResumedUsageDoesNotRebillSession(t *testing.T) {
	root := t.TempDir()
	var total assistantTurnUsage
	for i, frame := range []string{
		`{"type":"result","result":"done","session_id":"native","usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":1000,"cache_creation_input_tokens":20}}`,
		`{"type":"result","result":"done","session_id":"native","usage":{"input_tokens":200,"output_tokens":20,"cache_read_input_tokens":2000,"cache_creation_input_tokens":40}}`,
		`{"type":"result","result":"done","session_id":"native","usage":{"input_tokens":300,"output_tokens":30,"cache_read_input_tokens":3000,"cache_creation_input_tokens":60}}`,
	} {
		got := codeBuddyUsageInvocation(t, root, "session", i > 0, frame, nil)
		if len(got) != 1 || got[0].InputTokens != 100 || got[0].OutputTokens != 10 || got[0].CacheReadTokens != 1000 || got[0].CacheWriteTokens != 20 {
			t.Fatalf("turn %d: %+v", i+1, got)
		}
		total.add(got[0])
	}
	if got := total.snapshot(); got.InputTokens != 300 || got.TotalTokens != 330 || got.CacheReadTokens != 3000 {
		t.Fatalf("session usage was charged repeatedly: %+v", got)
	}
}

func TestCodeBuddyTurnSnapshotOverridesCumulativeAndStages(t *testing.T) {
	frames := `{"type":"system","subtype":"usage_update","_meta":{"codebuddy.ai/turnUsage":{"version":1,"scope":"turn","kind":"snapshot","requestId":"historical","total":{"usage":{"input_tokens":9999}}}}}
{"type":"system","subtype":"usage_update","_meta":{"codebuddy.ai/turnUsage":{"version":1,"scope":"turn","kind":"stage","requestId":"current","total":{"usage":{"input_tokens":20,"output_tokens":3}}}}}
{"type":"result","result":"done","usage":{"input_tokens":1020,"output_tokens":103},"_meta":{"codebuddy.ai/turnUsage":{"version":1,"scope":"turn","kind":"snapshot","requestId":"current","total":{"usage":{"input_tokens":20,"output_tokens":3,"cache_read_input_tokens":400,"cache_creation_input_tokens":5}}}}}
{"type":"result","parent_tool_use_id":"child","usage":{"input_tokens":99999}}`
	got := codeBuddyUsageInvocation(t, t.TempDir(), "session", true, frames, nil)
	if len(got) != 1 || got[0].InputTokens != 20 || got[0].OutputTokens != 3 || got[0].CacheReadTokens != 400 || got[0].CacheWriteTokens != 5 {
		t.Fatalf("only the current root turn snapshot should be charged: %+v", got)
	}
	zero := `{"type":"result","result":"done","usage":{"input_tokens":1020},"_meta":{"codebuddy.ai/turnUsage":{"version":1,"scope":"turn","kind":"snapshot","total":{"usage":{"input_tokens":0,"output_tokens":0}}}}}`
	if got := codeBuddyUsageInvocation(t, t.TempDir(), "session", true, zero, nil); len(got) != 0 {
		t.Fatalf("a zero turn must not rebill history: %+v", got)
	}
}

func TestCodeBuddyMissingCursorEstablishesBaseline(t *testing.T) {
	root := t.TempDir()
	first := `{"type":"result","result":"done","usage":{"input_tokens":1000,"output_tokens":100}}`
	if got := codeBuddyUsageInvocation(t, root, "session", true, first, nil); len(got) != 0 {
		t.Fatalf("unknown historical baseline was charged: %+v", got)
	}
	next := `{"type":"result","result":"done","usage":{"input_tokens":1020,"output_tokens":103}}`
	if got := codeBuddyUsageInvocation(t, root, "session", true, next, nil); len(got) != 1 || got[0].InputTokens != 20 || got[0].OutputTokens != 3 {
		t.Fatalf("next turn did not use the recovered baseline: %+v", got)
	}
}

func TestCodeBuddyPartialFailureInvalidatesCursor(t *testing.T) {
	root := t.TempDir()
	codeBuddyUsageInvocation(t, root, "session", false, `{"type":"result","result":"done","usage":{"input_tokens":100}}`, nil)
	got := codeBuddyUsageInvocation(t, root, "session", true, `{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":20,"output_tokens":3}}}`, errors.New("process interrupted"))
	if len(got) != 1 || got[0].InputTokens != 20 {
		t.Fatalf("partial invocation lost usage: %+v", got)
	}
	// The next cumulative result includes the already charged partial turn.
	got = codeBuddyUsageInvocation(t, root, "session", true, `{"type":"assistant","message":{"id":"m2","usage":{"input_tokens":10}}}
{"type":"result","result":"done","usage":{"input_tokens":130,"output_tokens":3}}`, nil)
	if len(got) != 1 || got[0].InputTokens != 10 {
		t.Fatalf("partial usage was charged again: %+v", got)
	}
}

func TestCodeBuddyCursorResetAndSessionIsolation(t *testing.T) {
	root := t.TempDir()
	codeBuddyUsageInvocation(t, root, "a", false, `{"type":"result","result":"done","session_id":"native-a","usage":{"input_tokens":1000}}`, nil)
	for _, tc := range []struct{ session, frame string }{
		{"a", `{"type":"result","result":"done","session_id":"native-a","usage":{"input_tokens":10}}`},
		{"b", `{"type":"result","result":"done","session_id":"native-b","usage":{"input_tokens":500}}`},
		{"a", `{"type":"result","result":"done","session_id":"replacement","usage":{"input_tokens":700}}`},
	} {
		if got := codeBuddyUsageInvocation(t, root, tc.session, true, tc.frame, nil); len(got) != 0 {
			t.Fatalf("reset/foreign session has no reliable delta: %+v", got)
		}
	}
}

func TestCodeBuddyReportedDollarsUseSessionDelta(t *testing.T) {
	root := t.TempDir()
	for i, tc := range []struct {
		frame string
		cost  float64
	}{
		{`{"type":"result","result":"done","usage":{"input_tokens":100},"total_cost_usd":0.12}`, .12},
		{`{"type":"result","result":"done","usage":{"input_tokens":150},"total_cost_usd":0.20,"_meta":{"codebuddy.ai/turnUsage":{"version":1,"scope":"turn","kind":"snapshot","total":{"usage":{"input_tokens":50}}}}}`, .08},
		{`{"type":"result","result":"done","usage":{"input_tokens":175},"total_cost_usd":0.20,"_meta":{"codebuddy.ai/turnUsage":{"version":1,"scope":"turn","kind":"snapshot","total":{"usage":{"input_tokens":25}}}}}`, 0},
	} {
		got := codeBuddyUsageInvocation(t, root, "session", i > 0, tc.frame, nil)
		if len(got) != 1 || math.Abs(got[0].CostUSD-tc.cost) > 1e-10 || got[0].CostStatus != "reported" {
			t.Fatalf("turn %d: reported dollars were lost or rebilled: %+v", i, got)
		}
	}
}

func TestCodeBuddyCursorWriteFailureLeavesUsageUnmetered(t *testing.T) {
	c := newAssistantSessionUsage(t.TempDir()+"/missing", "session", false, assistantEngineCodeBuddy)
	u := domain.TokenUsage{InputTokens: 100, TotalTokens: 100}
	c.cumulative = &u
	if got := c.finish(domain.TokenUsage{}); assistantUsageReported(got) {
		t.Fatalf("an unpersisted cursor would rebill this usage next time: %+v", got)
	}
}

func TestAssistantMixedReportedAndEstimatedRetriesKeepTheirAmounts(t *testing.T) {
	var total assistantTurnUsage
	total.add(domain.PriceScheduleExecutionUsage("glm-5.3-flash", domain.TokenUsage{InputTokens: 1e6, OutputTokens: 1e6, CostUSD: .001, CostStatus: "reported"}))
	total.add(domain.PriceScheduleExecutionUsage("glm-5.3-flash", domain.TokenUsage{InputTokens: 1e6, OutputTokens: 1e6}))
	u := total.snapshot()
	want := .001 + 3.6/domain.AnalyticsUSDToCNY
	if u == nil || math.Abs(u.CostUSD-want) > 1e-10 || u.CostStatus != "estimated" {
		t.Fatalf("each retry must choose reported cost or fallback independently: %+v", u)
	}
	priced := domain.PriceScheduleExecutionUsage("glm-5.3-flash", *u)
	if math.Abs(priced.CostUSD-want) > 1e-10 {
		t.Fatalf("mixed total was repriced a second time: %+v", priced)
	}
	total.add(domain.PriceScheduleExecutionUsage("unknown-model", domain.TokenUsage{InputTokens: 1}))
	partial := domain.PriceScheduleExecutionUsage("glm-5.3-flash", *total.snapshot())
	if partial.CostStatus != "unknown" || math.Abs(partial.CostUSD-want) > 1e-10 {
		t.Fatalf("partial known amount must survive incomplete coverage: %+v", partial)
	}
}

func TestCodeBuddyPlaceholderZeroIsNotAFreeBill(t *testing.T) {
	got := codeBuddyUsageInvocation(t, t.TempDir(), "session", false, `{"type":"result","result":"done","usage":{"input_tokens":100,"output_tokens":10},"total_cost_usd":0}`, nil)
	if len(got) != 1 || got[0].CostStatus == "reported" {
		t.Fatalf("CodeBuddy's compatibility zero was treated as a monetary bill: %+v", got)
	}
	priced := domain.PriceScheduleExecutionUsage("claude-sonnet-4-6", got[0])
	if priced.CostStatus != "estimated" || priced.CostUSD <= 0 {
		t.Fatalf("missing monetary report must use public fallback: %+v", priced)
	}
}

func TestClaudeTurnTokensAndCumulativeDollarsHaveSeparateScopes(t *testing.T) {
	root := t.TempDir()
	for i, tc := range []struct {
		frame string
		input int64
		cost  float64
	}{
		{`{"type":"result","usage":{"input_tokens":100,"output_tokens":10},"total_cost_usd":0.12}`, 100, .12},
		{`{"type":"result","usage":{"input_tokens":20,"output_tokens":3},"total_cost_usd":0.20}`, 20, .08},
		{`{"type":"result","usage":{"input_tokens":10,"output_tokens":1},"total_cost_usd":0.20}`, 10, 0},
	} {
		collector := assistantCLIUsage{session: newAssistantSessionUsage(root, "claude-session", i > 0, assistantEngineClaude)}
		collector.consume(tc.frame)
		got := collector.snapshot()
		if got.InputTokens != tc.input || math.Abs(got.CostUSD-tc.cost) > 1e-10 || got.CostStatus != "reported" {
			t.Fatalf("turn %d: %+v", i, got)
		}
	}
}
