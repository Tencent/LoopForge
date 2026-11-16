package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Hermes Agent (Nous Research) speaks OpenAI-compatible routes and, in one-shot
// mode (-z), prints its final answer as plain text instead of a JSON event
// stream. This integration therefore:
//
//   - writes the route into Hermes' config.yaml (Hermes has no CLI flag for a
//     Base URL) and points HERMES_HOME at the home that owns that file;
//   - forwards stdout lines as text in the stream translator (see
//     NewAgentCLITranslator);
//   - asks Hermes to write a usage file, which a later tier reads back for
//     token accounting.
//
// HERMES_HOME must be a home where the CLI's dependency environment is
// committed: hermes refuses to start otherwise ("run hermes pm repair"), and
// the installer commits it under its own install home. AGENTFLOW_HERMES_HOME
// names that install home (the shared case: one route file for the machine);
// without it every run gets its own private home, which only works when the
// CLI was installed into it.
const (
	hermesHomeKey    = ".hermes"
	hermesConfigFile = "config.yaml"
	hermesConfigPath = ".hermes/config.yaml"
	hermesUsageFile  = "agentflow-usage.json"
)

// hermesAPIName maps a wire protocol onto the "api" value Hermes stores for a
// provider route.
func hermesAPIName(protocol string) string {
	return map[string]string{
		AgentCLIProtocolChat:      "chat",
		AgentCLIProtocolResponses: "responses",
		AgentCLIProtocolMessages:  "anthropic",
	}[protocol]
}

// hermesConfig renders the YAML Hermes reads: the route under `providers` plus
// the same route as the default model.
func hermesConfig(api, key, baseURL, model, effort string) []byte {
	lines := []string{
		"model:",
		"  provider: agentflow",
		"  default: " + strconv.Quote(model),
		"  base_url: " + strconv.Quote(baseURL),
		"  api_key: " + strconv.Quote(key),
		"providers:",
		"  agentflow:",
		"    name: AgentFlow",
		"    api: " + strconv.Quote(api),
		"    base_url: " + strconv.Quote(baseURL),
		"    api_key: " + strconv.Quote(key),
		"    default_model: " + strconv.Quote(model),
	}
	if effort = strings.TrimSpace(effort); effort != "" {
		lines = append(lines, "agent:", "  reasoning_effort: "+strconv.Quote(effort))
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// applyHermesCredential writes the provider/model YAML Hermes reads at launch
// and adds --usage-file ahead of the run shape.
func applyHermesCredential(home AgentCLIHome, homeEnv, key, baseURL, protocol, model, effort string, argv []string) ([]string, map[string]string, error) {
	api := hermesAPIName(protocol)
	if api == "" {
		return nil, nil, fmt.Errorf("%s 不支持 %q 协议", AgentCLILabel(AgentCLIHermes), protocol)
	}
	content := hermesConfig(api, key, baseURL, model, effort)

	configHome := filepath.ToSlash(filepath.Join(homeEnv, hermesHomeKey))
	if shared := strings.TrimSpace(os.Getenv("AGENTFLOW_HERMES_HOME")); shared != "" {
		if !filepath.IsAbs(shared) {
			return nil, nil, fmt.Errorf("AGENTFLOW_HERMES_HOME 必须是绝对路径")
		}
		shared = filepath.Clean(shared)
		if err := os.MkdirAll(shared, 0o700); err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(filepath.Join(shared, hermesConfigFile), content, 0o600); err != nil {
			return nil, nil, err
		}
		configHome = filepath.ToSlash(shared)
	} else if err := writeAgentCLIFile(home, hermesConfigPath, content); err != nil {
		return nil, nil, err
	}

	argv = insertBeforeFlag(argv, "-z", "--usage-file", filepath.ToSlash(filepath.Join(homeEnv, hermesUsageFile)))
	return argv, map[string]string{"HERMES_HOME": configHome}, nil
}