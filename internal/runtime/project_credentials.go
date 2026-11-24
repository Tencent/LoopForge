package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"agentflow/internal/domain"
)

type projectCredentialEnvKey struct{}

// WithProjectCredentialEnv scopes sensitive variables to commands launched
// for one execution. It does not change the sandbox profile or the host env.
func WithProjectCredentialEnv(ctx context.Context, env map[string]string) context.Context {
	if len(env) == 0 {
		return ctx
	}
	return context.WithValue(ctx, projectCredentialEnvKey{}, env)
}

func projectCommandEnvironment(ctx context.Context, command CommandSpec, profile map[string]string) (CommandSpec, error) {
	env, _ := ctx.Value(projectCredentialEnvKey{}).(map[string]string)
	if len(env) == 0 {
		return command, nil
	}
	occupied := make(map[string]struct{}, len(profile)+len(command.environment))
	for name := range profile {
		occupied[strings.ToUpper(name)] = struct{}{}
	}
	// Command bindings include empty values to suppress inherited settings.
	for name := range command.environment {
		occupied[strings.ToUpper(name)] = struct{}{}
	}
	for name := range env {
		if err := domain.ValidateProjectCredentialVariable(name); err != nil {
			return CommandSpec{}, err
		}
		if _, exists := occupied[strings.ToUpper(name)]; exists {
			return CommandSpec{}, fmt.Errorf("项目凭据环境变量 %s 与沙箱配置或执行器冲突", name)
		}
	}
	command.projectCredentialEnv = env
	if command.environment["OTEL_LOG_TOOL_DETAILS"] != "" {
		command.environment = mergeEnvironmentMaps(command.environment, map[string]string{"OTEL_LOG_TOOL_DETAILS": "0"})
	}
	return command, nil
}

// projectCredentialRedactor filters command output before it is streamed or
// saved. Escaped values matter for CLI events that carry JSON strings.
func projectCredentialRedactor(env map[string]string) func(string) string {
	if len(env) == 0 {
		return nil
	}
	values := make(map[string]bool)
	for _, value := range env {
		for _, part := range strings.Split(value, "\n") {
			part = strings.TrimSuffix(part, "\r")
			if part == "" {
				continue
			}
			values[part] = true
			values[url.QueryEscape(part)] = true
			values[url.PathEscape(part)] = true
			escaped, _ := json.Marshal(part)
			values[string(escaped[1:len(escaped)-1])] = true
		}
	}
	keys := slices.Collect(maps.Keys(values))
	slices.SortFunc(keys, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(keys))
	for _, value := range keys {
		pairs = append(pairs, value, "[redacted]")
	}
	mask := strings.NewReplacer(pairs...).Replace
	return func(line string) string {
		masked := mask(line)
		trimmed := strings.TrimSpace(line)
		if masked == line || !(strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
			return masked
		}
		// Decode structured CLI events before replacing secrets. A password may
		// contain quotes or backslashes used by the JSON envelope itself.
		if !json.Valid([]byte(line)) {
			return masked
		}
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return masked
		}
		data, err := json.Marshal(redactCredentialJSON(value, mask))
		if err != nil {
			return masked
		}
		return string(data)
	}
}

func redactCredentialJSON(value any, mask func(string) string) any {
	switch v := value.(type) {
	case map[string]any:
		for key, field := range v {
			v[key] = redactCredentialJSON(field, mask)
		}
	case []any:
		for index, field := range v {
			v[index] = redactCredentialJSON(field, mask)
		}
	case string:
		return mask(v)
	case json.Number:
		if masked := mask(v.String()); masked != v.String() {
			return masked
		}
	}
	return value
}
