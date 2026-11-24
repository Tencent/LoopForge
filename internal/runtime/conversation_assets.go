package runtime

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ConversationStateError describes a preparation failure without exposing the
// host path of the conversation.
type ConversationStateError struct {
	Message string
	cause   error
}

func NewConversationStateError(operation, path string, cause error) *ConversationStateError {
	reason := cause
	var pathErr *fs.PathError
	var linkErr *os.LinkError
	if errors.As(cause, &pathErr) {
		reason = pathErr.Err
	} else if errors.As(cause, &linkErr) {
		reason = linkErr.Err
	}
	return &ConversationStateError{
		Message: fmt.Sprintf("%s %q 失败（%v）", operation, path, reason),
		cause:   cause,
	}
}

func (e *ConversationStateError) Error() string { return e.Message }
func (e *ConversationStateError) Unwrap() error { return e.cause }

// Only inspect paths used by platform-side materialization. Native CLIs own
// their logs and caches (including debug/latest links); WalkDir never follows
// those links, and the platform does not read or write through them.
func ValidateConversationRoot(root string) error {
	_, err := scanConversationRoot(root, false)
	return err
}

// ConversationSkillLinks lists recoverable links and rejects invalid paths in
// the rest of the conversation. Use this for read-only checks before taking a
// lock. The links must be removed before writing assets or starting a CLI.
func ConversationSkillLinks(root string) ([]string, error) {
	return scanConversationRoot(root, true)
}

// RemoveConversationSkillLinks unlinks skill entries left by native installers.
// The installer may have pointed an entry anywhere; only the link itself is
// removed, using a root-scoped operation. Callers serialize asset preparation
// with the native process.
func RemoveConversationSkillLinks(root string) ([]string, error) {
	links, err := ConversationSkillLinks(root)
	if err != nil || len(links) == 0 {
		return nil, err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, NewConversationStateError("打开会话目录", ".", err)
	}
	defer dir.Close()
	var removed []string
	for _, link := range links {
		info, err := dir.Lstat(link)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, NewConversationStateError("检查技能链接", link, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if err := dir.Remove(link); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return removed, NewConversationStateError("移除技能链接", link, err)
		}
		removed = append(removed, link)
	}
	return removed, nil
}

func scanConversationRoot(root string, collectSkillLinks bool) ([]string, error) {
	var links []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return NewConversationStateError("检查会话目录", ".", err)
		}
		rel = filepath.ToSlash(rel)
		if walkErr != nil {
			if os.IsNotExist(walkErr) && rel == "." {
				return nil
			}
			return NewConversationStateError("检查会话路径", rel, walkErr)
		}
		if !conversationManagedPath(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if collectSkillLinks && conversationSkillPath(rel) {
				links = append(links, rel)
				return nil
			}
			return NewConversationStateError("检查会话路径", rel, errors.New("不支持符号链接"))
		}
		if entry.IsDir() && path == filepath.Join(root, "ws") {
			return filepath.SkipDir
		}
		return nil
	})
	return links, err
}

func conversationSkillPath(rel string) bool {
	for _, tree := range conversationSkillTrees {
		if rel == tree || strings.HasPrefix(rel, tree+"/") {
			return true
		}
	}
	return false
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
	"home/.zcode/v2", "home/.codex/auth.json", "home/.claude/.credentials.json",
}

var conversationSkillTrees = []string{
	"home/.codebuddy/skills", "home/.claude/skills",
	"home/.codex/skills", "home/.agents/skills", "home/.pi/agent/skills",
	"home/.gemini/config/skills",
}

// Only platform-generated inputs travel to the execution host. Native
// transcripts, session maps, locks and provider credentials never do.
var conversationAssetTrees = append([]string{
	"home/.codebuddy/agents", "home/.claude/agents", "attachments",
}, conversationSkillTrees...)

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
