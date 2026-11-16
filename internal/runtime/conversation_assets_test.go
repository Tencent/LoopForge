package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestConversationNativeCacheLinksDoNotBlockNextTurn(t *testing.T) {
	root := t.TempDir()
	for _, provider := range []string{".codebuddy", ".claude"} {
		dir := filepath.Join(root, "home", provider, "debug")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "turn.log"), []byte("native journal"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("turn.log", filepath.Join(dir, "latest")); err != nil {
			t.Fatal(err)
		}
	}
	// Native caches may link to an installer outside HOME. Platform asset
	// validation must not visit these paths, even if the link is dangling.
	if err := os.Symlink("/unavailable/native/cache", filepath.Join(root, "home", "cache")); err != nil {
		t.Fatal(err)
	}
	pool := &ConversationProcessPool{}
	if _, err := pool.Handle(context.Background(), root, "", nil, ConversationProcessRequest{Operation: "prepare"}, nil); err != nil {
		t.Fatalf("native log/cache links broke next turn: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "home", ".claude", "debug", "latest")); err != nil || string(data) != "native journal" {
		t.Fatalf("native log changed: %q %v", data, err)
	}
}

func TestConversationPlatformPathsRejectLinks(t *testing.T) {
	paths := []string{"home", "ws", "attachments", "session.json", "session.lock", "codex-threads.json", "agent-cli-sessions.json", "native-approval.py", "current-settings.json", "home/tmp", "home/.codebuddy", "home/.config", "home/.config/opencode/plugins", "home/.agents/skills/x/run.sh"}
	paths = append(paths, conversationAssetTrees...)
	paths = append(paths, conversationGuardFiles...)
	paths = append(paths, conversationManagedFiles...)
	for _, rel := range paths {
		t.Run(rel, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), target); err != nil {
				t.Fatal(err)
			}
			if err := ValidateConversationRoot(root); err == nil {
				t.Fatal("platform accepted a symlink on a materialization path")
			}
		})
	}
}
