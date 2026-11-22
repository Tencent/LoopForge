package httpapi

import (
	"encoding/json"
	"testing"
)

func TestNativeLocalCommandReadOnlyFollowsClaudeCodeDefaults(t *testing.T) {
	scope, ok := newNativeLocalScope("/w/repo", "/w/repo/src")
	if !ok {
		t.Fatal("scope")
	}
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"ls -la", true},
		{"cat main.go | head -n 20", true},
		{"grep -rn TODO . && wc -l *.go", true},
		{"cd .. && ls", true},
		{"cat ../README.md 2>&1", true},
		{"find . -name '*.go' -type f", true},
		{"rg --glob '*.go' handler", true},
		{"git status && git log --oneline -n 5", true},
		{"git diff HEAD~1 -- main.go", true},
		{"git branch -a", true},
		{"git branch --contains abc123", true},
		{"git config --get remote.origin.url", true},
		{"git worktree list --porcelain", true},
		{"ls missing 2>/dev/null; echo done", true},
		{"wc -l < main.go", true},
		{"pwd; which go", true},
		{"cat <<'EOF'\nplain\nEOF", true},

		{"npm test", false},
		{"rm -rf build", false},
		{"ls > files.txt", false},
		{"echo x >> main.go", false},
		{"cat main.go | tee copy.go", false},
		{"cat /etc/hosts", false},
		{"cat ~/.ssh/id_rsa", false},
		{"cat ../../../etc/hosts", false},
		{"cd /tmp && ls", false},
		{"cd ../.. && ls", false},
		{"cd .. && git status", false},
		{"git -C /tmp status", false},
		{"git -c core.pager=sh status", false},
		{"git push origin fix", false},
		{"git branch -D old", false},
		{"git branch newbranch", false},
		{"git tag v1", false},
		{"git config user.name x", false},
		{"git diff --output=patch.diff", false},
		{"git grep -Ovim TODO", false},
		{"find . -name '*.tmp' -delete", false},
		{"find . -exec rm {} ;", false},
		{"rg --pre ./run.sh x", false},
		{"cat $(echo main.go)", false},
		{"cat `echo main.go`", false},
		{"ls $HOME", false},
		{"cat {/etc/hosts,main.go}", false},
		{"ls .*", false},
		{"FOO=1 ls", false},
		{"(ls)", false},
		{"ls &> out.txt", false},
		{"cat < /etc/hosts", false},
		{"cat <<EOF\n$(id)\nEOF", false},
		{"diff <(ls) <(ls ..)", false},
		{"/bin/ls", false},
		{"ls 'unterminated", false},
		{"", false},
	} {
		raw, _ := json.Marshal(map[string]any{"command": tc.command})
		if got := nativeLocalCommandReadOnly(raw, scope); got != tc.want {
			t.Errorf("%q: read-only = %v, want %v", tc.command, got, tc.want)
		}
	}
}

func TestNativeLocalCommandReadOnlyReadsArgvAndWorkdir(t *testing.T) {
	scope, _ := newNativeLocalScope("/w/repo", "/w/repo")
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{`{"command":["bash","-lc","git status"]}`, true},
		{`{"command":["ls","-la"]}`, true},
		{`{"command":["bash","-lc","make build"]}`, false},
		{`{"command":["rm","x"]}`, false},
		{`{"command":"ls","workdir":"/w/repo/src"}`, true},
		{`{"command":"ls","workdir":"/tmp"}`, false},
		{`{"command":"ls","cmd":"rm x"}`, false},
		{`{"cmd":"git log"}`, true},
		{`{"file_path":"x"}`, false},
	} {
		if got := nativeLocalCommandReadOnly(json.RawMessage(tc.input), scope); got != tc.want {
			t.Errorf("%s: read-only = %v, want %v", tc.input, got, tc.want)
		}
	}
	outside, _ := newNativeLocalScope("/w/repo", "/tmp")
	if nativeLocalCommandReadOnly(json.RawMessage(`{"command":"ls"}`), outside) {
		t.Error("a CLI outside the workspace must ask even for ls")
	}
}

func TestNativeLocalReadAllowedStaysInsideTheWorkspace(t *testing.T) {
	scope, _ := newNativeLocalScope("/w/repo", "/w/repo")
	for _, tc := range []struct {
		tool, input string
		want        bool
	}{
		{"read", `{"file_path":"/w/repo/main.go"}`, true},
		{"read_file", `{"absolute_path":"/w/repo/a/b.go"}`, true},
		{"grep", `{"pattern":"/etc/passwd","path":"src"}`, true},
		{"glob", `{"pattern":"**/*.go"}`, true},
		{"ls", `{}`, true},
		{"read", `{"file_path":"/w/repository/x"}`, false},
		{"read", `{"file_path":"/etc/hosts"}`, false},
		{"read", `{"file_path":"~/.netrc"}`, false},
		{"read", `{"path":"../secret"}`, false},
		{"glob", `{"pattern":"/etc/**"}`, false},
		{"list_directory", `{"dir_path":"/"}`, false},
		{"read", `{"paths":["a.go","/etc/hosts"]}`, false},
		{"read", `{"path":42}`, false},
	} {
		if got := nativeLocalReadAllowed(tc.tool, json.RawMessage(tc.input), scope); got != tc.want {
			t.Errorf("%s %s: allowed = %v, want %v", tc.tool, tc.input, got, tc.want)
		}
	}
	if _, ok := newNativeLocalScope("", "/w"); ok {
		t.Error("an unknown workspace has no scope")
	}
	if _, ok := newNativeLocalScope("/", "/"); ok {
		t.Error("the filesystem root is not a workspace")
	}
	if _, ok := newNativeLocalScope("/w", "relative"); ok {
		t.Error("a relative cwd has no scope")
	}
}
