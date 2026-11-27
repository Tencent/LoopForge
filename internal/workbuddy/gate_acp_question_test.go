package workbuddy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestACPQuestionGateUsesMCPAndScopesSubmissionToAttempt(t *testing.T) {
	t.Setenv("AGENTFLOW_WMA_GATE_ACP_QUESTION", "1")
	ctx := context.Background()
	gate, err := BeginGate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Discard(ctx)
	const decision = `{"decision":"pass","reason":"artifact registered"}`
	question := func(session, callID, text string) map[string]string {
		t.Helper()
		params, err := json.Marshal(map[string]any{
			"sessionId": session, "toolCallId": callID, "inputType": "question",
			"schema": map[string]any{"questions": []map[string]string{{"question": text}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		response := gate.answerACPQuestion("session-1", params)
		return response["outcome"].(map[string]any)["data"].(map[string]any)["answers"].(map[string]string)
	}
	if _, err := gate.Result(ctx, Turn{Text: decision}); err == nil {
		t.Fatal("text must not count as a Gate submission")
	}
	if answer := question("session-2", "call-1", gate.GateQuestionPrefix()+decision); answer["_action"] != "skip_question" {
		t.Fatalf("request from another session = %v", answer)
	}
	if answer := question("session-1", "call-1", "submit_gate_result other-ticket "+decision); answer["_action"] != "skip_question" {
		t.Fatalf("request from another attempt = %v", answer)
	}
	if answer := question("session-1", "call-1", gate.GateQuestionPrefix()+`{"decision":"human","reason":"missing confirmation"}`); !strings.Contains(answer["q_0"], "rejected") {
		t.Fatalf("invalid MCP arguments = %v", answer)
	}
	answer := question("session-1", "call-2", gate.GateQuestionPrefix()+decision)["q_0"]
	if !strings.Contains(answer, "Gate result submitted") {
		t.Fatalf("valid MCP arguments = %s", answer)
	}
	if repeated := question("session-1", "call-2", gate.GateQuestionPrefix()+decision)["q_0"]; repeated != answer {
		t.Fatalf("replayed request received a different response = %s", repeated)
	}
	if repeated := question("session-1", "call-3", gate.GateQuestionPrefix()+`{"decision":"fail","reason":"override"}`)["q_0"]; !strings.Contains(repeated, "already submitted") {
		t.Fatalf("second submission = %s", repeated)
	}
	result, err := gate.Result(ctx, Turn{})
	if err != nil || !strings.Contains(string(result), `"decision":"pass"`) {
		t.Fatalf("stored Gate result = %s, %v", result, err)
	}
}
