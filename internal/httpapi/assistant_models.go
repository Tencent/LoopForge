package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

const assistantDefaultModel = "default"

// assistantChatDefaultModel is the model the legacy sponsor account lends.
// New chat conversations preselect the shared key's default model when one is
// configured; otherwise chat uses the operator's default, like clarification,
// scheduled prompts, reports and executors.
const assistantChatDefaultModel = assistantSponsoredModel

type assistantModel struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Family  string   `json:"family"`
	Credits string   `json:"credits"`
	Efforts []string `json:"efforts"`
	// Engine is set for models that run on a CLI other than CodeBuddy.
	Engine string `json:"engine,omitempty"`
}

type assistantModelCatalog struct {
	Default        string           `json:"default"`
	DefaultEfforts []string         `json:"defaultEfforts,omitempty"`
	Models         []assistantModel `json:"models"`
	// SponsoredModels is set when users without their own CodeBuddy API Key
	// may still chat on the platform's shared key, limited to these models;
	// SponsoredModel is the first (the default).
	SponsoredModel  string   `json:"sponsoredModel,omitempty"`
	SponsoredModels []string `json:"sponsoredModels,omitempty"`
	// Engines lists the CLIs a signed-in user can switch between.
	Engines             []assistantEngineOption   `json:"engines,omitempty"`
	Preferences         *domain.AssistantDefaults `json:"preferences,omitempty"`
	CursorModelsPending bool                      `json:"cursorModelsPending,omitempty"`
}

func prefetchAssistantModels() {}

// CodeBuddy publishes the models available in each network environment in its
// --help output; the list differs between international, internal and iOA.
var (
	cliModelPattern   = regexp.MustCompile(`Currently supported: \(([^)]*)\)`)
	cliModelMu        sync.Mutex
	cliModelCache     = map[string]cliModelEntry{}
	discoverCLIModels = discoverCodeBuddyModels
)

const cliModelTTL = time.Hour

type cliModelEntry struct {
	environment string
	models      []string
	at          time.Time
}

// cliModelCacheKey separates accounts within one environment: without a key
// the CLI only prints the environment's fallback list, while an authenticated
// account may unlock extra models (e.g. claude-opus-5.5).
func cliModelCacheKey(environment, apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return environment
	}
	sum := sha256.Sum256([]byte(apiKey))
	return environment + "|" + hex.EncodeToString(sum[:8])
}

func discoverCodeBuddyModels(ctx context.Context, environment, apiKey string) []string {
	bin, err := resolveCodeBuddyBin()
	if err != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "loopforge-models-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--help")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "CODEBUDDY_CONFIG_DIR=" + dir, "CODEBUDDY_INTERNET_ENVIRONMENT=" + environment}
	if key := strings.TrimSpace(apiKey); key != "" {
		cmd.Env = append(cmd.Env, "CODEBUDDY_API_KEY="+key)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseCodeBuddyModels(string(out))
}

func parseCodeBuddyModels(help string) []string {
	m := cliModelPattern.FindStringSubmatch(help)
	if m == nil {
		return nil
	}
	var models []string
	for _, id := range strings.Split(m[1], ",") {
		id = strings.TrimSpace(id)
		if id == "" || id == "echo" || strings.Contains(id, ":") {
			continue
		}
		models = append(models, id)
	}
	return models
}

// refreshCLIModels runs discovery when the cached list is missing or stale.
func refreshCLIModels(ctx context.Context, environment, apiKey string) {
	environment = strings.TrimSpace(environment)
	cacheKey := cliModelCacheKey(environment, apiKey)
	cliModelMu.Lock()
	entry, ok := cliModelCache[cacheKey]
	cliModelMu.Unlock()
	if ok && time.Since(entry.at) < cliModelTTL {
		return
	}
	models := discoverCLIModels(ctx, environment, apiKey)
	cliModelMu.Lock()
	if models != nil || !ok {
		cliModelCache[cacheKey] = cliModelEntry{environment: environment, models: models, at: time.Now()}
	}
	cliModelMu.Unlock()
}

func refreshOfficialCodeBuddyModels(ctx context.Context, user domain.User) {
	if user.CodeBuddyBaseURL == "" && strings.TrimSpace(user.CodeBuddyAPIKey) != "" {
		refreshCLIModels(ctx, user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey)
	}
}

// assistantKnownCatalog validates settings that are not tied to one user's
// environment, such as SOP roles and notification bots.
func assistantKnownCatalog() assistantModelCatalog {
	catalog := assistantFixedCatalog()
	seen := map[string]bool{}
	for _, model := range catalog.Models {
		seen[model.ID] = true
	}
	cliModelMu.Lock()
	defer cliModelMu.Unlock()
	for _, entry := range cliModelCache {
		for _, id := range entry.models {
			if !seen[id] {
				catalog.Models = append(catalog.Models, assistantListedModel(id, id, ""))
				seen[id] = true
			}
		}
	}
	return catalog
}

func cachedCLIModels(environment, apiKey string) []string {
	cacheKey := cliModelCacheKey(strings.TrimSpace(environment), apiKey)
	cliModelMu.Lock()
	defer cliModelMu.Unlock()
	return cliModelCache[cacheKey].models
}

// models returns the available CLIs and the caller's models. Public catalog
// reads require no credential. Running a model requires a configured identity.
func (h *assistantHandlers) models(c *gin.Context) {
	if raw := strings.TrimSpace(c.Query("executor")); raw != "" && assistantExecutor(raw) != "codebuddy" {
		engine := assistantExecutor(raw)
		id := engine
		if engine == assistantEngineClaude {
			id = "claude"
		} else if id == "claude" {
			engine = assistantEngineClaude
		}
		var user domain.User
		if h.repo != nil && (engine == runtime.AgentCLIKimi || engine == runtime.AgentCLIAntigravity) {
			if current, signedIn := h.requestUser(c.Request, true); signedIn {
				user, _ = h.repo.User(current.ID)
			}
		}
		_, kimiBaseURL := assistantEngineCredential(user, runtime.AgentCLIKimi)
		catalog, ok := executorModelCatalogs(kimiBaseURL, assistantFixedCatalog())[id]
		if !ok {
			c.JSON(400, gin.H{"error": "不支持的执行器"})
			return
		}
		result := assistantModelCatalog{Models: make([]assistantModel, 0, len(catalog.Models))}
		if engine == runtime.AgentCLIAntigravity && antigravityUsesCustomEndpoint(user) {
			c.Header("Cache-Control", "private, no-store")
			c.JSON(200, result)
			return
		}
		for _, item := range catalog.Models {
			levels := make([]string, 0, len(item.Efforts))
			for _, option := range item.Efforts {
				levels = append(levels, option.Value)
			}
			result.Models = append(result.Models, assistantModel{ID: item.ID, Name: item.Name, Family: item.Family, Engine: engine, Efforts: levels})
		}
		if len(result.Models) > 0 {
			result.Default = result.Models[0].ID
		}
		c.JSON(200, result)
		return
	}

	var catalog assistantModelCatalog
	key, shared := h.assistantSharedKey()
	user, signedIn := domain.User{}, false
	if current, ok := h.requestUser(c.Request, true); ok {
		user, signedIn = h.repo.User(current.ID)
	}
	usingSharedKey := signedIn && shared && strings.TrimSpace(user.CodeBuddyAPIKey) == ""
	switch {
	case usingSharedKey:
		// Chat runs on the shared key, so list what that key's environment offers.
		refreshCLIModels(c.Request.Context(), key.InternetEnvironment, key.APIKey)
		catalog = restrictAssistantCatalog(assistantChatCatalog(key.InternetEnvironment, key.APIKey, key.Models), key.Models)
	case signedIn && strings.TrimSpace(user.CodeBuddyAPIKey) != "":
		// A user's own key keeps their environment's models and default: the
		// shared key's default may not exist in that environment.
		catalog = personalCodeBuddyCatalog(c.Request.Context(), user)
	case signedIn:
		// Do not probe a CLI the user has not configured.
		catalog.Default = assistantDefaultModel
	default:
		catalog = assistantChatCatalog("", "", nil)
	}
	if catalog.Default == assistantDefaultModel {
		models := user.CodeBuddyModels
		if usingSharedKey {
			models = nil
		}
		catalog.DefaultEfforts = assistantModelEffortsIn(catalog.Default, models)
	}
	catalog.Models = selectableCodeBuddyModels(catalog.Models)
	if shared {
		catalog.SponsoredModel, catalog.SponsoredModels = key.Models[0], key.Models
	}
	if signedIn {
		c.Header("Cache-Control", "private, no-store")
		if user.AssistantDefaults != (domain.AssistantDefaults{}) {
			catalog.Preferences = &user.AssistantDefaults
		}
		catalog.Engines = assistantEngineOptions(user, usingSharedKey)
		var runnerLogin map[string]bool
		if c.Query("schedule") == "1" {
			runnerLogin = make(map[string]bool)
			for i := range catalog.Engines {
				engine := catalog.Engines[i].ID
				if scheduleLoginRunner(h.repo, user.ID, engine).ID != "" {
					runnerLogin[engine] = true
					catalog.Engines[i].Configured = true
				}
			}
		}
		models, pending := assistantEngineModels(c.Request.Context(), user, runnerLogin)
		catalog.Models = append(catalog.Models, models...)
		catalog.CursorModelsPending = pending
		h.saveCursorCatalog(user)
	}
	c.JSON(200, catalog)
}

func personalCodeBuddyCatalog(ctx context.Context, user domain.User) assistantModelCatalog {
	if strings.TrimSpace(user.CodeBuddyAPIKey) == "" {
		return assistantModelCatalog{Default: assistantDefaultModel}
	}
	refreshOfficialCodeBuddyModels(ctx, user)
	catalog := assistantChatCatalog(user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey, nil)
	if personal := codeBuddyPersonalModels(ctx, user); len(personal) > 0 {
		catalog.Default = personal[0].ID
		if listed := selectableCodeBuddyModels(personal); len(listed) > 0 {
			catalog.Models = listed
		}
	}
	return catalog
}

// Keep the CLI default as an execution fallback and for older saved records.
// The model pickers offer the concrete models available to the caller.
func selectableCodeBuddyModels(models []assistantModel) []assistantModel {
	listed := make([]assistantModel, 0, len(models))
	for _, model := range models {
		if !codeBuddyIsDefaultModel(model.ID) {
			listed = append(listed, model)
		}
	}
	return listed
}

func codeBuddyIsDefaultModel(id string) bool {
	return id == assistantDefaultModel || id == "default-model"
}

// withCodeBuddyPersonalModels accepts the user's own CodeBuddy catalog and
// defaults requests without a model to the first of them. The environment's
// models stay accepted.
func withCodeBuddyPersonalModels(ctx context.Context, catalog assistantModelCatalog, user domain.User) assistantModelCatalog {
	personal := codeBuddyPersonalModels(ctx, user)
	if len(personal) == 0 {
		return catalog
	}
	catalog.Models = append(catalog.Models, personal...)
	catalog.Default = personal[0].ID
	return catalog
}

func codeBuddyPersonalModels(ctx context.Context, user domain.User) []assistantModel {
	if strings.TrimSpace(user.CodeBuddyAPIKey) == "" {
		return nil
	}
	var models []assistantModel
	personal, _ := assistantDiscoveredModelsFor(ctx, user, assistantEngineCodeBuddy)
	for _, item := range personal {
		name := item.Name
		if name == "" {
			name = item.ID
		}
		model := assistantListedModel(item.ID, name, "")
		model.Efforts = assistantModelEffortsIn(item.ID, user.CodeBuddyModels)
		models = append(models, model)
	}
	return models
}

// assistantChatCatalog is the catalog used by chat conversations: the same
// models, with the shared key's models listed and its default preselected.
// Pass shared only for turns that run on the shared key (see chatCatalog).
func assistantChatCatalog(environment, apiKey string, shared []string) assistantModelCatalog {
	catalog := loadAssistantModels(environment, apiKey)
	if len(shared) == 0 {
		return catalog
	}
	listed := map[string]bool{}
	for _, model := range catalog.Models {
		listed[model.ID] = true
	}
	for _, id := range shared {
		if !listed[id] {
			name := id
			if id == assistantChatDefaultModel {
				name = "Deepseek-V4.1-Flash"
			}
			catalog.Models = append(catalog.Models, assistantListedModel(id, name, ""))
			listed[id] = true
		}
	}
	catalog.Default = shared[0]
	return catalog
}

// loadAssistantModels never blocks on discovery; validation accepts models the
// caller's environment has advertised.
func loadAssistantModels(environment, apiKey string) assistantModelCatalog {
	catalog := assistantFixedCatalog()
	seen := map[string]bool{}
	for _, model := range catalog.Models {
		seen[model.ID] = true
	}
	for _, id := range cachedCLIModels(environment, apiKey) {
		if !seen[id] {
			catalog.Models = append(catalog.Models, assistantListedModel(id, id, ""))
			seen[id] = true
		}
	}
	return catalog
}

// assistantModelsForRequest looks up a concrete model with the execution
// account's credentials, including after the local discovery cache expired.
func assistantModelsForRequest(ctx context.Context, user domain.User, model string) assistantModelCatalog {
	environment, key := user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey
	catalog := withCodeBuddyPersonalModels(ctx, loadAssistantModels(environment, key), user)
	if model != "" && !assistantCatalogLists(catalog, model) {
		refreshOfficialCodeBuddyModels(ctx, user)
		catalog = withCodeBuddyPersonalModels(ctx, loadAssistantModels(environment, key), user)
	}
	return catalog
}

// assistantFixedCatalog mirrors `codebuddy --help` (CodeBuddy Code 2.180.0)
// --model list, names/credits from the CLI's remote product catalog. Two ids
// are kept although the CLI no longer advertises them: deepseek-v4-flash-ioa
// is pinned by live roles (and was the old built-in executor default), and
// hy4-preview-ioa stays alongside hy4std for existing users.
func assistantFixedCatalog() assistantModelCatalog {
	defaultID := strings.TrimSpace(os.Getenv("AGENTFLOW_ASSISTANT_MODEL"))
	if defaultID == "" {
		defaultID = assistantDefaultModel
	}
	defaultName := defaultID
	if defaultID == assistantDefaultModel {
		defaultName = "CLI 默认模型"
	}
	models := []assistantModel{assistantListedModel(defaultID, defaultName, "")}
	seen := map[string]bool{defaultID: true}
	for _, id := range strings.Split(os.Getenv("AGENTFLOW_ASSISTANT_MODELS"), ",") {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			models = append(models, assistantListedModel(id, id, ""))
			seen[id] = true
		}
	}
	return assistantModelCatalog{Default: defaultID, Models: models}
}

func assistantListedModel(id, name, credits string) assistantModel {
	return assistantModel{ID: id, Name: name, Family: assistantModelFamily(id, assistantEngineCodeBuddy), Credits: credits, Efforts: assistantModelEffortsIn(id, nil)}
}

func assistantModelFamily(id, engine string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if engine == assistantEngineCodeBuddy {
		switch id {
		case "fast-model", "balanced-model", "primary-model", "deep-model":
			return "codebuddy"
		}
	}
	id = id[strings.LastIndexByte(id, '/')+1:]
	switch {
	case strings.HasPrefix(id, "claude"):
		return "claude"
	case strings.HasPrefix(id, "gpt") || strings.Contains(id, "codex"):
		return "gpt"
	case strings.HasPrefix(id, "glm"):
		return "glm"
	case strings.HasPrefix(id, "gemini"):
		return "gemini"
	case id == "grok" || strings.HasPrefix(id, "grok-") || strings.HasPrefix(id, "cursor-grok-"):
		return "grok"
	case id == "muse" || strings.HasPrefix(id, "muse-"):
		return "muse"
	case id == "composer" || strings.HasPrefix(id, "composer-") || (engine == runtime.AgentCLICursor && (id == "auto" || id == "auto-smart")):
		return "cursor"
	case strings.HasPrefix(id, "kimi") || id == "k3" || strings.HasPrefix(id, "k3-") || strings.HasPrefix(id, "k3."):
		return "kimi"
	case strings.HasPrefix(id, "moonshot"):
		return "moonshot"
	case strings.HasPrefix(id, "qwen") || strings.HasPrefix(id, "qvq"):
		return "qwen"
	case strings.HasPrefix(id, "doubao") || strings.HasPrefix(id, "seed-") || strings.HasPrefix(id, "seedream"):
		return "doubao"
	case strings.HasPrefix(id, "ernie") || strings.HasPrefix(id, "wenxin") || strings.HasPrefix(id, "qianfan"):
		return "qianfan"
	case strings.HasPrefix(id, "deepseek"):
		return "deepseek"
	case strings.HasPrefix(id, "minimax"):
		return "minimax"
	case strings.HasPrefix(id, "hy") || strings.Contains(id, "hunyuan"):
		return "hunyuan"
	case strings.HasPrefix(id, "mimo"):
		return "mimo"
	default:
		return "other"
	}
}

func assistantAllowedModel(id string, catalog assistantModelCatalog) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return catalog.Default
	}
	for _, model := range catalog.Models {
		if model.ID == id {
			return id
		}
	}
	return catalog.Default
}
