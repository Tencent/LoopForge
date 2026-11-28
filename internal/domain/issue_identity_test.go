package domain

import (
	"strings"
	"testing"
)

func TestIssueIdentityBlockTAPD(t *testing.T) {
	issue := Issue{
		ID:     "tapd-1010000000100001887",
		Source: "tapd",
		ExternalRef: &ExternalRef{
			Source:       "tapd",
			ExternalID:   "1010000000100001887",
			ExternalType: "story",
			ExternalURL:  "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001887",
		},
	}
	got := issue.IdentityBlock()
	for _, want := range []string{
		"需求身份（平台权威，禁止忽略）",
		"平台工作项：tapd-1010000000100001887",
		"来源：TAPD",
		"类型：story",
		"外部 ID：1010000000100001887",
		"https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001887",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("identity block missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "回写") {
		t.Fatalf("identity block must not instruct write-back:\n%s", got)
	}
}

func TestIssueIdentityBlockLocal(t *testing.T) {
	got := Issue{ID: "iss-local"}.IdentityBlock()
	if !strings.Contains(got, "来源：本地") || !strings.Contains(got, "外部 ID：无") || strings.Contains(got, "回写") {
		t.Fatalf("local identity:\n%s", got)
	}
}

// GitHub issues used to fall through to "本地", telling the LOOP agent the
// GitHub issue it was fixing had no tracker behind it.
func TestIssueIdentityBlockGitHub(t *testing.T) {
	issue := Issue{
		ID:     "gh-repo-1",
		Source: "github",
		ExternalRef: &ExternalRef{
			Source:       "github",
			ExternalID:   "6663098674",
			ExternalIID:  1,
			ExternalType: "issue",
			ExternalURL:  "https://github.com/acme/widgets/issues/1",
		},
	}
	got := issue.IdentityBlock()
	for _, want := range []string{"来源：GitHub", "外部编号：1", "https://github.com/acme/widgets/issues/1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("identity block missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "来源：本地") {
		t.Fatalf("GitHub issue labelled local:\n%s", got)
	}
}
