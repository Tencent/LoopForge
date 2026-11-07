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
