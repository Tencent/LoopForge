//go:build clilive

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

var liveCLIOrder = []string{"codebuddy", "codex", "claude-code", "opencode", "cursor", "agy", "kimi", "pi", "dsh"}

type liveCLIConfig struct {
	Key         string `json:"key"`
	BaseURL     string `json:"baseUrl"`
	Protocol    string `json:"protocol"`
	Model       string `json:"model"`
	Environment string `json:"environment"`
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

func TestLiveCLIConversations(t *testing.T) {
	if os.Getenv("CLI_LIVE") != "1" {
		t.Skip("set CLI_LIVE=1 to run CLI conversations")
	}
	cfg := loadLiveConfig(t)
	root := os.Getenv("CLI_LIVE_WORKSPACE")
	if root == "" {
		t.Fatal("CLI_LIVE_WORKSPACE is required")
	}
	for _, kind := range liveCLIOrder {
		item, ok := cfg[kind]
		if !ok || strings.TrimSpace(item.Key) == "" {
			t.Run(kind, func(t *testing.T) { t.Skip("no key") })
			continue
		}
		t.Run(kind, func(t *testing.T) {
			runLiveChat(t, filepath.Join(root, "chat", kind), kind, item)
		})
	}
}

func runLiveChat(t *testing.T, root, kind string, item liveCLIConfig) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout())
	defer cancel()
	user, model := liveCLIUser(kind, item)
	text, session, err := runAssistantCLI(ctx, assistantCLIRequest{
		Prompt:       "Reply with exactly the single word pong. Do not use tools.",
		Model:        model,
		Executor:     kind,
		SystemPrompt: "Reply with exactly the single word pong. Do not use tools.",
		Native:       true,
		SessionRoot:  root,
		UserID:       user.ID,
	}, user, nil)
	text = redactLive(text + "\n" + session)
	if err != nil {
		t.Fatalf("对话失败: %s\n%s", redactLive(err.Error()), clipLive(text))
	}
	if !strings.Contains(strings.ToLower(text), "pong") {
		t.Fatalf("回复里没有 pong\n%s", clipLive(text))
	}
	t.Logf("ok %s", clipLive(text))
}

func TestLiveCLITitles(t *testing.T) {
	if os.Getenv("CLI_LIVE") != "1" {
		t.Skip("set CLI_LIVE=1 to run CLI title summaries")
	}
	cfg := loadLiveConfig(t)
	for _, kind := range liveCLIOrder {
		item, ok := cfg[kind]
		if !ok || strings.TrimSpace(item.Key) == "" {
			t.Run(kind, func(t *testing.T) { t.Skip("no key") })
			continue
		}
		t.Run(kind, func(t *testing.T) {
			if kind == "cursor" && strings.TrimSpace(item.Model) == "" {
				item.Model = "auto"
			}
			user, model := liveCLIUser(kind, item)
			ctx, cancel := context.WithTimeout(context.Background(), liveTimeout())
			defer cancel()
			started := time.Now()
			title, err := summarizeAssistantTitle(ctx, user, model, "帮我梳理一下积分商城的需求，按模块列出现有缺口，并给出两周的排期建议", nil)
			if err != nil || strings.TrimSpace(title) == "" {
				t.Fatalf("标题总结失败: %s", redactLive(fmt.Sprint(err)))
			}
			t.Logf("ok %s (%s)", title, time.Since(started).Round(time.Second))
		})
	}
}

func liveCLIUser(kind string, item liveCLIConfig) (domain.User, string) {
	user := domain.User{ID: 1, LoginName: "live"}
	model := item.Model
	switch kind {
	case "codebuddy":
		user.CodeBuddyAPIKey = item.Key
		user.CodeBuddyInternetEnvironment = item.Environment
	case "codex":
		user.CodexAPIKey = item.Key
		user.CodexBaseURL = item.BaseURL
		if model != "" {
			model = "codex:" + model
		}
	case "claude-code":
		user.ClaudeCodeAPIKey = item.Key
		user.ClaudeCodeBaseURL = item.BaseURL
		if model != "" {
			model = "claude-code:" + model
		}
	default:
		user.AgentCLICredentials = map[string]domain.AgentCLICredential{
			kind: {APIKey: item.Key, BaseURL: item.BaseURL, Protocol: item.Protocol},
		}
		if model != "" {
			model = kind + ":" + model
		}
	}
	return user, model
}
