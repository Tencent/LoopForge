package workbuddy

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"agentflow/internal/gatemcp"
)

const gateQuestionMethod = "_codebuddy.ai/question"
const gateQuestionTool = "submit_gate_result"

type acpQuestionGate struct {
	directory string
	callID    string
	arguments string
	answer    string
}

func beginACPQuestionGate() (*GateAttempt, error) {
	directory, err := os.MkdirTemp("", "loopforge-wma-gate-")
	if err != nil {
		return nil, err
	}
	return &GateAttempt{Ticket: gatemcp.IssueTicket(nil), question: &acpQuestionGate{directory: directory}}, nil
}

func (g *acpQuestionGate) result() ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(g.directory, "result.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("WorkBuddy 未在本次 ACP 会话中提交有效的 Gate 终态")
	}
	return data, err
}

// GateQuestionPrefix binds the reverse ACP request to one attempt. The Agent
// supplies tool arguments over the existing authenticated outbound connection.
func (a *GateAttempt) GateQuestionPrefix() string {
	return gateQuestionTool + " " + a.Ticket + " "
}

func (a *GateAttempt) answerACPQuestion(sessionID string, params json.RawMessage) map[string]any {
	var request struct {
		SessionID  string `json:"sessionId"`
		ToolCallID string `json:"toolCallId"`
		InputType  string `json:"inputType"`
		Schema     struct {
			Questions []struct {
				Question string `json:"question"`
			} `json:"questions"`
		} `json:"schema"`
	}
	answers := map[string]string{"_action": "skip_question"}
	if json.Unmarshal(params, &request) == nil && request.SessionID == sessionID && request.ToolCallID != "" &&
		request.InputType == "question" && len(request.Schema.Questions) == 1 {
		if arguments, ok := strings.CutPrefix(request.Schema.Questions[0].Question, a.GateQuestionPrefix()); ok {
			answers = map[string]string{"q_0": a.question.submit(request.ToolCallID, strings.TrimSpace(arguments))}
		}
	}
	return map[string]any{"outcome": map[string]any{"outcome": "submitted", "data": map[string]any{"answers": answers}}}
}

func (g *acpQuestionGate) submit(callID, arguments string) string {
	if g.callID != "" {
		if callID == g.callID && arguments == g.arguments {
			return g.answer
		}
		return "Gate result already submitted."
	}
	const rejected = "Gate MCP rejected the result. Check the decision and reason."
	if len(arguments) > gatemcp.MaxReceiptPayload || !json.Valid([]byte(arguments)) {
		return rejected
	}
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": gateQuestionTool, "arguments": json.RawMessage(arguments)},
	})
	if err != nil {
		return rejected
	}
	var output bytes.Buffer
	if err := gatemcp.Run(bytes.NewReader(request), &output, filepath.Join(g.directory, "result.json")); err != nil {
		return rejected
	}
	var response struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if json.Unmarshal(output.Bytes(), &response) != nil || response.Result.IsError || len(response.Result.Content) != 1 {
		return rejected
	}
	g.callID, g.arguments, g.answer = callID, arguments, response.Result.Content[0].Text
	return g.answer
}
