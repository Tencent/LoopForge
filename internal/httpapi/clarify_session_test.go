package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestMaterializeClarifyRepoCreatesRealTree(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "cache", "new_dnspod")
	dest := filepath.Join(root, "ws", "new_dnspod")
	mustWriteClarifyFile(t, filepath.Join(src, "TagService.php"), "function processMsg() {}\n")
	mustWriteClarifyFile(t, filepath.Join(src, ".git", "HEAD"), "ref: refs/heads/master\n")

	if err := materializeClarifyRepo(src, dest, true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("materialized repo must not be a symlink")
	}
	got, err := os.ReadFile(filepath.Join(dest, "TagService.php"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "function processMsg() {}\n" {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Lstat(filepath.Join(dest, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git should be skipped, err=%v", err)
	}
}

func TestMaterializeClarifyRepoReplacesLeftoverSymlink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "cache", "repo")
	ws := filepath.Join(root, "ws")
	dest := filepath.Join(ws, "repo")
	mustWriteClarifyFile(t, filepath.Join(src, "a.php"), "ok\n")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, dest); err != nil {
		t.Fatal(err)
	}
	resetClarifyWorkspaceLinks(ws)
	if err := materializeClarifyRepo(src, dest, true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("leftover symlink was not replaced")
	}
	if _, err := os.ReadFile(filepath.Join(dest, "a.php")); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializeClarifyRepoReusesDirectoryUnlessRefresh(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dest := filepath.Join(root, "dest")
	mustWriteClarifyFile(t, filepath.Join(src, "a.txt"), "one\n")
	if err := materializeClarifyRepo(src, dest, true); err != nil {
		t.Fatal(err)
	}
	mustWriteClarifyFile(t, filepath.Join(dest, "session-only.txt"), "keep\n")
	mustWriteClarifyFile(t, filepath.Join(src, "b.txt"), "new\n")
	if err := materializeClarifyRepo(src, dest, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "session-only.txt")); err != nil {
		t.Fatal("later turn should reuse dest")
	}
	if _, err := os.Stat(filepath.Join(dest, "b.txt")); !os.IsNotExist(err) {
		t.Fatal("later turn must not recopy src")
	}
	if err := materializeClarifyRepo(src, dest, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "session-only.txt")); !os.IsNotExist(err) {
		t.Fatal("first turn should replace dest")
	}
	got, err := os.ReadFile(filepath.Join(dest, "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Fatalf("refreshed dest missing new file, got %q", got)
	}
}

func mustWriteClarifyFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCanResumeClarifySessionUsesResponsibleUserHome(t *testing.T) {
	root := t.TempDir()
	ownerHome := filepath.Join(root, "1", "home")
	newHome := filepath.Join(root, "13", "home")
	mustWriteClarifyFile(t, filepath.Join(ownerHome, ".codebuddy", "projects", "repo", "old-session.jsonl"), "conversation\n")
	for _, tc := range []struct {
		name        string
		home        string
		owner, user int64
		want        bool
	}{
		{"same owner", ownerHome, 1, 1, true},
		{"changed responsible user", newHome, 1, 13, false},
		{"legacy same user", ownerHome, 0, 1, true},
		{"legacy changed user", newHome, 0, 13, false},
		{"missing file", newHome, 13, 13, false},
		{"owner mismatch even with file", ownerHome, 13, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := canResumeClarifySession(tc.home, "old-session", tc.owner, tc.user); got != tc.want {
				t.Fatalf("canResume=%v, want %v", got, tc.want)
			}
		})
	}
	for _, id := range []string{"", "../old-session", "*", "old-session?", ".."} {
		if canResumeClarifySession(ownerHome, id, 1, 1) {
			t.Fatalf("invalid session ID accepted: %q", id)
		}
	}
}

func TestClarifySessionOutputError(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		wantError    bool
	}{
		{"missing resume", `{"type":"result","is_error":true,"result":"No conversation found with session ID: old-session"}`, true},
		{"error event", `{"type":"error","error":{"name":"SessionNotFoundError"}}`, true},
		{"valid result", `{"type":"result","is_error":false,"result":"已完成"}`, false},
		{"quoted error in analysis", `{"type":"assistant","message":{"content":[{"type":"text","text":"SessionNotFoundError"}]}}`, false},
		{"unrelated warning", `{"type":"result","is_error":true,"result":"max turns reached"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clarifySessionOutputError(tc.output); (got != nil) != tc.wantError {
				t.Fatalf("err=%v, wantError=%v", got, tc.wantError)
			}
		})
	}
}

func TestRunClarifyTurnDetectsMissingSessionWithZeroExitCode(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "codebuddy")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"is_error\":true,\"result\":\"No conversation found with session ID: old-session\"}'\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{}
	_, _, _, err := h.runClarifyTurn(context.Background(), root, bin, "default", "continue", "test", "old-session", root, true,
		runtime.RunCredentials{CodeBuddy: runtime.CodeBuddyCredential{APIKey: "test-key"}}, func(string, any) {}, clarifyTurnOpts{Native: true})
	if err == nil || !strings.Contains(err.Error(), "澄清会话不存在") {
		t.Fatalf("zero-exit missing session must trigger fallback: %v", err)
	}
}

func TestExecuteClarifyRebuildsSessionFromHistory(t *testing.T) {
	for _, tc := range []struct {
		name        string
		owner       int64
		localFile   bool
		firstResume bool
	}{
		{"changed responsible user", 1, false, false},
		{"legacy session in another home", 0, false, false},
		{"CLI rejects existing session with zero exit", 13, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("AGENTFLOW_CLARIFY_SESSION_DIR", filepath.Join(root, "sessions"))
			s, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			if err := s.PutUser(domain.User{ID: 13, CodeBuddyAPIKey: "test-key"}); err != nil {
				t.Fatal(err)
			}
			issue := domain.Issue{ID: "session-switch", ProjectID: "platform", AssigneeUserID: 13,
				Clarification: domain.ClarificationAssessment{SessionID: "old-session", SessionUserID: tc.owner}}
			if err := s.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			if tc.localFile {
				mustWriteClarifyFile(t, filepath.Join(clarifyCLIHome(13), ".codebuddy", "projects", "old-workspace", "old-session.jsonl"), "old conversation")
			}
			bin := filepath.Join(root, "codebuddy")
			t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
			script := `#!/bin/sh
for arg in "$@"; do printf '%s\n' "$arg"; done >> '` + filepath.Join(root, "args") + `'
printf '%s\n' 'END_CALL' >> '` + filepath.Join(root, "args") + `'
for arg in "$@"; do
 if [ "$arg" = '--resume' ]; then
  printf '%s\n' '{"type":"result","is_error":true,"result":"No conversation found with session ID: old-session"}'
  exit 0
 fi
done
printf '%s\n' '{"type":"result","result":"{\"summary\":\"已确认需求\",\"ready\":true,\"questions\":[]}"}'
`
			if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			request := chatClarifyRequest{Message: "保留最近90天", SessionID: "old-session"}
			request.History = append(request.History, struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}{Role: "user", Content: "保留最近90天"})
			h := &streamHandlers{deps: &deps{repo: s}}
			parsed, _, info, err := h.executeClarify(context.Background(), issue, request, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !parseClarifyModel(parsed, "").Ready {
				t.Fatalf("no recovered conclusion: %v", parsed)
			}
			if info["sessionId"] == "old-session" {
				t.Fatal("old session ID reused")
			}
			args, err := os.ReadFile(filepath.Join(root, "args"))
			if err != nil {
				t.Fatal(err)
			}
			calls := strings.Split(string(args), "END_CALL\n")
			if strings.Contains(calls[0], "--resume\n") != tc.firstResume {
				t.Fatalf("unexpected first invocation: %s", calls[0])
			}
			fresh := calls[0]
			if tc.firstResume {
				fresh = calls[1]
			}
			if strings.Contains(fresh, "--resume\n") || !strings.Contains(fresh, "--session-id\n") || strings.Contains(fresh, "old-session\n") {
				t.Fatalf("did not start a new session: %s", fresh)
			}
			if !strings.Contains(fresh, "保留最近90天") || !strings.Contains(fresh, "## 对话") {
				t.Fatalf("new session lost history: %s", fresh)
			}
			stored, _ := s.Issue(issue.ID)
			if stored.Clarification.SessionUserID != 13 || stored.Clarification.SessionID == "old-session" {
				t.Fatalf("session owner not rebound: %+v", stored.Clarification)
			}
		})
	}
}
