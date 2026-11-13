package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestParseDSHVersion(t *testing.T) {
	minimum, _, _ := parseDSHVersion(assistantMinDSHVersion)
	for _, tc := range []struct {
		version string
		valid   bool
		allowed bool
	}{
		{"0.2.1-rc.5", true, false},
		{"0.2.2-rc.0", true, false},
		{"0.2.2-rc.1", true, true},
		{"v0.2.2-rc.2", true, true},
		{"0.2.2", true, true},
		{"0.2.3-rc.1", true, true},
		{"0.2.2-beta.1", false, false},
		{"not a version", false, false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			got, _, ok := parseDSHVersion(tc.version)
			if ok != tc.valid {
				t.Fatalf("parse valid=%v, want %v", ok, tc.valid)
			}
			if ok && dshVersionAtLeast(got, minimum) != tc.allowed {
				t.Fatalf("version %q allowed=%v, want %v", tc.version, dshVersionAtLeast(got, minimum), tc.allowed)
			}
		})
	}
}

func TestRunAssistantCLIRejectsOldDSHBeforeChatLaunch(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "dsh")
	launchLog := filepath.Join(dir, "chat-launches")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 0.2.1-rc.5; exit 0; fi\nprintf launched >> " + shellQuote(launchLog) + "\nexit 99\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_DSH_BIN", bin)
	user := domain.User{ID: 1, AgentCLICredentials: map[string]domain.AgentCLICredential{
		runtime.AgentCLIDeepSeek: {APIKey: "test-only"},
	}}
	_, _, err := runAssistantCLI(context.Background(), assistantCLIRequest{
		Executor: "dsh", Model: "deepseek-v4-flash", Prompt: "hello", SessionRoot: dir, Native: true,
	}, user, nil)
	if err == nil || !strings.Contains(err.Error(), "0.2.1-rc.5") || !strings.Contains(err.Error(), assistantMinDSHVersion) {
		t.Fatalf("expected actionable version error, got %v", err)
	}
	if _, err := os.Stat(launchLog); !os.IsNotExist(err) {
		t.Fatalf("chat command launched with unsupported flags: stat err=%v", err)
	}
	_, message := assistantGenerationFailure(context.Background(), err)
	if !strings.Contains(message, assistantMinDSHVersion) {
		t.Fatalf("user-facing error does not mention required version: %q", message)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
