package httpapi

import (
	"errors"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// Task 1: 澄清的引擎解析。这些测试钉住的是「选了就不许改」这条契约：
// 任何一个非法或凭据缺失的引擎/模型组合都必须报错并带上原值，
// 而不是像旧的 clarifyAllowedModel 那样静默换成 CodeBuddy 默认模型。

func TestClarifyEngineOfReadsThePrefix(t *testing.T) {
	for _, tc := range []struct {
		modelID  string
		engine   string
		normalID string
	}{
		{"", "codebuddy", ""},
		{"default", "codebuddy", "default"},
		{"glm-5.3-flash-ioa", "codebuddy", "glm-5.3-flash-ioa"},
		{"codex:gpt-5.5", "codex", "codex:gpt-5.5"},
		{"claude-code:claude-opus-5", "claude-code", "claude-code:claude-opus-5"},
		{"opencode:glm-4", "opencode", "opencode:glm-4"},
	} {
		engine := clarifyEngineOf(tc.modelID)
		if engine.ID != tc.engine {
			t.Errorf("clarifyEngineOf(%q).ID = %q, want %q", tc.modelID, engine.ID, tc.engine)
		}
		if engine.ModelID != tc.normalID {
			t.Errorf("clarifyEngineOf(%q).ModelID = %q, want %q", tc.modelID, engine.ModelID, tc.normalID)
		}
	}
}

func TestClarifyResolveModelKeepsCodeBuddyDefaults(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODEL", "")
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "glm-5.3-flash-ioa")

	empty, err := clarifyResolveModel("", domain.User{})
	if err != nil {
		t.Fatalf("empty model must stay valid, got %v", err)
	}
	if empty.ID != "codebuddy" || empty.Model != assistantDefaultModel || empty.ModelID != assistantDefaultModel {
		t.Fatalf("empty model = %+v, want codebuddy/default/default", empty)
	}

	listed, err := clarifyResolveModel("glm-5.3-flash-ioa", domain.User{})
	if err != nil {
		t.Fatalf("listed CodeBuddy model rejected: %v", err)
	}
	if listed.ID != "codebuddy" || listed.ModelID != "glm-5.3-flash-ioa" {
		t.Fatalf("listed model = %+v", listed)
	}
}

func TestClarifyResolveModelRejectsUnknownCodeBuddyModel(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODEL", "glm-5.3-flash-ioa")
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "")

	_, err := clarifyResolveModel("not-a-model", domain.User{})
	if err == nil {
		t.Fatal("unknown CodeBuddy model must fail instead of being rewritten to the catalog default")
	}
	if !strings.Contains(err.Error(), "not-a-model") {
		t.Fatalf("error must name the rejected model, got %q", err.Error())
	}
}

func TestClarifyResolveModelAcceptsConfiguredEngine(t *testing.T) {
	user := domain.User{CodexAPIKey: "sk-test"}

	engine, err := clarifyResolveModel("codex:gpt-5.5", user)
	if err != nil {
		t.Fatalf("configured codex model rejected: %v", err)
	}
	if engine.ID != "codex" || engine.Model != "gpt-5.5" || engine.ModelID != "codex:gpt-5.5" {
		t.Fatalf("engine = %+v, want codex/gpt-5.5/codex:gpt-5.5", engine)
	}
}

func TestClarifyResolveModelRejectsEngineWithoutKey(t *testing.T) {
	_, err := clarifyResolveModel("codex:gpt-5.5", domain.User{})
	if err == nil {
		t.Fatal("an engine without a personal key must be rejected")
	}
	if !strings.Contains(err.Error(), "个人配置中配置 Codex API Key") {
		t.Fatalf("error = %q, want the missing-key message naming Codex", err.Error())
	}
}

func TestClarifyResolveModelRejectsUnknownEngine(t *testing.T) {
	_, err := clarifyResolveModel("foobar:x", domain.User{})
	if !errors.Is(err, errClarifyEngineUnknown) {
		t.Fatalf("err = %v, want errClarifyEngineUnknown", err)
	}
	if !strings.Contains(err.Error(), "foobar") {
		t.Fatalf("error must name the unknown engine, got %q", err.Error())
	}
}

func TestClarifyResolveModelRequiresBaseURL(t *testing.T) {
	user := domain.User{AgentCLICredentials: map[string]domain.AgentCLICredential{
		"opencode": {APIKey: "sk-test"},
	}}

	_, err := clarifyResolveModel("opencode:glm-4", user)
	if err == nil {
		t.Fatal("opencode without a Base URL must be rejected")
	}
	if !strings.Contains(err.Error(), "Base URL") {
		t.Fatalf("error = %q, want a message naming Base URL", err.Error())
	}

	user.AgentCLICredentials["opencode"] = domain.AgentCLICredential{APIKey: "sk-test", BaseURL: "https://gateway.example.com/v1"}
	engine, err := clarifyResolveModel("opencode:glm-4", user)
	if err != nil {
		t.Fatalf("opencode with a Base URL was rejected: %v", err)
	}
	if engine.ID != "opencode" || engine.ModelID != "opencode:glm-4" {
		t.Fatalf("engine = %+v", engine)
	}
}
