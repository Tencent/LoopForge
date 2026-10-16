package tracker

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestParseGongfengMRState(t *testing.T) {
	if got := parseGongfengMRState(`{"data":[{"iid":215,"state":"merged"}]}`, 215); got != "merged" {
		t.Fatalf("wrapped list: %q", got)
	}
	if got := parseGongfengMRState(`{"data":{"iid":1,"state":"opened"}}`, 1); got != "opened" {
		t.Fatalf("wrapped object: %q", got)
	}
	if got := parseGongfengMRState(`[{"iid":8,"state":"closed"}]`, 8); got != "closed" {
		t.Fatalf("bare list: %q", got)
	}
}

func TestParseGongfengMRStatusTargetBranch(t *testing.T) {
	state, target := parseGongfengMRStatus(`{"data":[{"iid":286,"state":"merged","target_branch":"agentflow/integration"}]}`, 286)
	if state != "merged" || target != "agentflow/integration" {
		t.Fatalf("got state=%q target=%q", state, target)
	}
	state, target = parseGongfengMRStatus(`{"data":{"iid":67,"state":"merged","target_branch":"master"}}`, 67)
	if state != "merged" || target != "master" {
		t.Fatalf("got state=%q target=%q", state, target)
	}
}

func TestParseGongfengMRRecordsPromote(t *testing.T) {
	items := parseGongfengMRRecords(`{"data":[{"iid":284,"state":"opened","source_branch":"agentflow/integration","target_branch":"master"},{"iid":200,"state":"merged","source_branch":"agentflow/integration","target_branch":"master","merged_at":"2026-08-01T10:00:00Z"}]}`)
	if len(items) != 2 || items[0].IID != 284 || items[1].IID != 200 {
		t.Fatalf("records=%+v", items)
	}
	if items[1].MergedAt == "" {
		t.Fatal("expected merged_at")
	}
	if parseGongfengTime(items[1].MergedAt).IsZero() {
		t.Fatal("merged_at should parse")
	}
}

func TestParseGongfengTimeNumericOffset(t *testing.T) {
	got := parseGongfengTime("2026-10-12T03:08:58+0000")
	want := time.Date(2026, 10, 12, 3, 8, 58, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("parsed %s, want %s", got, want)
	}
}

func TestPromoteMRCheckedAtUsesResolvedAtNotNow(t *testing.T) {
	now := time.Date(2026, 10, 13, 6, 59, 0, 0, time.UTC)
	mr := gongfengListedMR{ResolvedAt: "2026-10-12T03:08:59+0000", UpdatedAt: "2026-10-12T03:08:58+0000"}
	got, ok := promoteMRCheckedAt(mr, "merged", now)
	if !ok {
		t.Fatal("resolved_at should be a merge time")
	}
	want := time.Date(2026, 10, 12, 3, 8, 59, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("checked %s, want %s", got, want)
	}
	if _, ok := promoteMRCheckedAt(gongfengListedMR{}, "merged", now); ok {
		t.Fatal("merged promote MR without a timestamp must not use now")
	}
}

func TestWorkCommitOnDefaultBranchUsesSourceCommitRefs(t *testing.T) {
	sha := parseGongfengSourceCommit(`{"data":[{"iid":463,"source_commit":"cc798aff009ae069edf290d1a129333975991a54","target_branch":"agentflow/integration"}]}`, 463)
	if sha != "cc798aff009ae069edf290d1a129333975991a54" {
		t.Fatalf("sha=%q", sha)
	}
	names, ok := parseGongfengRefNames(`[{"type":"branch","name":"agentflow/integration"},{"type":"branch","name":"feature/fix-issue-77"},{"type":"tag","name":"v1"}]`)
	if !ok {
		t.Fatal("refs should parse")
	}
	if domain.BranchListed(names, "master") {
		t.Fatal("commit only on the baseline must not count as master")
	}
	if !domain.BranchListed(append(names, "master"), "master") {
		t.Fatal("master ref should count")
	}
}
