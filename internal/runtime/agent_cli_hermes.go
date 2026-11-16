package runtime

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Hermes Agent (Nous Research) speaks OpenAI-compatible routes and, in one-shot
// mode (-z), prints its final answer as plain text instead of a JSON event
// stream. This integration therefore:
//
//   - writes the route into ~/.hermes/config.yaml (Hermes has no CLI flag for a
//     Base URL) and points HERMES_HOME at the per-Agent private HOME;
//   - forwards stdout lines as text in the stream translator (see
//     NewAgentCLITranslator);
//   - asks Hermes to write a usage file, which a later tier reads back for
//     token accounting.
const (
	hermesHomeKey    = ".hermes"
	hermesConfigPath = ".hermes/config.yaml"
	hermesUsagePath  = ".hermes/agentflow-usage.json"
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

// applyHermesCredential writes the provider/model YAML Hermes reads at launch
// and adds --usage-file ahead of the run shape. Hermes resolves its route from
// the top-level `model` block, so the same values are repeated under
// `providers` for named routes.
func applyHermesCredential(home AgentCLIHome, homeEnv, key, baseURL, protocol, model, effort string, argv []string) ([]string, map[string]string, error) {
	api := hermesAPIName(protocol)
	if api == "" {
		return nil, nil, fmt.Errorf("%s 不支持 %q 协议", AgentCLILabel(AgentCLIHermes), protocol)
	}
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
	if err := writeAgentCLIFile(home, hermesConfigPath, []byte(strings.Join(lines, "\n")+"\n")); err != nil {
		return nil, nil, err
	}
	env := map[string]string{
		"HERMES_HOME": filepath.ToSlash(filepath.Join(homeEnv, hermesHomeKey)),
	}
	argv = insertBeforeFlag(argv, "-z", "--usage-file", filepath.ToSlash(filepath.Join(homeEnv, hermesUsagePath)))
	return argv, env, nil
}