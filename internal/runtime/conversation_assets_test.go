package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestConversationPrepareRemovesSkillLinksWithoutReadingTargets(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	const skill = "home/.agents/skills/@publisher/design/SKILL.md"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, skill)), 0700); err != nil {
		t.Fatal(err)
	}
	original := map[string]string{filepath.Join(root, skill): "installed skill", filepath.Join(outside, "content"): "outside data"}
	for path, content := range original {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		"home/.agents/skills/design":   "@publisher/design",
		"home/.agents/skills/dangling": "missing-skill",
		"home/.agents/skills/external": outside,
		"home/.claude/skills":          outside,
	}
	var want []string
	for link, target := range links {
		path := filepath.Join(root, link)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		want = append(want, link)
	}
	slices.Sort(want)
	pool := &ConversationProcessPool{}
	prepare := ConversationProcessRequest{Operation: "prepare"}
	result, err := pool.Handle(t.Context(), root, "", nil, prepare, nil)
	if err != nil || !slices.Equal(result.RemovedSkillLinks, want) {
		t.Fatalf("prepare removed %v, want %v: %v", result.RemovedSkillLinks, want, err)
	}
	if err := ValidateConversationRoot(root); err != nil {
		t.Fatalf("the next turn is still blocked: %v", err)
	}
	for path, content := range original {
		if got, err := os.ReadFile(path); err != nil || string(got) != content {
			t.Fatalf("link target changed: %q %v", got, err)
		}
	}
	if result, err = pool.Handle(t.Context(), root, "", nil, prepare, nil); err != nil || len(result.RemovedSkillLinks) > 0 {
		t.Fatalf("repeated prepare reported removed links again: %+v %v", result, err)
	}
}

func TestConversationSkillPreparationKeepsProtectedPathChecks(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	const skillLink = "home/.agents/skills/alias"
	const guardLink = "home/.codex/auth.json"
	for _, rel := range []string{skillLink, guardLink} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	var stateErr *ConversationStateError
	removed, err := RemoveConversationSkillLinks(root)
	if len(removed) != 0 || !errors.As(err, &stateErr) || !strings.Contains(err.Error(), guardLink) || !strings.Contains(err.Error(), "不支持符号链接") || strings.Contains(err.Error(), root) {
		t.Fatalf("protected path did not block preparation with its relative path: %v %v", removed, err)
	}
	for _, rel := range []string{skillLink, guardLink} {
		if got, err := os.Readlink(filepath.Join(root, rel)); err != nil || got != target {
			t.Fatalf("rejected preparation changed a link: %q %v", got, err)
		}
	}
}
