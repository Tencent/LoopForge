//go:build clilive

package runtime

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

var liveCLIOrder = []string{"codebuddy", "codex", "claude-code", "opencode", "cursor", "agy", "kimi", "pi", "dsh"}

type liveCLIConfig struct {
	Key         string `json:"key"`
	BaseURL     string `json:"baseUrl"`
	Protocol    string `json:"protocol"`
	Model       string `json:"model"`
	Environment string `json:"environment"`
}

func liveExecutorArgs(kind string) []string {
	switch kind {
	case "codebuddy":
		return []string{"-p", "--output-format", "stream-json", "--include-partial-messages", "-y", "--setting-sources", "none"}
	case "codex":
		return []string{"exec", "--json", "--sandbox", "workspace-write", "--skip-git-repo-check"}
	case "claude-code":
		return []string{"-p", "--output-format", "stream-json", "--permission-mode", "dontAsk"}
	default:
		return nil
	}
}

func liveTimeout() time.Duration {
	seconds, _ := strconv.Atoi(os.Getenv("CLI_LIVE_TIMEOUT"))
	if seconds <= 0 {
		seconds = 90
	}
	return time.Duration(seconds) * time.Second
}

func loadLiveConfig(t *testing.T) map[string]liveCLIConfig {
	t.Helper()
	path := os.Getenv("CLI_LIVE_CONFIG")
	if path == "" {
		t.Fatal("CLI_LIVE_CONFIG is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]liveCLIConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func redactLive(text string) string {
	for _, prefix := range []string{"sk-", "crsr_", "ck_", "AQ."} {
		for {
			i := strings.Index(text, prefix)
			if i < 0 {
				break
			}
			end := i + len(prefix)
			for end < len(text) && end < i+len(prefix)+80 {
				r := text[end]
				if r == ' ' || r == '\n' || r == '\t' || r == '"' || r == '\'' || r == ',' || r == '\\' {
					break
				}
				end++
			}
			text = text[:i] + "[redacted]" + text[end:]
		}
	}
	return text
}

func clipLive(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 700 {
		return text[:700] + "…"
	}
	return text
}
