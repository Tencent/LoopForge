package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Only inspect paths used by platform-side materialization. Native CLIs own
// their logs and caches (including debug/latest links); WalkDir never follows
// those links, and the platform does not read or write through them.
func ValidateConversationRoot(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !conversationManagedPath(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("会话状态包含符号链接，拒绝宿主访问：%s", path)
		}
		if entry.IsDir() && path == filepath.Join(root, "ws") {
			return filepath.SkipDir
		}
		return nil
	})
}

func conversationManagedPath(rel string) bool {
	// All top-level files are platform inputs, checkpoints or hooks. The ws
	// directory itself must be real, but its native contents are not inspected.
	if !strings.Contains(rel, "/") {
		return true
	}
	for _, paths := range [][]string{conversationAssetTrees, conversationGuardFiles, conversationManagedFiles} {
		for _, path := range paths {
			if rel == path || strings.HasPrefix(path, rel+"/") {
				return true
			}
		}
	}
	for _, tree := range conversationAssetTrees {
		if strings.HasPrefix(rel, tree+"/") {
			return true
		}
	}
	return false
}

// Credential/config targets and directories created or removed by the host,
// in addition to synchronized assets and read-only approval guards.
var conversationManagedFiles = []string{
	"home/tmp", "home/.agents/agents", "home/.pi/agent/agents", "home/.gemini/config/agents",
	"home/.config/opencode/opencode.json", "home/.cursor/mcp.json", "home/.cursor/cli-config.json",
	"home/.gemini/config/mcp_config.json", "home/.gemini/antigravity-cli/settings.json",
	"home/.kimi-code/mcp.json", "home/.pi/agent/mcp.json", "home/.pi/agent/models.json",
	"home/.dsh/agentflow-mcp.json", "home/.dsh/agentflow-model.json",
	"home/.zcode/v2",
}

// Only platform-generated inputs travel to the execution host. Native
// transcripts, session maps, locks and provider credentials never do.
var conversationAssetTrees = []string{
	"home/.codebuddy/skills", "home/.codebuddy/agents",
	"home/.claude/skills", "home/.claude/agents",
	"home/.codex/skills", "home/.agents/skills", "home/.pi/agent/skills",
	"home/.gemini/config/skills",
	"attachments",
}

var conversationAssetFiles = []string{
	"current-mcp.json", "current-settings.json", "claude-mcp.json", "claude-settings.json", "mcp.json", "codex-native-guard.py", "kimi-agent.md",
	"home/.config/opencode/opencode.json", "home/.cursor/mcp.json", "home/.cursor/cli-config.json", "home/.gemini/config/mcp_config.json", "home/.kimi-code/mcp.json", "home/.pi/agent/mcp.json", "home/.dsh/agentflow-mcp.json",
	"home/.zcode/cli/config.json",
}

// ConversationAssetDirectory permits only ancestors of generated inputs and
// complete asset trees. Native journals and caches are never traversed, while
// a skill may legitimately contain directories named projects, sessions or tmp.
func ConversationAssetDirectory(rel string) bool {
	if rel == "." {
		return true
	}
	for _, dir := range conversationAssetTrees {
		if rel == dir || strings.HasPrefix(dir, rel+"/") || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	for _, path := range conversationAssetFiles {
		if strings.HasPrefix(path, rel+"/") {
			return true
		}
	}
	return false
}

func ConversationAsset(rel string) (allowed, rewrite bool) {
	for _, dir := range conversationAssetTrees {
		if strings.HasPrefix(rel, dir+"/") {
			return true, false
		}
	}
	for _, path := range conversationAssetFiles {
		if rel == path {
			return true, true
		}
	}
	if !strings.Contains(rel, "/") && strings.HasSuffix(rel, "-system.md") {
		return true, true
	}
	return false, false
}
