package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSubmitClarificationQuestionTypes(t *testing.T) {
	for _, tc := range []struct {
		name, question, wantError string
	}{
		{"choice", `{"id":"q1","question":"ID 怎么输入？","options":["命令行","文件"]}`, ""},
		{"multi choice", `{"id":"q1","question":"要更新哪些字段？","options":["push_time","retry"],"multiSelect":true}`, ""},
		{"missing options", `{"id":"q1","question":"ID 怎么输入？"}`, "两个不同"},
		{"empty options", `{"id":"q1","question":"ID 怎么输入？","options":[]}`, "两个不同"},
		{"blank options", `{"id":"q1","question":"ID 怎么输入？","options":[" ",""]}`, "两个不同"},
		{"duplicate options", `{"id":"q1","question":"ID 怎么输入？","options":["文件"," 文件 "]}`, "两个不同"},
		{"free text", `{"id":"q1","question":"文件路径是什么？","options":[],"freeText":true}`, ""},
		{"free text with choices", `{"id":"q1","question":"文件路径是什么？","options":["A","B"],"freeText":true}`, "options=[]"},
		{"free text multi", `{"id":"q1","question":"文件路径是什么？","options":[],"freeText":true,"multiSelect":true}`, "multiSelect=false"},
		{"missing question text", `{"id":"q1","question":"","options":["A","B"]}`, "问题文本为空"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"summary":"需要用户确认","ready":false,"questions":[` + tc.question + `]}`)
			_, err := parseSubmitClarificationArgs(raw)
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("want error containing %q, got %v", tc.wantError, err)
			}
		})
	}
	if _, err := parseSubmitClarificationArgs(json.RawMessage(`{"summary":"信息齐全，可以开工","ready":true,"questions":[]}`)); err != nil {
		t.Fatalf("ready result does not need questions: %v", err)
	}
}

func TestInvalidClarificationCanBeCorrectedInSameSession(t *testing.T) {
	session := &AgentToolSession{Mode: AgentToolClarify, Final: make(chan AgentTerminalResult, 1)}
	gateway := &agentToolGateway{}
	invalid := json.RawMessage(`{"summary":"业务原因未明确","ready":false,"questions":[],"assessment":{"missing":["业务原因未明确"]}}`)
	result, err := gateway.callTerminal(session, submitClarificationTool, invalid)
	if err != nil || result["isError"] != true {
		t.Fatalf("validation must be a recoverable tool error: %v %v", result, err)
	}
	if session.finished() {
		t.Fatal("invalid submission must not finish the session")
	}
	if _, ok := session.takeFinal(); ok {
		t.Fatal("invalid submission must not produce a UI result")
	}
	corrected := json.RawMessage(`{"summary":"需要确认修复目的","ready":false,"questions":[{"id":"q1","question":"修复目的是什么？","options":["仅修正本地状态","重新推送账单"],"multiSelect":false}]}`)
	result, err = gateway.callTerminal(session, submitClarificationTool, corrected)
	if err != nil || result["isError"] == true {
		t.Fatalf("corrected submission failed: %v %v", result, err)
	}
	final, ok := session.takeFinal()
	if !ok || final.Clarification == nil || len(final.Clarification.Questions) != 1 || len(final.Clarification.Questions[0].Options) != 2 {
		t.Fatalf("corrected questions must reach the UI: %+v", final)
	}
}
