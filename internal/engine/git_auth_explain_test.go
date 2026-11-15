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
	for _, want := range []string{"Git 访问令牌", "协议、主机和端口", "用户名", "could not read Username"} {
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

func TestExplainGitAuthFailureNamesTheCNBToken(t *testing.T) {
	quit := errors.New(`repository checkout: git clone https://cnb.cool/org/repo.git: exit status 128: fatal: credential helper '!f() { printf 'quit=1\n'; }; f' told us to quit`)
	if got := explainGitAuthFailure(quit, true); !strings.Contains(got.Error(), "未配置 CNB 访问令牌") || !errors.Is(got, quit) {
		t.Fatalf("a missing CNB token must be named, with the raw error kept: %v", got)
	}
	rejected := errors.New("repository checkout: git clone https://cnb.cool/org/repo.git: exit status 128: remote: Authentication failed for 'https://cnb.cool/org/repo.git/'")
	if got := explainGitAuthFailure(rejected, false); !strings.Contains(got.Error(), "CNB 访问令牌无效") || strings.Contains(got.Error(), "GitHub") {
		t.Fatalf("a rejected CNB token must not blame the GitHub token: %v", got)
	}
}
