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

	"github.com/gin-gonic/gin"
)

const assistantDefaultModel = "default"

type assistantModel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Family  string `json:"family"`
	Credits string `json:"credits"`
}

type assistantModelCatalog struct {
	Default string           `json:"default"`
	Models  []assistantModel `json:"models"`
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

func cachedCLIModels(environment string) []string {
	cliModelMu.Lock()
	defer cliModelMu.Unlock()
	environment = strings.TrimSpace(environment)
	var models []string
	seen := map[string]bool{}
	for _, entry := range cliModelCache {
		if entry.environment != environment {
			continue
		}
		for _, id := range entry.models {
			if !seen[id] {
				models = append(models, id)
				seen[id] = true
			}
		}
	}
	return models
}

// models returns the assistant model catalog for the caller's CodeBuddy
// environment. Guests get the configured models only.
func (h *assistantHandlers) models(c *gin.Context) {
	if id := assistantExecutor(c.Query("executor")); id != "codebuddy" {
		catalog, ok := executorModelCatalogs()[id]
		if !ok {
			c.JSON(400, gin.H{"error": "不支持的执行器"})
			return
		}
		result := assistantModelCatalog{Default: "default", Models: []assistantModel{{ID: "default", Name: "CLI default", Family: map[string]string{"codex": "gpt", "claude": "claude"}[id]}}}
		for _, item := range catalog.Models {
			result.Models = append(result.Models, assistantModel{ID: item.ID, Name: item.Name, Family: map[string]string{"codex": "gpt", "claude": "claude"}[id]})
		}
		c.JSON(200, result)
		return
	}

	environment := ""
	if current, ok := h.requestUser(c.Request, true); ok {
		if user, ok := h.repo.User(current.ID); ok {
			environment = user.CodeBuddyInternetEnvironment
			refreshCLIModels(c.Request.Context(), environment, user.CodeBuddyAPIKey)
		}
	}
	c.JSON(200, loadAssistantModels(c.Request.Context(), environment))
}

// loadAssistantModels never blocks on discovery; validation accepts models the
// caller's environment has advertised.
func loadAssistantModels(_ context.Context, environment string) assistantModelCatalog {
	catalog := assistantFixedCatalog()
	seen := map[string]bool{}
	for _, model := range catalog.Models {
		seen[model.ID] = true
	}
	for _, id := range cachedCLIModels(environment) {
		if !seen[id] {
			catalog.Models = append(catalog.Models, assistantListedModel(id, id, ""))
			seen[id] = true
		}
	}
	return catalog
}

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
	return assistantModel{ID: id, Name: name, Family: assistantModelFamily(id), Credits: credits}
}

func assistantModelFamily(id string) string {
	id = strings.ToLower(id)
	switch {
	case strings.HasPrefix(id, "claude"):
		return "claude"
	case strings.HasPrefix(id, "gpt") || strings.Contains(id, "codex"):
		return "gpt"
	case strings.HasPrefix(id, "glm"):
		return "glm"
	case strings.HasPrefix(id, "gemini"):
		return "gemini"
	case strings.HasPrefix(id, "kimi"):
		return "kimi"
	case strings.HasPrefix(id, "deepseek"):
		return "deepseek"
	case strings.HasPrefix(id, "minimax"):
		return "minimax"
	case strings.HasPrefix(id, "hy") || strings.Contains(id, "hunyuan"):
		return "hunyuan"
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
