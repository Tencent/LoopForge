package governance

import (
	"testing"
	"time"
)

func ptrTime(t time.Time) *time.Time { return &t }

func TestAnalyzeFindsFourGovernanceProblems(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -400)
	recent := now.AddDate(0, 0, -2)
	docs := []Document{
		{SourceID: "s1", Path: "guides/old.md", Title: "部署手册", Owner: "alice", Updated: ptrTime(old), HasFrontmatter: true, Body: "# 部署手册\n\n[有效](new.md)\n"},
		{SourceID: "s1", Path: "guides/new.md", Title: "新手指南", Owner: "bob", Updated: ptrTime(recent), HasFrontmatter: true, Body: "# 新手指南\n\n[死链](missing.md)\n[外链](https://example.com/x.md)\n![图](image.png)\n"},
		{SourceID: "s1", Path: "guides/no-owner.md", Title: "无负责人", Updated: ptrTime(recent), HasFrontmatter: true, Body: "# 无负责人\n"},
		{SourceID: "s2", Path: "other/old.md", Title: "部署手册", Owner: "carol", Updated: ptrTime(recent), HasFrontmatter: true, Body: "# 部署手册\n"},
	}
	report := Analyze(docs, now, DefaultStaleDays)
	if report.Documents != 4 {
		t.Fatalf("documents = %d", report.Documents)
	}
	counts := map[string]int{}
	for _, issue := range report.Issues {
		counts[issue.Type]++
	}
	if counts[TypeStale] != 1 {
		t.Errorf("stale = %d, want 1: %#v", counts[TypeStale], report.Issues)
	}
	if counts[TypeMissingOwner] != 1 {
		t.Errorf("missing owner = %d, want 1: %#v", counts[TypeMissingOwner], report.Issues)
	}
	if counts[TypeDeadLink] != 1 {
		t.Errorf("dead link = %d, want 1: %#v", counts[TypeDeadLink], report.Issues)
	}
	if counts[TypeDuplicate] != 2 {
		t.Errorf("duplicate = %d, want 2: %#v", counts[TypeDuplicate], report.Issues)
	}
	for _, issue := range report.Issues {
		if issue.Type == TypeDeadLink && issue.Line != 3 {
			t.Errorf("dead link line = %d, want 3", issue.Line)
		}
	}
}

func TestAnalyzeTreatsMissingUpdatedAsStaleEvidence(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	report := Analyze([]Document{{SourceID: "s", Path: "a.md", Title: "A", Owner: "alice", HasFrontmatter: true, Body: "# A\n"}}, now, DefaultStaleDays)
	found := false
	for _, issue := range report.Issues {
		if issue.Type == TypeStale {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing updated should produce a stale issue: %#v", report.Issues)
	}
}

func TestAnalyzeResolvesRelativeLinksWithinSourceOnly(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	recent := now
	docs := []Document{
		{SourceID: "s1", Path: "guides/a.md", Title: "A", Owner: "alice", Updated: ptrTime(recent), HasFrontmatter: true, Body: "# A\n\n[同源](b.md)\n[跨源](../other/b.md)\n[根相对](/guides/b.md)\n"},
		{SourceID: "s1", Path: "guides/b.md", Title: "B", Owner: "alice", Updated: ptrTime(recent), HasFrontmatter: true, Body: "# B\n"},
		{SourceID: "s2", Path: "other/b.md", Title: "Other B", Owner: "alice", Updated: ptrTime(recent), HasFrontmatter: true, Body: "# Other B\n"},
	}
	report := Analyze(docs, now, DefaultStaleDays)
	dead := 0
	for _, issue := range report.Issues {
		if issue.Type == TypeDeadLink {
			dead++
		}
	}
	if dead != 1 {
		t.Fatalf("dead links = %d, want only the cross-source target: %#v", dead, report.Issues)
	}
}

func TestAnalyzeFlagsPathTraversalAndSkipsCodeFences(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	docs := []Document{{
		SourceID: "s", Path: "a.md", Title: "A", Owner: "alice", Updated: ptrTime(now), HasFrontmatter: true,
		Body: "# A\n\n```\n[不是链接](ignored.md)\n```\n[越界](../../secret.md)\n",
	}}
	report := Analyze(docs, now, DefaultStaleDays)
	count := 0
	for _, issue := range report.Issues {
		if issue.Type == TypeDeadLink {
			count++
			if issue.Line != 6 {
				t.Errorf("line = %d, want 6", issue.Line)
			}
		}
	}
	if count != 1 {
		t.Fatalf("dead links = %d, want 1: %#v", count, report.Issues)
	}
}

func TestAnalyzeIsBounded(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	docs := make([]Document, MaxDocuments+1)
	for i := range docs {
		docs[i] = Document{SourceID: "s", Path: "a" + string(rune('a'+i%26)) + ".md", Title: "A", Owner: "alice", Updated: ptrTime(now), Body: "# A\n"}
	}
	report := Analyze(docs, now, DefaultStaleDays)
	if !report.Truncated {
		t.Fatal("document budget exhaustion must be marked truncated")
	}
	if report.Documents > MaxDocuments {
		t.Fatalf("documents = %d", report.Documents)
	}
}

func TestAnalyzeSkipsOwnerAndStaleWithoutFrontmatter(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	docs := []Document{
		{SourceID: "s", Path: "plain.md", Title: "部署手册", Body: "# 部署手册\n\n[缺失](missing.md)\n"},
		{SourceID: "s", Path: "headed.md", Title: "部署手册", Owner: "alice", Updated: ptrTime(now), HasFrontmatter: true, Body: "# 部署手册\n"},
	}
	report := Analyze(docs, now, DefaultStaleDays)
	dead, dup := 0, 0
	for _, issue := range report.Issues {
		if issue.Path == "plain.md" && (issue.Type == TypeMissingOwner || issue.Type == TypeStale) {
			t.Fatalf("plain document must not be flagged for metadata: %#v", issue)
		}
		switch issue.Type {
		case TypeDeadLink:
			dead++
		case TypeDuplicate:
			dup++
		}
	}
	if dead != 1 || dup != 2 {
		t.Fatalf("dead=%d dup=%d, want 1 and 2: %#v", dead, dup, report.Issues)
	}
}

func TestAnalyzeHonorsSourceMetadataSwitch(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	report := Analyze([]Document{{
		SourceID: "s", Path: "a.md", Title: "A", HasFrontmatter: true, SkipMetadata: true,
		Body: "# A\n\n[缺失](missing.md)\n",
	}}, now, DefaultStaleDays)
	if len(report.Issues) != 1 || report.Issues[0].Type != TypeDeadLink {
		t.Fatalf("disabled metadata checks should leave only the dead link: %#v", report.Issues)
	}
}

func TestHasFrontmatterRequiresClosingMarker(t *testing.T) {
	if !HasFrontmatter("---\nowner: alice\n---\n# A\n") {
		t.Fatal("closed frontmatter should count")
	}
	if HasFrontmatter("# A\n") || HasFrontmatter("---\nowner: alice\n# A\n") {
		t.Fatal("prose and an unclosed marker are not frontmatter")
	}
}
