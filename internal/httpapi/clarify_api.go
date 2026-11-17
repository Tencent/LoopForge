package httpapi

import (
	"context"
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

func clarifyExecutionModel(user domain.User, model string) (string, error) {
	model = strings.TrimSpace(model)
	if err := clarifyModelEngineError(model); err != nil {
		return "", err
	}
	provider, name := assistantModelEngine(model)
	if provider == assistantEngineOpenAI {
		if strings.TrimSpace(user.OpenAIAPIKey) == "" {
			return "", fmt.Errorf("请先在个人配置中配置 OpenAI API Key")
		}
		if !assistantValidEngineModel(name) || name == "default" {
			return "", fmt.Errorf("请为 OpenAI API 选择具体模型")
		}
		return model, nil
	}
	return clarifyAllowedModel(model), nil
}

func runAPIClarifyTurn(ctx context.Context, model, prompt, sessionID string, emit func(string, any), opts clarifyTurnOpts) (any, string, string, error) {
	session := opts.Session
	output, captured, err := runAssistantModel(ctx, assistantCLIRequest{
		Model: model, Prompt: prompt, SessionID: sessionID, UserID: session.UserID,
		Native: true, MCPConfig: opts.MCPConfig,
	}, session.User, func(event assistantStreamEvent) {
		text := event.Text
		if text == "" {
			text = event.Tool
		}
		if text != "" {
			emit("thought", map[string]string{"kind": event.Kind, "text": text})
		}
	})
	return nil, output, captured, err
}
