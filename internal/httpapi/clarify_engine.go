package httpapi

// clarify_engine.go 是需求澄清的引擎层：包级 helper 与 (streamHandlers) 方法都只服务
// 澄清这一个领域（AGENTS.md：一个文件不得为别的领域定义方法）。
//
// 澄清不再固定跑在 CodeBuddy 上。一个带引擎前缀的模型 ID（"codex:gpt-5.5"）同时表达
// 「用哪个智能体框架」和「用它的哪个模型」，前缀规则与 AI 对话共用 assistantModelEngine；
// 凭据取需求责任人在个人配置里对应那一项。
import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
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
// 缺 API Key、缺 Base URL 或模型非法都返回错误，由调用方原样显示给用户。改造前的
// 实现会把任何非法值换成 CodeBuddy 默认模型，那正是「澄清被绑死在 CodeBuddy」
// 的表现。空模型 ID 表示用户没有选择，沿用 CodeBuddy 默认。
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

// clarifyReadOnlyTools 是「只剩提交」重试里要关掉的读工具；CodeBuddy 与 Claude Code
// 用同名工具，其它引擎没有这个开关。
const clarifyReadOnlyTools = "Read,Grep,Glob"

// clarifyInvocationOpts 是一次澄清进程需要的输入。Root 是该用户专属的澄清会话目录，
// Config 是会话 MCP 网关的 CodeBuddy 风格配置，由各引擎按自己的格式改写。
type clarifyInvocationOpts struct {
	Bin, Root, Config, Settings, Prompt, Model, SystemPrompt, SessionID, RunID string
	Resume                                                                     bool
	SubmitOnly                                                                 bool
	User                                                                       runtime.ProviderCredential
}

// clarifyInvocation 是各引擎统一后的结果。凭据不在这里装配：executeClarify 按需求责任人
// 给出全套凭据，CommandSpec.CredentialBindings 决定哪一个真正被绑定。
type clarifyInvocation struct {
	Command   runtime.CommandSpec
	Translate func(string) []string // nil = 这个 CLI 本身就说 stream-json
	// ResumeMissing 表示这个引擎接不上上一次的会话（Codex 没记下 thread、agent CLI 的
	// native session 失效），调用方应改走「重放历史」的新会话。
	ResumeMissing bool
	// Finish 在进程退出后调用；ok 表示这一轮拿到了可用结果。
	Finish func(ok bool)
}

// prepareClarifyInvocation 为指定引擎准备好一条可执行命令：CodeBuddy 延用原有的澄清 argv，
// 其余引擎复用 AI 对话已经验证过的 builder。
func prepareClarifyInvocation(engine clarifyEngine, o clarifyInvocationOpts) (clarifyInvocation, error) {
	if engine.ID == assistantEngineCodeBuddy {
		command, err := prepareClarifyCommandWith(clarifyCommandOpts{
			Bin: o.Bin, Model: o.Model, Prompt: o.Prompt, RunID: o.RunID, SessionID: o.SessionID,
			Engine: engine.ID, Resume: o.Resume, Native: true,
			MCPConfig: o.Config, Settings: o.Settings, SubmitOnly: o.SubmitOnly,
		})
		if err != nil {
			return clarifyInvocation{}, err
		}
		return clarifyInvocation{Command: command}, nil
	}
	opts := assistantEngineInvocationOpts{
		Bin: o.Bin, Root: o.Root, Config: o.Config, Prompt: o.Prompt, Model: o.Model,
		SessionID: o.SessionID, SystemPrompt: o.SystemPrompt, Resume: o.Resume, User: o.User,
	}
	var (
		prepared assistantCLIInvocation
		err      error
	)
	switch {
	case engine.ID == assistantEngineClaude:
		prepared, err = prepareAssistantClaudeInvocation(opts)
	case engine.ID == assistantEngineCodex:
		prepared, err = prepareAssistantCodexInvocation(opts)
	case runtime.IsAgentCLI(engine.ID):
		prepared, err = prepareAssistantAgentCLIInvocation(engine.ID, opts)
	default:
		return clarifyInvocation{}, fmt.Errorf("%w：%s", errClarifyEngineUnknown, engine.ID)
	}
	if err != nil {
		return clarifyInvocation{}, err
	}
	if prepared.resumeMissing {
		return clarifyInvocation{ResumeMissing: true}, nil
	}
	invocation := clarifyInvocation{Command: prepared.command, Translate: prepared.translate, Finish: prepared.finish}
	if o.SubmitOnly && clarifySubmitOnlySupported(engine) {
		invocation.Command.Argv = appendDisallowedTools(invocation.Command.Argv, clarifyReadOnlyTools)
	}
	return invocation, nil
}

// clarifySubmitOnlySupported 判断这个引擎能否在重试时把工具收窄到只剩提交。
// Codex 的护栏是 PreToolUse 脚本、agent CLI 只有整体只读档，两者都没有逐工具开关。
func clarifySubmitOnlySupported(engine clarifyEngine) bool {
	return engine.ID == assistantEngineCodeBuddy || engine.ID == assistantEngineClaude
}

// errClarifySessionMissing 是这个引擎接不上上一次的会话：引擎换了、thread 没记下来、
// CLI 自己说找不到 conversation。调用方据此改为重放已有历史。
var errClarifySessionMissing = errors.New("澄清会话不存在，无法续接")

// clarifyChoice 决定这一轮澄清用哪个框架：本次请求的选择 > 这张单已经记住的框架 >
// 责任人的个人默认 > CodeBuddy。只有第一项不许回退——用户当场选的框架缺凭据就报错，
// 因为静默换回 CodeBuddy 正是这次要拆掉的东西。后两项是历史数据与偏好配置，失效了就用
// 默认值，由 /api/clarify-engines 以 defaultStale 报给界面。
func (h *streamHandlers) clarifyChoice(request chatClarifyRequest, issue domain.Issue, responsible domain.User) (clarifyEngine, error) {
	if raw := strings.TrimSpace(request.Model); raw != "" {
		return clarifyResolveModel(raw, responsible)
	}
	for _, fallback := range []string{
		strings.TrimSpace(issue.Clarification.ModelID),
		strings.TrimSpace(responsible.ClarifyModel),
	} {
		if fallback == "" {
			continue
		}
		if engine, err := clarifyResolveModel(fallback, responsible); err == nil {
			return engine, nil
		}
	}
	return clarifyResolveModel("", responsible)
}

// clarifyEngineCredential 从整套凭据里取出这个引擎那一份，供各引擎的 builder 使用。
func clarifyEngineCredential(credentials runtime.RunCredentials, engineID string) runtime.ProviderCredential {
	switch engineID {
	case assistantEngineCodex:
		return credentials.Codex
	case assistantEngineClaude:
		return credentials.ClaudeCode
	}
	if runtime.IsAgentCLI(engineID) {
		return credentials.AgentCLI[engineID]
	}
	return runtime.ProviderCredential{}
}

// clarifyEngineBin 定位这个引擎的可执行文件。Claude Code 不在镜像里，由平台在首次使用时
// 从 npm 安装（AI 对话也是同一处理），所以澄清走同一条路，不会因为「镜像没有」而失败。
func clarifyEngineBin(ctx context.Context, engineID string) (string, error) {
	if engineID == assistantEngineClaude {
		return runtime.ResolveClaudeCode(ctx)
	}
	return resolveAssistantEngineBin(engineID)
}

// clarifyToolVocabulary 给出这个引擎里「读代码的那几个工具」的说法。Read/Grep/Glob 是
// CodeBuddy 与 Claude Code 的工具名；其它引擎用了只会去调不存在的工具。
func clarifyToolVocabulary(engineID string) string {
	switch engineID {
	case "", assistantEngineCodeBuddy, assistantEngineClaude:
		return "Read/Grep/Glob"
	}
	return "内置的读文件和搜索工具"
}

// clarifyApplyToolVocabulary 把提示词里的 CodeBuddy 工具名换成当前引擎的说法。
func clarifyApplyToolVocabulary(text, engineID string) string {
	const codebuddyReadTools = "Read/Grep/Glob"
	vocabulary := clarifyToolVocabulary(engineID)
	if vocabulary == codebuddyReadTools {
		return text
	}
	return strings.ReplaceAll(text, codebuddyReadTools, vocabulary)
}

// clarifyEngineOption 是选择器里一个框架分组：能不能选、为什么不能选、能选哪些模型。
type clarifyEngineOption struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Configured bool             `json:"configured"`
	Available  bool             `json:"available"`
	Reason     string           `json:"reason,omitempty"`
	Credential string           `json:"credential"`
	Models     []assistantModel `json:"models,omitempty"`
}

// clarifyEngineCatalog 是按需求责任人算出来的整套可用性。
type clarifyEngineCatalog struct {
	ResponsibleUserID   int64                 `json:"responsibleUserId"`
	ResponsibleUserName string                `json:"responsibleUserName"`
	Default             string                `json:"default"`
	DefaultStale        bool                  `json:"defaultStale,omitempty"`
	Engines             []clarifyEngineOption `json:"engines"`
}

// clarifyEngines 告诉界面：这张单（或这个项目）的澄清能选哪些框架。可用性按**需求责任人**
// 判定——澄清是以责任人的凭据跑的，别人的 Key 不算数。接口只输出布尔状态、原因和模型 ID，
// 绝不带出任何 Key 或 Base URL。
func (h *streamHandlers) clarifyEngines(c *gin.Context) {
	if _, ok := h.requestUser(c.Request, false); !ok {
		c.JSON(http.StatusForbidden, map[string]string{"error": "请先登录"})
		return
	}
	responsible, code, message := h.clarifyEngineResponsible(c)
	if code != 0 {
		c.JSON(code, map[string]string{"error": message})
		return
	}
	_, shared := (&assistantHandlers{deps: h.deps}).assistantSharedKey()
	catalog := clarifyEngineCatalogFor(c.Request.Context(), responsible, shared && strings.TrimSpace(responsible.CodeBuddyAPIKey) == "")
	c.JSON(http.StatusOK, catalog)
}

// clarifyEngineResponsible 从 issueId 或 projectId 解析出需求责任人。
func (h *streamHandlers) clarifyEngineResponsible(c *gin.Context) (domain.User, int, string) {
	issueID := strings.TrimSpace(c.Query("issueId"))
	projectID := strings.TrimSpace(c.Query("projectId"))
	switch {
	case issueID != "":
		issue, ok := h.repo.Issue(issueID)
		if !ok {
			return domain.User{}, http.StatusNotFound, "需求不存在"
		}
		responsible, err := h.repo.ResponsibleUser(issue)
		if err != nil {
			return domain.User{}, http.StatusBadRequest, err.Error()
		}
		return responsible, 0, ""
	case projectID != "":
		if _, ok := h.repo.Project(projectID); !ok {
			return domain.User{}, http.StatusNotFound, "项目不存在"
		}
		responsible, err := h.repo.ResponsibleUser(domain.Issue{ProjectID: projectID})
		if err != nil {
			return domain.User{}, http.StatusBadRequest, err.Error()
		}
		return responsible, 0, ""
	}
	return domain.User{}, http.StatusBadRequest, "需要 issueId 或 projectId"
}

func clarifyEngineCatalogFor(ctx context.Context, responsible domain.User, sponsored bool) clarifyEngineCatalog {
	name := strings.TrimSpace(responsible.ChineseName)
	if name == "" {
		name = responsible.LoginName
	}
	catalog := clarifyEngineCatalog{
		ResponsibleUserID:   responsible.ID,
		ResponsibleUserName: name,
		Default:             clarifyDefaultModel,
		Engines:             clarifyEngineAvailability(ctx, responsible, sponsored),
	}
	// 个人默认失效不报错，只标记：由界面显式说明「已失效，当前用 CodeBuddy」。
	if raw := strings.TrimSpace(responsible.ClarifyModel); raw != "" {
		if engine, err := clarifyResolveModel(raw, responsible); err == nil {
			catalog.Default = engine.ModelID
		} else {
			catalog.DefaultStale = true
		}
	}
	return catalog
}

// clarifyEngineAvailability 列出平台提供的每个框架（配置与否都列），并算出它的可选模型。
func clarifyEngineAvailability(ctx context.Context, user domain.User, sponsored bool) []clarifyEngineOption {
	engineModels := assistantEngineModels(ctx, user)
	options := assistantEngineOptions(user, sponsored)
	out := make([]clarifyEngineOption, 0, len(options))
	for _, option := range options {
		entry := clarifyEngineOption{ID: option.ID, Name: option.Name, Configured: option.Configured, Credential: option.Credential}
		if option.ID == assistantEngineCodeBuddy {
			entry.Models = assistantKnownCatalog().Models
		} else {
			for _, model := range engineModels {
				if model.Engine == option.ID {
					entry.Models = append(entry.Models, model)
				}
			}
			// opencode / pi 的内置目录是空的，但允许自定义模型：给一项默认模型，
			// 不能因为目录空就把这个框架判死。
			if len(entry.Models) == 0 && executorModelCatalogs()[clarifyEngineCatalogKey(option.ID)].CustomModel {
				entry.Models = []assistantModel{{ID: option.ID + ":default", Name: "CLI 默认模型", Family: "other", Engine: option.ID}}
			}
		}
		entry.Reason = clarifyEngineUnavailableReason(option.ID, user, option.Configured, entry.Models)
		entry.Available = entry.Reason == ""
		out = append(out, entry)
	}
	return out
}

// clarifyEngineUnavailableReason 按「缺 Key → 缺 Base URL → 没装 CLI → 没有可选模型」的顺序
// 给出第一个原因；空串表示可用。Claude Code 不算「没装」：它不在镜像里，平台在首次使用时
// 从 npm 安装，装不上时由那一轮的错误说明原因。
func clarifyEngineUnavailableReason(engine string, user domain.User, configured bool, models []assistantModel) string {
	if !configured {
		return assistantEngineMissingKey(engine)
	}
	if _, baseURL := assistantEngineCredential(user, engine); runtime.AgentCLINeedsBaseURL(engine, assistantEngineProtocol(user, engine)) && baseURL == "" {
		return fmt.Sprintf("%s 需要在个人配置中填写 Base URL", assistantEngineLabel(engine))
	}
	if _, err := resolveAssistantEngineBin(engine); err != nil {
		if engine == assistantEngineClaude {
			return ""
		}
		return err.Error()
	}
	if len(models) == 0 {
		return "该框架暂无可用模型，请先在个人配置里添加"
	}
	return ""
}

// clarifyEngineCatalogKey 把引擎 ID 换成内置模型目录的键名（claude-code 那边叫 claude）。
func clarifyEngineCatalogKey(engine string) string {
	if engine == assistantEngineClaude {
		return "claude"
	}
	return engine
}
