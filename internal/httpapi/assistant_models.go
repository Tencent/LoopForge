package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

// assistantChatDefaultModel is the model the legacy sponsor account lends.
// New chat conversations preselect the shared key's first model when one is
// configured; otherwise every picker starts on the first model it lists.
const assistantChatDefaultModel = assistantSponsoredModel

// Legacy saved selections may contain this provider-default alias.
const assistantDefaultModel = "default"

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
	Engines             []assistantEngineOption `json:"engines,omitempty"`
	CursorModelsPending bool                    `json:"cursorModelsPending,omitempty"`
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
	models []string
	at     time.Time
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
		cliModelCache[cacheKey] = cliModelEntry{models: models, at: time.Now()}
	}
	cliModelMu.Unlock()
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
	for _, key := range slices.Sorted(maps.Keys(cliModelCache)) {
		entry := cliModelCache[key]
		for _, id := range entry.models {
			if !codeBuddyIsDefaultModel(id) && !seen[id] {
				catalog.Models = append(catalog.Models, assistantListedModel(id, id, ""))
				seen[id] = true
			}
		}
	}
	return catalog
}

func cachedCLIModels(environment, apiKey string) []string {
	cliModelMu.Lock()
	defer cliModelMu.Unlock()
	return cliModelCache[cliModelCacheKey(strings.TrimSpace(environment), apiKey)].models
}

// models returns the chat model catalog for the caller's CodeBuddy
// environment. Guests get the configured models only; actually chatting still
// requires login plus a configured or sponsored CodeBuddy key.
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
		c.JSON(200, result.withFirstDefault())
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
		catalog = restrictAssistantCatalog(assistantChatCatalog(c.Request.Context(), key.InternetEnvironment, key.APIKey, key.Models), key.Models)
	case signedIn:
		// A user's own key keeps their environment's models and default: the
		// shared key's default may not exist in that environment.
		refreshCLIModels(c.Request.Context(), user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey)
		catalog = assistantChatCatalog(c.Request.Context(), user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey, nil)
		if saved := selectableCodeBuddyModels(codeBuddyPersonalModels(user)); len(saved) > 0 {
			catalog.Default = saved[0].ID
			catalog.Models = saved
		}
	default:
		catalog = assistantChatCatalog(c.Request.Context(), "", "", nil)
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

// Older personal settings may still contain the CLI-default placeholder.
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

func codeBuddyAccountModel(ctx context.Context, user domain.User, id string) string {
	refreshCLIModels(ctx, user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey)
	catalog := withCodeBuddyPersonalModels(loadAssistantModels(ctx, user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey), user)
	return assistantAllowedModel(id, catalog)
}

// withCodeBuddyPersonalModels accepts the user's saved CodeBuddy models and
// defaults requests without a model to the first of them. The environment's
// models stay accepted.
func withCodeBuddyPersonalModels(catalog assistantModelCatalog, user domain.User) assistantModelCatalog {
	saved := selectableCodeBuddyModels(codeBuddyPersonalModels(user))
	if len(saved) == 0 {
		return catalog
	}
	catalog.Models = append(catalog.Models, saved...)
	catalog.Default = saved[0].ID
	return catalog
}

func codeBuddyPersonalModels(user domain.User) []assistantModel {
	if strings.TrimSpace(user.CodeBuddyAPIKey) == "" {
		return nil
	}
	var models []assistantModel
	for _, item := range assistantSavedModels(user, assistantEngineCodeBuddy) {
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
func assistantChatCatalog(ctx context.Context, env, apiKey string, shared []string) assistantModelCatalog {
	catalog := loadAssistantModels(ctx, env, apiKey)
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
func loadAssistantModels(_ context.Context, environment, apiKey string) assistantModelCatalog {
	catalog := assistantFixedCatalog()
	seen := map[string]bool{}
	for _, model := range catalog.Models {
		seen[model.ID] = true
	}
	for _, id := range cachedCLIModels(environment, apiKey) {
		if !codeBuddyIsDefaultModel(id) && !seen[id] {
			catalog.Models = append(catalog.Models, assistantListedModel(id, id, ""))
			seen[id] = true
		}
	}
	return catalog.withFirstDefault()
}

// withFirstDefault makes the first listed model the default. A catalog
// with no models has no default.
func (c assistantModelCatalog) withFirstDefault() assistantModelCatalog {
	c.Default = ""
	if len(c.Models) > 0 {
		c.Default = c.Models[0].ID
	}
	return c
}

// assistantFixedCatalog lists the models the operator configured:
// AGENTFLOW_ASSISTANT_MODEL first, then AGENTFLOW_ASSISTANT_MODELS. The
// models CodeBuddy advertises (`codebuddy --help`) follow them in the
// catalogs built on this one.
func assistantFixedCatalog() assistantModelCatalog {
	var models []assistantModel
	seen := map[string]bool{}
	for _, id := range append([]string{os.Getenv("AGENTFLOW_ASSISTANT_MODEL")}, strings.Split(os.Getenv("AGENTFLOW_ASSISTANT_MODELS"), ",")...) {
		id = domain.ChosenModel(id)
		if id != "" && !codeBuddyIsDefaultModel(id) && !seen[id] {
			models = append(models, assistantListedModel(id, id, ""))
			seen[id] = true
		}
	}
	return assistantModelCatalog{Models: models}.withFirstDefault()
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

// assistantAllowedModel is id when catalog lists it, else the first listed
// model.
func assistantAllowedModel(id string, catalog assistantModelCatalog) string {
	id = domain.ChosenModel(id)
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
