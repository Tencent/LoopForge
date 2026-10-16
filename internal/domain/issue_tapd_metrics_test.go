package domain

import (
	"strings"
	"testing"
)

func TestPlatformRequirementTAPDIDIsMetricsOnly(t *testing.T) {
	issue := Issue{ID: "platform-1", Source: "local", Kind: "requirement", TapdID: " 1001 "}
	if got := issue.RequirementTAPDID(); got != "1001" {
		t.Fatalf("metric TAPD ID = %q", got)
	}
	block := issue.IdentityBlock()
	if !strings.Contains(block, "统计关联 TAPD ID：1001") || strings.Contains(block, "回写") {
		t.Fatalf("manual metrics link must not instruct write-back: %s", block)
	}
	issue.TapdID = ""
	if issue.RequirementTAPDID() != "" || !strings.Contains(issue.IdentityBlock(), "外部 ID：无") {
		t.Fatalf("unbound platform issue is valid: %+v", issue)
	}
	issue.Source = "tapd"
	issue.TapdID = "9999"
	issue.ExternalRef = &ExternalRef{Source: "tapd", ExternalID: "2002"}
	if got := issue.RequirementTAPDID(); got != "2002" {
		t.Fatalf("synced tracker ID must win: %q", got)
	}
}
