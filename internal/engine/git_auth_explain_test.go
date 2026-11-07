package engine

import (
	"errors"
	"strings"
	"testing"
)

func TestExplainGitAuthFailure(t *testing.T) {
	raw := errors.New("repository checkout: git clone https://git.example.com/a/b.git: exit status 128 (fatal: could not read Username for 'https://git.example.com': terminal prompts disabled) (exit 0)")
	missing := explainGitAuthFailure(raw, false)
	if !errors.Is(missing, raw) || !strings.HasPrefix(missing.Error(), "运行责任人未配置 Git Token") {
		t.Fatalf("missing token: %v", missing)
	}
	for _, want := range []string{"Contents 和 Pull requests", "https://github.com/settings/tokens", "could not read Username"} {
		if !strings.Contains(missing.Error(), want) {
			t.Fatalf("missing %q in %v", want, missing)
		}
	}
	if rejected := explainGitAuthFailure(raw, true); !strings.HasPrefix(rejected.Error(), "运行责任人的 Git Token 无效") {
		t.Fatalf("rejected token: %v", rejected)
	}
	for _, other := range []error{
		errors.New("repository checkout: git clone: exit status 128 (fatal: repository not found)"),
		errors.New("CodeBuddy Authentication failed: invalid api key"),
		nil,
	} {
		if got := explainGitAuthFailure(other, false); got != other {
			t.Fatalf("unrelated error rewritten: %v", got)
		}
	}
}
