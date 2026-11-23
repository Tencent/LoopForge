package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Codex falls back to built-in metadata for a model it does not know, which
// understates the context window of a model served by another gateway and
// makes the CLI warn on every turn. A gateway that serves a model outside
// OpenAI's catalog therefore needs a model catalog: Codex reads it from
// model_catalog_json.
//
// The schema is Codex's own; every field below is required by its parser
// except the ones marked optional.
type codexModelCatalog struct {
	Models []codexModelInfo `json:"models"`
}

type codexModelInfo struct {
	Slug                     string                     `json:"slug"`
	DisplayName              string                     `json:"display_name"`
	ContextWindow            int                        `json:"context_window"`
	MaxOutputTokens          int                        `json:"max_output_tokens"`
	ShellType                string                     `json:"shell_type"`
	Visibility               string                     `json:"visibility"`
	SupportedInAPI           bool                       `json:"supported_in_api"`
	SupportsReasoningSummary bool                       `json:"supports_reasoning_summaries"`
	Priority                 int                        `json:"priority"`
	SupportVerbosity         bool                       `json:"support_verbosity"`
	TruncationPolicy         codexTruncationPolicy      `json:"truncation_policy"`
	ExperimentalTools        []string                   `json:"experimental_supported_tools"`
	BaseInstructions         string                     `json:"base_instructions"`
	SupportedReasoningLevels []codexReasoningEffortSpec `json:"supported_reasoning_levels"`
	InputModalities          []string                   `json:"input_modalities,omitempty"`
}

type codexTruncationPolicy struct {
	Mode  string `json:"mode"`
	Limit int    `json:"limit"`
}

type codexReasoningEffortSpec struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// codexModelCatalogWindow is the window a model gets when the gateway does
// not publish one. It is deliberately generous: the catalog only has to be
// wide enough that Codex does not truncate a turn early.
const codexModelCatalogWindow = 128000

// codexModelCatalogOutput is the output budget used when the gateway does
// not publish one.
const codexModelCatalogOutput = 32000

// codexModelCatalogLimit is the truncation budget, half the window, so a
// turn leaves room for the reply.
func codexModelCatalogLimit(window int) int {
	limit := window / 2
	if limit < 1024 {
		limit = 1024
	}
	return limit
}

// CodexModel describes one model for the Codex catalog.
type CodexModel struct {
	// ID is the model ID the CLI sends to the gateway.
	ID string
	// Name is the label Codex shows.
	Name string
	// ContextWindow is the model's real context window; 0 means unknown.
	ContextWindow int
	// MaxOutputTokens is the model's output budget; 0 means unknown.
	MaxOutputTokens int
	// Efforts are the reasoning-effort levels the model accepts.
	Efforts []string
}

// codexCatalogArg writes the model catalog for this turn and returns the path
// the CLI should read.
//
// A catalog that cannot be written is not fatal: the CLI still runs, it only
// falls back to its own metadata for a model it does not know. The turn then
// behaves like the one before the catalog existed.
func codexCatalogArg(bound CommandSpec, models []CodexModel) string {
	if len(models) == 0 {
		return ""
	}
	path, err := codexCatalogPath(bound, models)
	if err != nil {
		return ""
	}
	return path
}

// codexCatalogPath writes the model catalog next to the CLI's own
// configuration and returns its path. The catalog lives in the private home
// of the turn, so it is written where CODEX_HOME points.
//
// An empty model list returns an empty path: Codex then keeps its own
// catalog, which is right for OpenAI's own endpoint.
func codexCatalogPath(bound CommandSpec, models []CodexModel) (string, error) {
	if len(models) == 0 {
		return "", nil
	}
	home := strings.TrimSpace(bound.environment["HOME"])
	if home == "" {
		return "", fmt.Errorf("codex private home is not set")
	}
	codexHome := strings.TrimSpace(bound.environment["CODEX_HOME"])
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	path := filepath.Join(codexHome, "models.json")
	if _, err := WriteCodexModelCatalog(home, path, models); err != nil {
		return "", err
	}
	return path, nil
}

// codexCatalogEffortOrder is the order Codex lists its effort levels in.
// The catalog lists a subset of them, in this order.
var codexCatalogEffortOrder = []string{"minimal", "low", "medium", "high", "xhigh"}

// WriteCodexModelCatalog writes the model catalog Codex reads through
// model_catalog_json, and returns its path. A model without a known window
// gets a conservative one, because Codex's fallback is smaller still.
//
// The file is written next to the CLI's own configuration so that it lives
// and dies with the private home of the turn.
func WriteCodexModelCatalog(home, path string, models []CodexModel) (string, error) {
	if len(models) == 0 {
		return "", nil
	}
	catalog := codexModelCatalog{Models: make([]codexModelInfo, 0, len(models))}
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		window := model.ContextWindow
		if window <= 0 {
			window = codexModelCatalogWindow
		}
		output := model.MaxOutputTokens
		if output <= 0 {
			output = codexModelCatalogOutput
		}
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = model.ID
		}
		catalog.Models = append(catalog.Models, codexModelInfo{
			Slug:                     model.ID,
			DisplayName:              name,
			ContextWindow:            window,
			MaxOutputTokens:          output,
			ShellType:                "disabled",
			Visibility:               "list",
			SupportedInAPI:           true,
			SupportsReasoningSummary: true,
			Priority:                 1,
			SupportVerbosity:         false,
			TruncationPolicy:         codexTruncationPolicy{Mode: "tokens", Limit: codexModelCatalogLimit(window)},
			ExperimentalTools:        []string{},
			BaseInstructions:         "You are a helpful assistant.",
			SupportedReasoningLevels: codexCatalogEfforts(model.Efforts),
			InputModalities:          []string{"text"},
		})
	}
	if len(catalog.Models) == 0 {
		return "", nil
	}
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// codexCatalogEfforts keeps the levels the model accepts, in Codex's order.
// A model without levels keeps the CLI's default handling.
func codexCatalogEfforts(efforts []string) []codexReasoningEffortSpec {
	allowed := map[string]bool{}
	for _, effort := range efforts {
		allowed[effort] = true
	}
	var out []codexReasoningEffortSpec
	for _, effort := range codexCatalogEffortOrder {
		if !allowed[effort] {
			continue
		}
		out = append(out, codexReasoningEffortSpec{Effort: effort, Description: codexEffortDescription(effort)})
	}
	return out
}

func codexEffortDescription(effort string) string {
	switch effort {
	case "minimal":
		return "Minimal reasoning"
	case "low":
		return "Low reasoning"
	case "medium":
		return "Medium reasoning"
	case "high":
		return "High reasoning"
	case "xhigh":
		return "Extra high reasoning"
	default:
		return fmt.Sprintf("%s reasoning", effort)
	}
}
