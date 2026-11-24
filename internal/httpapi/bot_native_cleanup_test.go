package httpapi

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/repo"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Mixed-source CLI homes: only the native logs that a due Bot mapping points
// at are removed; web-assistant logs in the same home, other users' homes,
// symlinked trees and unmapped files stay.
func TestBotNativeLogCleanupRemovesOnlyMappedDueLogs(t *testing.T) {
	base := t.TempDir()
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", base)
	write := func(path string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	home := assistantCLIHome(7)
	otherHome := assistantCLIHome(8)
	outside := t.TempDir()
	files := map[string]string{
		"bot-cb":       write(filepath.Join(home, ".codebuddy", "projects", "-ws", "afbotturn01.jsonl")),
		"bot-cb-dir":   write(filepath.Join(home, ".codebuddy", "projects", "-ws", "afbotturn01", "subagent.jsonl")),
		"bot-cb-sess":  write(filepath.Join(home, ".codebuddy", "sessions", "afbotturn01.json")),
		"web":          write(filepath.Join(home, ".codebuddy", "projects", "-ws", "afwebchat001.jsonl")),
		"recent-bot":   write(filepath.Join(home, ".codebuddy", "projects", "-ws", "afbotrecent1.jsonl")),
		"other-user":   write(filepath.Join(otherHome, ".codebuddy", "projects", "-ws", "afbotturn01.jsonl")),
		"codex":        write(filepath.Join(home, ".codex", "sessions", "2026", "05", "01", "rollout-2026-05-01T10-00-00-0199aaaa-bbbb-cccc-dddd-eeeeffff0001.jsonl")),
		"codex-other":  write(filepath.Join(home, ".codex", "sessions", "2026", "05", "01", "rollout-2026-05-01T11-00-00-0199aaaa-bbbb-cccc-dddd-eeeeffff0002.jsonl")),
		"claude-out":   write(filepath.Join(outside, "projects", "-ws", "claudeturn01.jsonl")),
		"claude-guard": write(filepath.Join(home, ".claude-real", "x")),
	}
	// ~/.claude is a link that leaves the home: never followed.
	if err := os.Symlink(outside, filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexThreadsPath(assistantSessionRoot(7)), []byte(`{"afbotcodex01":"0199aaaa-bbbb-cccc-dddd-eeeeffff0001","afweb000001":"0199aaaa-bbbb-cccc-dddd-eeeeffff0002"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	start := func(engine, requested, native string, finishedAgo time.Duration) string {
		t.Helper()
		id, err := store.StartBotNativeSession(repo.BotNativeSession{ConfigID: "cfg", ProjectID: "p", UserID: 7, InteractionID: "i-" + requested, Engine: engine, RequestedSessionID: requested, StorageScope: "assistant-cli-home"})
		if err != nil {
			t.Fatal(err)
		}
		if id, err = store.FinishBotNativeSession(id, native, "completed"); err != nil {
			t.Fatal(err)
		}
		if err := store.DB.Table("bot_native_sessions").Where("id = ?", id).Update("finished_at", now.Add(-finishedAgo)).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}
	day := 24 * time.Hour
	cb := start("codebuddy", "afbotturn01", "afbotturn01", 100*day)
	recent := start("codebuddy", "afbotrecent1", "afbotrecent1", 10*day)
	codex := start("codex", "afbotcodex01", "0199aaaa-bbbb-cccc-dddd-eeeeffff0001", 100*day)
	claude := start("claude-code", "claudeturn01", "claudeturn01", 100*day)
	gone := start("codebuddy", "afbotgone001", "afbotgone001", 100*day)
	other := start("opencode", "afbotopen001", "ses_open001", 100*day)

	svc := botinteraction.New(store)
	svc.NativeLogs = cleanBotNativeLogs
	policy := repo.BotRetentionPolicy{Body: 90 * day, Record: 180 * day}
	if _, handled, err := svc.RunRetention(context.Background(), now, policy); err != nil || handled != 5 {
		t.Fatalf("handled = %d err = %v", handled, err)
	}
	exists := func(key string) bool { _, err := os.Lstat(files[key]); return err == nil }
	for _, key := range []string{"bot-cb", "bot-cb-dir", "bot-cb-sess", "codex"} {
		if exists(key) {
			t.Errorf("%s not removed", key)
		}
	}
	for _, key := range []string{"web", "recent-bot", "other-user", "codex-other", "claude-out", "claude-guard"} {
		if !exists(key) {
			t.Errorf("%s removed", key)
		}
	}
	state := func(id string) (string, string) {
		status, paths, _, err := store.BotNativeCleanupState(id)
		if err != nil {
			t.Fatal(err)
		}
		return status, paths
	}
	if status, paths := state(cb); status != repo.BotNativeCleanupRemoved || paths != ".codebuddy/projects/-ws/afbotturn01\n.codebuddy/projects/-ws/afbotturn01.jsonl\n.codebuddy/sessions/afbotturn01.json" {
		t.Errorf("codebuddy = %s %q", status, paths)
	}
	if status, paths := state(codex); status != repo.BotNativeCleanupRemoved || !strings.HasSuffix(paths, "eeeeffff0001.jsonl") {
		t.Errorf("codex = %s %q", status, paths)
	}
	if status, _ := state(claude); status != repo.BotNativeCleanupMissing {
		t.Errorf("claude behind a symlink = %s", status)
	}
	if status, _ := state(gone); status != repo.BotNativeCleanupMissing {
		t.Errorf("missing = %s", status)
	}
	if status, _ := state(other); status != repo.BotNativeCleanupUnsupported {
		t.Errorf("unsupported engine = %s", status)
	}
	if status, _ := state(recent); status != "" {
		t.Errorf("recent mapping touched: %s", status)
	}
	threads, _ := os.ReadFile(codexThreadsPath(assistantSessionRoot(7)))
	if strings.Contains(string(threads), "afbotcodex01") || !strings.Contains(string(threads), "afweb000001") {
		t.Errorf("codex thread index = %s", threads)
	}
	// After the record horizon, settled mappings go; unsupported stay listed.
	if _, _, err := svc.RunRetention(context.Background(), now.Add(100*day), policy); err != nil {
		t.Fatal(err)
	}
	if status, _, _, err := store.BotNativeCleanupState(cb); err == nil {
		t.Errorf("settled mapping kept: %s", status)
	}
	if status, _ := state(other); status != repo.BotNativeCleanupUnsupported {
		t.Errorf("unsupported mapping removed: %s", status)
	}
}

func TestBotRetentionPolicyFromEnv(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		body, record         string
		wantBody, wantRecord time.Duration
	}{
		{"", "", 90 * day, 180 * day},
		{"30", "60", 30 * day, 60 * day},
		{"0", "365", 0, 365 * day},
		{"120", "60", 120 * day, 120 * day},
		{"abc", "-1", 90 * day, 180 * day},
		{"30", "0", 30 * day, 0},
	}
	for _, c := range cases {
		t.Setenv("AGENTFLOW_BOT_BODY_RETENTION_DAYS", c.body)
		t.Setenv("AGENTFLOW_BOT_RECORD_RETENTION_DAYS", c.record)
		p := botinteraction.RetentionPolicyFromEnv()
		if p.Body != c.wantBody || p.Record != c.wantRecord || p.Batch != 1000 {
			t.Errorf("%q/%q = %+v", c.body, c.record, p)
		}
	}
}
