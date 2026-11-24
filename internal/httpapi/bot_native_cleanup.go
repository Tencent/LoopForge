package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agentflow/internal/repo"
)

// Native CLI logs of Bot turns (stage H). Bot turns run in the user's shared
// CLI home (assistantCLIHome), next to logs of other sources, so the cleanup
// only removes files the mapping identifies: the requested or reported
// session ID as the file name. It never follows symbolic links, never leaves
// the home of the user the mapping belongs to, and records each removed
// path relative to that home.

// botNativeScopeHome is the only storage scope Bot turns use.
const botNativeScopeHome = "assistant-cli-home"

func botNativeSessionIDOK(id string) bool {
	id = strings.TrimSpace(id)
	return len(id) >= 8 && len(id) <= 191 && id != "." && id != ".." && !strings.ContainsAny(id, `/\*?[]`+"\x00")
}

// botNativeMatcher reports whether a file or directory name belongs to one of
// the session IDs of an engine, and which ID.
type botNativeMatcher func(name string, ids map[string]string) (string, bool)

func botNativeEngineRoots(engine, home string) ([]string, botNativeMatcher) {
	byName := func(name string, ids map[string]string) (string, bool) {
		for id, mapping := range ids {
			if assistantCLISessionName(name, id) {
				return mapping, true
			}
		}
		return "", false
	}
	switch engine {
	case "", assistantEngineCodeBuddy:
		return []string{filepath.Join(home, ".codebuddy", "projects"), filepath.Join(home, ".codebuddy", "sessions")}, byName
	case assistantEngineClaude:
		return []string{filepath.Join(home, ".claude", "projects")}, byName
	case assistantEngineCodex:
		// rollout-<timestamp>-<thread id>.jsonl under sessions/YYYY/MM/DD.
		return []string{filepath.Join(home, ".codex", "sessions")}, func(name string, ids map[string]string) (string, bool) {
			if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
				return "", false
			}
			for id, mapping := range ids {
				if strings.HasSuffix(name, "-"+id+".jsonl") {
					return mapping, true
				}
			}
			return "", false
		}
	}
	return nil, nil
}

// cleanBotNativeLogs is the botinteraction.NativeLogCleaner of the platform.
// Mappings are grouped by user and engine so each log tree is walked once.
func cleanBotNativeLogs(ctx context.Context, due []repo.BotNativeCleanup) []repo.BotNativeCleanupResult {
	results := make(map[string]*repo.BotNativeCleanupResult, len(due))
	order := make([]string, 0, len(due))
	type group struct {
		userID int64
		engine string
	}
	groups := map[group]map[string]string{}
	codexKeys := map[int64][]string{}
	for _, item := range due {
		res := &repo.BotNativeCleanupResult{ID: item.ID, Status: repo.BotNativeCleanupMissing}
		results[item.ID] = res
		order = append(order, item.ID)
		if item.StorageScope != botNativeScopeHome || item.UserID <= 0 {
			res.Status, res.Detail = repo.BotNativeCleanupUnsupported, "未知的存储范围："+item.StorageScope
			continue
		}
		if roots, _ := botNativeEngineRoots(item.Engine, ""); roots == nil {
			res.Status, res.Detail = repo.BotNativeCleanupUnsupported, "该 CLI 的原生日志位置未登记，需人工核查："+item.Engine
			continue
		}
		ids := []string{item.NativeID}
		if item.Engine != assistantEngineCodex {
			ids = append(ids, item.RequestedSessionID)
		} else if botNativeSessionIDOK(item.RequestedSessionID) {
			codexKeys[item.UserID] = append(codexKeys[item.UserID], item.RequestedSessionID)
		}
		key := group{item.UserID, item.Engine}
		for _, id := range ids {
			if !botNativeSessionIDOK(id) {
				continue
			}
			if groups[key] == nil {
				groups[key] = map[string]string{}
			}
			groups[key][id] = item.ID
		}
	}
	for key, ids := range groups {
		if ctx.Err() != nil {
			break
		}
		home := assistantCLIHome(key.userID)
		info, err := os.Lstat(home)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			for _, mapping := range ids {
				results[mapping].Status, results[mapping].Detail = repo.BotNativeCleanupError, "CLI HOME 不可用或不是目录"
			}
			continue
		}
		realHome, err := filepath.EvalSymlinks(home)
		if err != nil {
			continue
		}
		roots, match := botNativeEngineRoots(key.engine, home)
		for _, root := range roots {
			// The log tree must really live inside this home: a symbolic
			// link on the way (for example ~/.codebuddy) is not followed.
			if real, err := filepath.EvalSymlinks(root); err != nil || real != filepath.Join(realHome, strings.TrimPrefix(root, home)) {
				continue
			}
			_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					if path == root {
						return fs.SkipDir
					}
					return nil
				}
				if path == root || ctx.Err() != nil {
					return nil
				}
				if d.Type()&os.ModeSymlink != 0 {
					return nil
				}
				mapping, ok := match(d.Name(), ids)
				if !ok {
					return nil
				}
				res := results[mapping]
				rel, relErr := filepath.Rel(home, path)
				if relErr != nil || strings.HasPrefix(rel, "..") {
					res.Status, res.Detail = repo.BotNativeCleanupError, "日志路径不在该用户的 CLI HOME 内"
					return nil
				}
				if err := os.RemoveAll(path); err != nil {
					res.Status, res.Detail = repo.BotNativeCleanupError, "删除失败："+rel
				} else {
					res.Paths = append(res.Paths, filepath.ToSlash(rel))
					if res.Status != repo.BotNativeCleanupError {
						res.Status = repo.BotNativeCleanupRemoved
					}
				}
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			})
		}
	}
	for userID, keys := range codexKeys {
		forgetCodexThreads(assistantSessionRoot(userID), keys)
	}
	out := make([]repo.BotNativeCleanupResult, 0, len(order))
	for _, id := range order {
		res := results[id]
		sort.Strings(res.Paths)
		if res.Status == repo.BotNativeCleanupMissing && res.Detail == "" {
			res.Detail = "未找到映射指向的日志文件（可能已被删除）"
		}
		out = append(out, *res)
	}
	return out
}

// forgetCodexThreads removes Bot session keys from the Codex thread index of
// a user's shared root.
func forgetCodexThreads(root string, keys []string) {
	codexThreadsMu.Lock()
	defer codexThreadsMu.Unlock()
	data, err := os.ReadFile(codexThreadsPath(root))
	if err != nil {
		return
	}
	threads := map[string]string{}
	if json.Unmarshal(data, &threads) != nil {
		return
	}
	changed := false
	for _, key := range keys {
		if _, ok := threads[key]; ok {
			delete(threads, key)
			changed = true
		}
	}
	if !changed {
		return
	}
	if data, err = json.Marshal(threads); err == nil {
		_ = os.WriteFile(codexThreadsPath(root), data, 0o600)
	}
}
