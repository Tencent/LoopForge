package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestConversationAssetDirectoryOnlyWalksAssetAncestors(t *testing.T) {
	for _, rel := range []string{
		".", "home", "home/.claude", "home/.claude/skills", "home/.claude/skills/review",
		"home/.claude/skills/review/projects", "home/.agents/skills/sessions/tmp",
		"attachments", "attachments/a",
		// Ancestors of generated config files are walked so the file is reached.
		"home/.cursor", "home/.zcode/cli", "home/.gemini/config",
	} {
		if !ConversationAssetDirectory(rel) {
			t.Errorf("asset directory %q skipped", rel)
		}
	}
	for _, rel := range []string{
		"home/.claude/projects", "home/.codex/sessions", "home/.cache", "tmp",
		"home/.claude/skillset", "attachmentsx", "home/.zcode/cli/logs", "home/.cursor/chats",
		// A name prefix of a generated file is not its ancestor.
		"current", "home/.cursor/mc",
	} {
		if ConversationAssetDirectory(rel) {
			t.Errorf("native directory %q walked", rel)
		}
	}
}

func TestConversationAssetFilesAndRewrites(t *testing.T) {
	for _, c := range []struct {
		rel              string
		allowed, rewrite bool
	}{
		{"home/.claude/skills/review/SKILL.md", true, false},
		{"attachments/a/source.pdf", true, false},
		{"current-mcp.json", true, true},
		{"home/.zcode/cli/config.json", true, true},
		{"codebuddy-system.md", true, true},
		{"nested/codebuddy-system.md", false, false},
		{"home/.claude/skills", false, false},
		{"home/.claude/projects/journal.jsonl", false, false},
		{"home/.zcode/cli/credentials.json", false, false},
		{"attachments", false, false},
	} {
		allowed, rewrite := ConversationAsset(c.rel)
		if allowed != c.allowed || rewrite != c.rewrite {
			t.Errorf("ConversationAsset(%q) = %v/%v, want %v/%v", c.rel, allowed, rewrite, c.allowed, c.rewrite)
		}
	}
}

func TestConversationDockerAccessRejectsUnsafeOverrides(t *testing.T) {
	for _, c := range []struct{ host, network string }{
		{"evil/host", "net"},
		{`evil\host`, "net"},
		{"host\nx", "net"},
		{"host", "ne\rt"},
	} {
		t.Setenv("AGENTFLOW_CONVERSATION_RELAY_HOST", c.host)
		t.Setenv("AGENTFLOW_CONVERSATION_DOCKER_NETWORK", c.network)
		if _, _, err := conversationDockerAccess(context.Background()); err == nil {
			t.Errorf("host %q network %q accepted", c.host, c.network)
		}
	}
	t.Setenv("AGENTFLOW_CONVERSATION_RELAY_HOST", " relay.internal ")
	t.Setenv("AGENTFLOW_CONVERSATION_DOCKER_NETWORK", " loopforge ")
	host, network, err := conversationDockerAccess(context.Background())
	if err != nil || host != "relay.internal" || network != "loopforge" {
		t.Fatalf("explicit overrides = %q %q %v", host, network, err)
	}
}

func TestConversationSetupOutputKeepsTail(t *testing.T) {
	if got := conversationSetupOutput([]byte("  short failure \n")); got != "short failure" {
		t.Fatalf("short output = %q", got)
	}
	long := strings.Repeat("a", 5000) + strings.Repeat("b", 2990) + " tail-error"
	got := conversationSetupOutput([]byte(long))
	if len(got) != 3000 || !strings.HasSuffix(got, "tail-error") || !strings.HasPrefix(got, "bbb") {
		t.Fatalf("long output kept %d bytes: %q…", len(got), got[:20])
	}
}

func TestConversationCommandForCarriesCommandSpec(t *testing.T) {
	command := CommandSpec{Argv: []string{"cli", "--model", "m"}, Provider: "codebuddy", AgentCLI: AgentCLIOptions{Model: "m"}}
	credentials := RunCredentials{}
	wire := ConversationCommandFor(command, credentials)
	if strings.Join(wire.Argv, " ") != "cli --model m" || wire.Provider != "codebuddy" || wire.AgentCLI.Model != "m" || wire.Interactive {
		t.Fatalf("wire command = %+v", wire)
	}
	command.Stdin = strings.NewReader("prompt")
	if !ConversationCommandFor(command, credentials).Interactive {
		t.Fatal("stdin command not marked interactive")
	}
}
