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
		{SourceID: "s1", Path: "guides/old.md", Title: "部署手册", Owner: "alice", Updated: ptrTime(old), Body: "# 部署手册\n\n[有效](new.md)\n"},
		{SourceID: "s1", Path: "guides/new.md", Title: "新手指南", Owner: "bob", Updated: ptrTime(recent), Body: "# 新手指南\n\n[死链](missing.md)\n[外链](https://example.com/x.md)\n![图](image.png)\n"},
		{SourceID: "s1", Path: "guides/no-owner.md", Title: "无负责人", Updated: ptrTime(recent), Body: "# 无负责人\n"},
		{SourceID: "s2", Path: "other/old.md", Title: "部署手册", Owner: "carol", Updated: ptrTime(recent), Body: "# 部署手册\n"},
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
	report := Analyze([]Document{{SourceID: "s", Path: "a.md", Title: "A", Owner: "alice", Body: "# A\n"}}, now, DefaultStaleDays)
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
		{SourceID: "s1", Path: "guides/a.md", Title: "A", Owner: "alice", Updated: ptrTime(recent), Body: "# A\n\n[同源](b.md)\n[跨源](../other/b.md)\n[根相对](/guides/b.md)\n"},
		{SourceID: "s1", Path: "guides/b.md", Title: "B", Owner: "alice", Updated: ptrTime(recent), Body: "# B\n"},
		{SourceID: "s2", Path: "other/b.md", Title: "Other B", Owner: "alice", Updated: ptrTime(recent), Body: "# Other B\n"},
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
		SourceID: "s", Path: "a.md", Title: "A", Owner: "alice", Updated: ptrTime(now),
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
