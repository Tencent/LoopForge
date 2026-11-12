package httpapi

// clarify_engine.go 是需求澄清的引擎层：包级 helper 与 (streamHandlers) 方法都只服务
// 澄清这一个领域（AGENTS.md：一个文件不得为别的领域定义方法）。
//
// 澄清不再固定跑在 CodeBuddy 上。一个带引擎前缀的模型 ID（"codex:gpt-5.5"）同时表达
// 「用哪个智能体框架」和「用它的哪个模型」，前缀规则与 AI 对话共用 assistantModelEngine；
// 凭据取需求责任人在个人配置里对应那一项。

import (
	"errors"
	"fmt"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// clarifyEngine 是一轮澄清所用的 CLI 与其模型。
type clarifyEngine struct {
	ID      string // "codebuddy" | "codex" | "claude-code" | agent CLI kind
	Model   string // 去掉引擎前缀后的模型，允许 "default"
	ModelID string // 对外使用的 ID：CodeBuddy 不带前缀，其余为 "<engine>:<model>"
}

// errClarifyEngineUnknown 表示模型 ID 声称了一个平台不认识的框架。
var errClarifyEngineUnknown = errors.New("未知的智能体框架")

// clarifyEngineOf 只做解析，不校验凭据。assistantModelEngine 只认识已有框架的前缀，
// 因此无法识别的 ID 一律落在 CodeBuddy 上，由 clarifyResolveModel 负责拒绝。
func clarifyEngineOf(modelID string) clarifyEngine {
	engine, model := assistantModelEngine(strings.TrimSpace(modelID))
	normalized := model
	if engine != assistantEngineCodeBuddy {
		normalized = engine + ":" + model
	}
	return clarifyEngine{ID: engine, Model: model, ModelID: normalized}
}

// clarifyEngineKnown 判断引擎是否在平台提供的框架全集里。assistantEngineOptions 会列出
// 全部框架（配置与否都列），所以传一个空用户即可当作目录使用。
func clarifyEngineKnown(engine string) bool {
	for _, option := range assistantEngineOptions(domain.User{}, false) {
		if option.ID == engine {
			return true
		}
	}
	return false
}

// clarifyResolveModel 按需求责任人的配置校验一个模型 ID。它绝不静默改写：框架不认识、
// 缺 API Key、缺 Base URL 或模型非法都返回错误，由调用方原样显示给用户。旧的
// clarifyAllowedModel 会把任何非法值换成 CodeBuddy 默认模型，那正是「澄清被绑死在
// CodeBuddy」的表现。空模型 ID 表示用户没有选择，沿用 CodeBuddy 默认。
func clarifyResolveModel(modelID string, user domain.User) (clarifyEngine, error) {
	raw := strings.TrimSpace(modelID)
	if raw == "" {
		return clarifyEngine{ID: assistantEngineCodeBuddy, Model: assistantDefaultModel, ModelID: assistantDefaultModel}, nil
	}
	engine, model := assistantModelEngine(raw)
	if engine == assistantEngineCodeBuddy {
		if model == assistantDefaultModel || codebuddyListsModel(model) {
			return clarifyEngine{ID: engine, Model: model, ModelID: raw}, nil
		}
		if prefix, _, found := strings.Cut(raw, ":"); found && !clarifyEngineKnown(prefix) {
			return clarifyEngine{}, fmt.Errorf("%w：%s", errClarifyEngineUnknown, prefix)
		}
		return clarifyEngine{}, fmt.Errorf("模型 %s 不在 CodeBuddy 目录中", raw)
	}
	apiKey, baseURL := assistantEngineCredential(user, engine)
	if apiKey == "" {
		return clarifyEngine{}, errors.New(assistantEngineMissingKey(engine))
	}
	if runtime.AgentCLINeedsBaseURL(engine, assistantEngineProtocol(user, engine)) && baseURL == "" {
		return clarifyEngine{}, fmt.Errorf("%s 需要在个人配置中填写 Base URL", assistantEngineLabel(engine))
	}
	if !assistantValidEngineModel(model) {
		return clarifyEngine{}, fmt.Errorf("模型 %s 无效", raw)
	}
	return clarifyEngine{ID: engine, Model: model, ModelID: engine + ":" + model}, nil
}

// codebuddyListsModel 检查模型是否在平台已知的 CodeBuddy 目录里。
func codebuddyListsModel(model string) bool {
	for _, listed := range assistantKnownCatalog().Models {
		if listed.ID == model {
			return true
		}
	}
	return false
}
