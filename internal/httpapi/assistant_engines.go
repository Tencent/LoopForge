package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// The assistant stores CLI-prefixed model IDs. Bare concrete IDs from older
// CodeBuddy sessions retain the CodeBuddy interpretation.
const (
	assistantEngineCodeBuddy = "codebuddy"
	assistantEngineCodex     = "codex"
	assistantEngineClaude    = "claude-code"
)

// assistantCLIEngines are the CLIs other than CodeBuddy, in switch order.
func assistantCLIEngines() []string {
	return append([]string{assistantEngineCodex, assistantEngineClaude}, runtime.AgentCLIKinds...)
}

func assistantModelEngine(id string) (engine, model string) {
	id = strings.TrimSpace(id)
	if rest, ok := strings.CutPrefix(id, assistantEngineCodeBuddy+":"); ok {
		return assistantEngineCodeBuddy, strings.TrimSpace(rest)
	}
	for _, engine := range assistantCLIEngines() {
		if rest, ok := strings.CutPrefix(id, engine+":"); ok {
			return engine, strings.TrimSpace(rest)
		}
	}
	return assistantEngineCodeBuddy, id
}

// assistantEngineOption is one entry of the chat composer's CLI switch.
// Credential names the 个人配置 field that enables it.
type assistantEngineOption struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
	Personal   bool   `json:"personal"`
	Credential string `json:"credential"`
}

// assistantEngineCredentialField is the 个人配置 field that enables a CLI.
func assistantEngineCredentialField(engine string) string {
	switch engine {
	case assistantEngineCodeBuddy:
		return "codeBuddyApiKey"
	case assistantEngineCodex:
		return "codexApiKey"
	case assistantEngineClaude:
		return "claudeCodeApiKey"
	}
	return "agentCli." + engine
}

// assistantEngineOptions lists every CLI, configured or not, so the switch
// also shows where to add a key. CodeBuddy counts as configured when the
// platform sponsors chats without one.
func assistantEngineOptions(user domain.User, sponsored bool) []assistantEngineOption {
	engines := append([]string{assistantEngineCodeBuddy}, assistantCLIEngines()...)
	out := make([]assistantEngineOption, 0, len(engines))
	for _, engine := range engines {
		key, _ := assistantEngineCredential(user, engine)
		out = append(out, assistantEngineOption{
			ID:         engine,
			Name:       assistantEngineLabel(engine),
			Configured: key != "" || (engine == assistantEngineCodeBuddy && sponsored),
			Personal:   key != "",
			Credential: assistantEngineCredentialField(engine),
		})
	}
	return out
}

func assistantEngineLabel(engine string) string {
	switch engine {
	case assistantEngineCodex:
		return "Codex"
	case assistantEngineClaude:
		return "Claude Code"
	case assistantEngineCodeBuddy, "":
		return "CodeBuddy"
	}
	return runtime.AgentCLILabel(engine)
}

func assistantEngineCredential(user domain.User, engine string) (apiKey, baseURL string) {
	switch engine {
	case assistantEngineCodex:
		return strings.TrimSpace(user.CodexAPIKey), strings.TrimSpace(user.CodexBaseURL)
	case assistantEngineClaude:
		return strings.TrimSpace(user.ClaudeCodeAPIKey), strings.TrimSpace(user.ClaudeCodeBaseURL)
	case assistantEngineCodeBuddy:
		return strings.TrimSpace(user.CodeBuddyAPIKey), strings.TrimSpace(user.CodeBuddyBaseURL)
	}
	cred := user.AgentCLICredentials[engine]
	if runtime.IsAgentCLI(engine) && engine != runtime.AgentCLICursor {
		baseURL = strings.TrimSpace(cred.BaseURL)
	}
	return strings.TrimSpace(cred.APIKey), baseURL
}

// assistantEngineProtocol is the API protocol an agent CLI speaks; "" for
// the other CLIs.
func assistantEngineProtocol(user domain.User, engine string) string {
	if !runtime.IsAgentCLI(engine) {
		return ""
	}
	return runtime.AgentCLIProtocol(engine, user.AgentCLICredentials[engine].Protocol)
}

// assistantEngineAnthropicModels reports whether the model list behind a
// Base URL is the Anthropic one ({base}/v1/models, x-api-key).
func assistantEngineAnthropicModels(engine, protocol string) bool {
	return engine == assistantEngineClaude || protocol == runtime.AgentCLIProtocolMessages
}

func antigravityUsesCustomEndpoint(user domain.User) bool {
	_, baseURL := assistantEngineCredential(user, runtime.AgentCLIAntigravity)
	return baseURL != "" || assistantEngineProtocol(user, runtime.AgentCLIAntigravity) == runtime.AgentCLIProtocolChat
}

func assistantEngineCustomModels(user domain.User, engine string) []domain.CLIModel {
	switch engine {
	case assistantEngineCodeBuddy:
		return user.CodeBuddyModels
	case assistantEngineCodex:
		return user.CodexModels
	case assistantEngineClaude:
		return user.ClaudeCodeModels
	}
	return user.AgentCLICredentials[engine].Models
}

// assistantEngineBuiltinModels is the executor catalog for a CLI, used on the
// official endpoint (no Base URL) or when the Base URL lists no model.
func assistantEngineBuiltinModels(engine string) []assistantDiscoveredModel {
	catalogKey := engine
	if engine == assistantEngineClaude {
		catalogKey = "claude"
	}
	var out []assistantDiscoveredModel
	for _, model := range executorModelCatalogs("", assistantFixedCatalog())[catalogKey].Models {
		out = append(out, assistantDiscoveredModel{ID: model.ID, Name: model.Name})
	}
	return out
}

// assistantValidEngineModel guards model IDs that reach a CLI argument or a
// TOML config override.
func assistantValidEngineModel(model string) bool {
	if model == "" || len(model) > 128 || strings.HasPrefix(model, "-") {
		return false
	}
	for _, r := range model {
		if r <= ' ' || r == 0x7f || r == '"' || r == '\'' || r == '`' || r == '\\' {
			return false
		}
	}
	return true
}

// assistantConversationEngine is the CLI that wrote the conversation's latest
// own reply, "" before the first one. Role replies (SpeakerAgentID) run on
// their own executors and do not count. A turn that failed before producing
// anything leaves no reply, so it does not pin the CLI.
//
// Replies saved before model prefixes carry the CLI on the session Executor
// (legacyExecutor) instead. An unprefixed model is not itself proof of CodeBuddy.
func assistantConversationEngine(messages []assistantMessage, legacyExecutor ...string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != "assistant" || m.SpeakerAgentID != "" || strings.TrimSpace(m.Model) == "" {
			continue
		}
		if assistantModelHasEnginePrefix(m.Model) {
			engine, _ := assistantModelEngine(m.Model)
			return engine
		}
		if legacy := assistantLegacyExecutor(legacyExecutor...); legacy != "" && legacy != assistantEngineCodeBuddy {
			return legacy
		}
		engine, _ := assistantModelEngine(m.Model)
		return engine
	}
	return ""
}

func assistantModelHasEnginePrefix(id string) bool {
	id = strings.TrimSpace(id)
	for _, engine := range append([]string{assistantEngineCodeBuddy}, assistantCLIEngines()...) {
		if _, ok := strings.CutPrefix(id, engine+":"); ok {
			return true
		}
	}
	return false
}

// assistantLegacyExecutor maps a session Executor saved before model prefixes.
// CodeBuddy and an empty value stay unset so an unprefixed model remains CodeBuddy.
func assistantLegacyExecutor(candidates ...string) string {
	for _, raw := range candidates {
		switch id := strings.ToLower(strings.TrimSpace(raw)); id {
		case assistantEngineCodex:
			return assistantEngineCodex
		case "claude", assistantEngineClaude:
			return assistantEngineClaude
		case "", assistantEngineCodeBuddy:
			continue
		default:
			for _, kind := range domain.AgentCLIExecutorKinds {
				if strings.EqualFold(kind, id) {
					return kind
				}
			}
		}
	}
	return ""
}

// assistantEngineSwitchRefusal keeps a conversation on one CLI. Its native
// session holds tool calls and reasoning that no other CLI can resume, so a
// switch would silently drop them; a new conversation is the only way.
func assistantEngineSwitchRefusal(locked, model string) string {
	engine, _ := assistantModelEngine(model)
	if locked == "" || locked == engine {
		return ""
	}
	return fmt.Sprintf("这个对话由 %s 回复，不能中途切换 CLI；请新建对话后使用 %s", assistantEngineLabel(locked), assistantEngineLabel(engine))
}

func assistantEngineMissingKey(engine string) string {
	return fmt.Sprintf("请先在个人配置中配置 %s API Key 后再使用 %s 模型", assistantEngineLabel(engine), assistantEngineLabel(engine))
}

// assistantTurnUser checks personal credentials or the chosen runner login.
func (h *assistantHandlers) assistantTurnUser(stored domain.User, model string, runnerLogin bool) (domain.User, string, string) {
	engine, id := assistantModelEngine(model)
	if engine != assistantEngineCodeBuddy {
		if id == "" {
			id = assistantDefaultModel
		}
		if !assistantValidEngineModel(id) {
			return stored, "", "模型 ID 无效"
		}
		if key, _ := assistantEngineCredential(stored, engine); !runnerLogin && key == "" {
			return stored, "", assistantEngineMissingKey(engine)
		}
		return stored, engine + ":" + id, ""
	}
	user, shared, ok := h.assistantChatUser(stored)
	if !ok {
		return stored, "", "请先在个人配置中配置自己的 CodeBuddy API Key"
	}
	if len(shared) > 0 {
		forced, allowed := assistantSponsoredModelFor(shared, id)
		if !allowed {
			return stored, "", errAssistantSponsoredModel(shared)
		}
		model = forced
	}
	return user, model, ""
}

// assistantAllowedUserModel is assistantAllowedModel plus the caller's CLI
// models. Any well-formed model ID is accepted for a configured CLI: the
// gateway behind the Base URL decides what exists, and a discovery outage
// must not silently move the conversation back to CodeBuddy.
func assistantAllowedUserModel(id string, catalog assistantModelCatalog, user domain.User) string {
	engine, model := assistantModelEngine(id)
	if engine == assistantEngineCodeBuddy {
		return assistantAllowedModel(model, catalog)
	}
	if key, _ := assistantEngineCredential(user, engine); key == "" || !assistantValidEngineModel(model) {
		return catalog.Default
	}
	return engine + ":" + model
}

func assistantTurnCatalogModel(requested string, catalog assistantModelCatalog, user domain.User, exact bool) (string, error) {
	model := assistantAllowedUserModel(requested, catalog, user)
	engine, id := assistantModelEngine(requested)
	useDefault := id == "" || id == assistantDefaultModel ||
		(engine == assistantEngineCodeBuddy && codeBuddyIsDefaultModel(id))
	if exact && !useDefault {
		selectedEngine, selectedID := assistantModelEngine(model)
		if selectedEngine != engine || selectedID != id {
			return "", fmt.Errorf("模型 %s 已不在当前目录，请重新选择模型", id)
		}
	}
	return model, nil
}

// assistantScheduledModel normalizes a scheduled model without replacing a
// well-formed selection. The account bound to the task validates it at run time.
func assistantScheduledModel(id string, catalog assistantModelCatalog) string {
	engine, model := assistantModelEngine(id)
	if assistantValidEngineModel(model) {
		if engine != assistantEngineCodeBuddy || model == assistantDefaultModel && assistantModelHasEnginePrefix(id) {
			return engine + ":" + model
		}
		return model
	}
	return assistantAllowedModel(id, catalog)
}

// assistantDiscoveredModelsFor resolves the model list shared by the chat
// composer and role editor. A saved catalog takes precedence over discovery.
// The second result reports whether Cursor is refreshing its automatic catalog.
func assistantDiscoveredModelsFor(ctx context.Context, user domain.User, engine string) ([]assistantDiscoveredModel, bool) {
	key, baseURL := assistantEngineCredential(user, engine)
	protocol := assistantEngineProtocol(user, engine)
	listed := assistantSavedModels(user, engine)
	if len(listed) == 0 && key != "" && baseURL != "" {
		if assistantEngineAnthropicModels(engine, protocol) {
			protocol = runtime.AgentCLIProtocolMessages
		}
		listed = cachedAssistantEngineDiscovery(ctx, protocol, key, baseURL)
	}
	if engine == runtime.AgentCLICursor && key != "" && !user.AgentCLICredentials[engine].ModelsCustomized {
		fresh, pending := cachedCursorModels(key)
		if len(fresh) > 0 {
			listed = fresh
		}
		return listed, pending
	}
	return listed, false
}

func assistantSavedModels(user domain.User, engine string) []assistantDiscoveredModel {
	var listed []assistantDiscoveredModel
	for _, model := range assistantEngineCustomModels(user, engine) {
		if model.ID != assistantDefaultModel && assistantValidEngineModel(model.ID) {
			listed = append(listed, assistantDiscoveredModel{
				ID: model.ID, Name: model.Name, Efforts: model.Efforts, DefaultEffort: model.DefaultEffort,
				ReasoningEfforts: model.ReasoningEfforts, DiscoveredReasoningEfforts: model.DiscoveredReasoningEfforts,
			})
		}
	}
	return listed
}

func assistantEngineModels(ctx context.Context, user domain.User, runnerLogin map[string]bool) (models []assistantModel, cursorModelsPending bool) {
	for _, engine := range assistantCLIEngines() {
		key, baseURL := assistantEngineCredential(user, engine)
		listed := assistantSavedModels(user, engine)
		if key == "" && !runnerLogin[engine] && len(listed) == 0 {
			continue
		}
		if !runnerLogin[engine] {
			var pending bool
			listed, pending = assistantDiscoveredModelsFor(ctx, user, engine)
			cursorModelsPending = cursorModelsPending || pending
		}
		if len(listed) == 0 && (engine != runtime.AgentCLIAntigravity || runnerLogin[engine] || !antigravityUsesCustomEndpoint(user)) {
			listed = assistantEngineBuiltinModels(engine)
		}
		catalog := cliModelsOf(listed)
		for _, model := range listed {
			if model.ID == assistantDefaultModel {
				continue
			}
			name := strings.TrimSpace(model.Name)
			if name == "" {
				name = model.ID
			}
			id := engine + ":" + model.ID
			models = append(models, assistantModel{
				ID:      id,
				Name:    name,
				Family:  assistantModelFamily(model.ID, engine),
				Engine:  engine,
				Efforts: assistantModelEffortsForBaseURL(id, catalog, baseURL),
			})
		}
	}
	return models, cursorModelsPending
}

type assistantDiscoveredModel struct {
	ID   string
	Name string
	// Efforts and DefaultEffort describe a Cursor family folded out of the
	// CLI's list (domain.CLIModel).
	Efforts                    map[string]string
	DefaultEffort              string
	ReasoningEfforts           *[]string
	DiscoveredReasoningEfforts *[]string
}

type assistantDiscoveryEntry struct {
	models  []assistantDiscoveredModel
	expires time.Time
}

const (
	assistantDiscoveryTTL        = 10 * time.Minute
	assistantDiscoveryFailureTTL = time.Minute
	assistantDiscoveryTimeout    = 4 * time.Second
	assistantDiscoveryMaxModels  = 100
)

var (
	assistantDiscoveryCache sync.Map // sha256(engine, baseURL, key) -> assistantDiscoveryEntry
	// assistantEngineDiscover is a variable so tests can stub the network.
	assistantEngineDiscover = discoverAssistantEngineModels
)

func assistantDiscoveryKey(protocol, apiKey, baseURL string) string {
	sum := sha256.Sum256([]byte(protocol + "\x00" + baseURL + "\x00" + apiKey))
	return hex.EncodeToString(sum[:])
}

func cachedAssistantEngineDiscovery(ctx context.Context, protocol, apiKey, baseURL string) []assistantDiscoveredModel {
	cacheKey := assistantDiscoveryKey(protocol, apiKey, baseURL)
	now := time.Now()
	if cached, ok := assistantDiscoveryCache.Load(cacheKey); ok {
		if entry := cached.(assistantDiscoveryEntry); now.Before(entry.expires) {
			return entry.models
		}
	}
	ctx, cancel := context.WithTimeout(ctx, assistantDiscoveryTimeout)
	defer cancel()
	var models []assistantDiscoveredModel
	var err error
	if protocol == runtime.AgentCLIProtocolGemini {
		models, err = discoverGeminiModels(ctx, apiKey, baseURL)
	} else {
		models, err = assistantEngineDiscover(ctx, protocol == runtime.AgentCLIProtocolMessages, apiKey, baseURL)
	}
	ttl := assistantDiscoveryTTL
	if err != nil || len(models) == 0 {
		models, ttl = nil, assistantDiscoveryFailureTTL
	}
	assistantDiscoveryCache.Store(cacheKey, assistantDiscoveryEntry{models: models, expires: now.Add(ttl)})
	return models
}

var (
	assistantCursorModelsCache    sync.Map // sha256(key) -> assistantDiscoveryEntry
	assistantCursorModelsInflight sync.Map
	// assistantCursorModels is a variable so tests can stub cursor-agent.
	assistantCursorModels = cursorAgentModels
)

// cachedCursorModels lists the models the key's Cursor plan offers. The CLI
// takes seconds, so a miss answers with the built-in catalog (nil) while a
// background refresh fills the cache. The second result identifies snapshots
// that should be fetched again after discovery completes.
func cachedCursorModels(apiKey string) ([]assistantDiscoveredModel, bool) {
	sum := sha256.Sum256([]byte("cursor\x00" + apiKey))
	cacheKey := hex.EncodeToString(sum[:])
	var stale []assistantDiscoveredModel
	if cached, ok := assistantCursorModelsCache.Load(cacheKey); ok {
		entry := cached.(assistantDiscoveryEntry)
		if time.Now().Before(entry.expires) {
			return entry.models, false
		}
		stale = entry.models
	}
	if _, running := assistantCursorModelsInflight.LoadOrStore(cacheKey, true); !running {
		discover := assistantCursorModels
		go func() {
			defer assistantCursorModelsInflight.Delete(cacheKey)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			found, err := discover(ctx, apiKey)
			var models []assistantDiscoveredModel
			for _, model := range found {
				if assistantValidEngineModel(model.ID) && len(models) < assistantDiscoveryMaxModels {
					models = append(models, assistantDiscoveredModel{ID: model.ID, Name: model.Name, Efforts: model.Efforts, DefaultEffort: model.DefaultEffort})
				}
			}
			ttl := assistantDiscoveryTTL
			if err != nil || len(models) == 0 {
				models, ttl = stale, assistantDiscoveryFailureTTL
			}
			assistantCursorModelsCache.Store(cacheKey, assistantDiscoveryEntry{models: models, expires: time.Now().Add(ttl)})
		}()
	}
	return stale, true
}

// saveCursorCatalog keeps the user's freshly listed Cursor models, folded
// families with their effort→ID mapping included, in their Cursor
// credential. Effort checks and the --model of chat turns and SOP nodes
// resolve from that saved list, so a family keeps working on every instance
// and after a restart, and each user resolves against their own CLI's list.
func (h *assistantHandlers) saveCursorCatalog(user domain.User) {
	if h == nil || h.repo == nil || user.ID <= 0 || user.AgentCLICredentials[runtime.AgentCLICursor].ModelsCustomized {
		return
	}
	key, _ := assistantEngineCredential(user, runtime.AgentCLICursor)
	if key == "" {
		return
	}
	fresh, _ := cachedCursorModels(key)
	if len(fresh) == 0 {
		return
	}
	models := cliModelsOf(fresh)
	if cursorCatalogEqual(models, assistantCursorCatalog(user)) {
		return
	}
	if _, err := h.repo.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{
		AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{runtime.AgentCLICursor: {Models: &models, DiscoveredForKey: key}},
	}); err != nil {
		log.Printf("cursor model catalog save skipped user=%d: %v", user.ID, err)
	}
}

func cursorCatalogEqual(a, b []domain.CLIModel) bool {
	return slices.EqualFunc(a, b, func(x, y domain.CLIModel) bool {
		return x.ID == y.ID && (x.Name == y.Name || x.Name == x.ID && y.Name == "" || y.Name == y.ID && x.Name == "") &&
			x.DefaultEffort == y.DefaultEffort && maps.Equal(x.Efforts, y.Efforts)
	})
}

// discoverAssistantEngineModels reads the OpenAI-style model list behind a
// Base URL: {base}/models for OpenAI-style Base URLs (which already end in
// /v1) and {base}/v1/models for Anthropic ones. Gateways answer both with
// {"data":[{"id":…,"display_name"|"name":…}]}.
func discoverAssistantEngineModels(ctx context.Context, anthropic bool, apiKey, baseURL string) ([]assistantDiscoveredModel, error) {
	endpoint := strings.TrimRight(baseURL, "/")
	if anthropic {
		endpoint += "/v1/models"
	} else {
		endpoint += "/models"
	}
	if !strings.HasPrefix(endpoint, "https://") && !strings.HasPrefix(endpoint, "http://") {
		return nil, fmt.Errorf("Base URL 必须以 http:// 或 https:// 开头")
	}
	schemes := []string{"bearer"}
	if anthropic {
		if strings.TrimRight(baseURL, "/") == assistantAnthropicAPIBase {
			schemes = []string{"x-api-key"}
		} else {
			schemes = append(schemes, "x-api-key")
		}
	}
	var lastErr error
	for _, scheme := range schemes {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		if anthropic {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		if scheme == "bearer" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		} else {
			req.Header.Set("x-api-key", apiKey)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				err = urlErr.Err
			}
			return nil, fmt.Errorf("无法连接 %s：%w", endpoint, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			lastErr = fmt.Errorf("%s 拒绝了 API Key（HTTP %d）", endpoint, resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s 返回 HTTP %d", endpoint, resp.StatusCode)
		}
		var payload struct {
			Data []struct {
				ID                        string    `json:"id"`
				Name                      string    `json:"name"`
				DisplayName               string    `json:"display_name"`
				ReasoningEfforts          *[]string `json:"reasoningEfforts"`
				SupportedReasoningEfforts *[]string `json:"supported_reasoning_efforts"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("%s 返回的不是模型列表", endpoint)
		}
		var out []assistantDiscoveredModel
		seen := map[string]bool{}
		for _, item := range payload.Data {
			id := strings.TrimSpace(item.ID)
			if !assistantValidEngineModel(id) || seen[id] {
				continue
			}
			seen[id] = true
			name := strings.TrimSpace(item.DisplayName)
			if name == "" {
				name = strings.TrimSpace(item.Name)
			}
			efforts := item.ReasoningEfforts
			if efforts == nil {
				efforts = item.SupportedReasoningEfforts
			}
			out = append(out, assistantDiscoveredModel{ID: id, Name: truncateRunes(name, 64), DiscoveredReasoningEfforts: efforts})
			if len(out) == assistantDiscoveryMaxModels {
				break
			}
		}
		return out, nil
	}
	return nil, lastErr
}

// assistantEngineState records which CLI holds a personal conversation's
// native transcript. A turn on another CLI cannot resume it: it starts a new
// CLI session from the full transcript, so switching back never continues a
// transcript that misses the turns in between.
type assistantEngineState struct {
	// Edited history starts a new journal once; errors resume that revision.
	HistoryRevision         int    `json:"historyRevision,omitempty"`
	Engine                  string `json:"engine"`
	SessionID               string `json:"sessionId"`
	Unstarted               bool   `json:"unstarted,omitempty"`
	WorkspaceRepositoryHash string `json:"workspaceRepositoryHash,omitempty"`
}

func readAssistantEngineState(root string) assistantEngineState {
	state := assistantEngineState{Engine: assistantEngineCodeBuddy}
	data, err := os.ReadFile(filepath.Join(root, "engine.json"))
	if err != nil || json.Unmarshal(data, &state) != nil || state.Engine == "" {
		return assistantEngineState{Engine: assistantEngineCodeBuddy}
	}
	return state
}

func writeAssistantEngineState(root string, state assistantEngineState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "engine.json"), data, 0o600)
}

// assistantChatRequestModel keeps an existing conversation's model when
// omitted, or chooses a personal CLI for a new conversation with no selection.
func (h *assistantHandlers) assistantChatRequestModel(req assistantRequest, saved domain.AssistantSession, user domain.User) string {
	if strings.TrimSpace(req.Executor) == "" && assistantModelUnspecified(req.Model) {
		if saved.Model != "" || saved.Executor != "" {
			return assistantRequestModel(saved.Executor, saved.Model)
		}
		if preferred := personalDefaultModel(user); preferred != "" {
			return preferred
		}
		if _, _, codeBuddyAvailable := h.assistantChatUser(user); !codeBuddyAvailable {
			if model, err := resolveFeatureModel(user, ""); err == nil {
				return model
			}
		}
	}
	return assistantRequestModel(req.Executor, req.Model)
}

// assistantRequestModel maps a legacy executor field onto the model prefix
// the conversation engines use. A model that already names its CLI is kept.
func assistantRequestModel(executor, model string) string {
	model = strings.TrimSpace(model)
	if assistantModelHasEnginePrefix(model) {
		return model
	}
	switch assistantExecutor(executor) {
	case assistantEngineCodex:
		return assistantEngineCodex + ":" + model
	case "claude", assistantEngineClaude:
		return assistantEngineClaude + ":" + model
	default:
		if runtime.IsAgentCLI(executor) {
			return strings.TrimSpace(executor) + ":" + model
		}
	}
	return model
}
