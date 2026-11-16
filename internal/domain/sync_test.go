package domain

import (
	"reflect"
	"testing"
)

func TestNormalizeIntegrateBranch(t *testing.T) {
	if got := NormalizeIntegrateBranch("  agentflow/integration  "); got != "agentflow/integration" {
		t.Fatalf("trim = %q", got)
	}
	if got := NormalizeIntegrateBranch(""); got != "" {
		t.Fatalf("empty must stay empty, got %q", got)
	}
}

func TestNormalizeScheduleIssueStatuses(t *testing.T) {
	got := NormalizeScheduleIssueStatuses([]string{" TODO ", "done", "backlog", "todo", "unknown", "blocked"})
	want := []string{"backlog", "todo", "blocked"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalize = %v, want %v", got, want)
	}
}

func TestScheduleJobTriggerIssueStatuses(t *testing.T) {
	empty := ScheduleJob{WorkflowID: "wf"}
	if got := empty.TriggerIssueStatuses(); !reflect.DeepEqual(got, ScheduleIssueStatusOptions) {
		t.Fatalf("empty default = %v", got)
	}
	if !empty.MatchesIssueStatus("todo") || !empty.MatchesIssueStatus("") || !empty.MatchesIssueStatus("in_progress") {
		t.Fatal("legacy empty job should match all open statuses")
	}
	custom := ScheduleJob{WorkflowID: "wf", IssueStatuses: []string{"in_progress", "blocked"}}
	if !custom.MatchesIssueStatus("blocked") || custom.MatchesIssueStatus("todo") {
		t.Fatal("custom statuses were not honored")
	}
	ai := ScheduleJob{SkipDispatch: true, IssueStatuses: []string{"todo"}}
	if len(ai.TriggerIssueStatuses()) != 0 || ai.MatchesIssueStatus("todo") {
		t.Fatal("AI jobs must not scan issue statuses")
	}
}

func TestScheduleJobMatchesIssueStatusNormalizesInput(t *testing.T) {
	custom := ScheduleJob{WorkflowID: "wf", IssueStatuses: []string{" Backlog ", "IN_REVIEW"}}
	cases := map[string]bool{
		"backlog":      true,
		"  BACKLOG\t":  true,
		"":             true, // empty status is treated as backlog
		"   ":          true,
		"In_Review":    true,
		"todo":         false,
		"done":         false,
		"in_progress ": false,
	}
	for status, want := range cases {
		if got := custom.MatchesIssueStatus(status); got != want {
			t.Errorf("MatchesIssueStatus(%q) = %v, want %v", status, got, want)
		}
	}
	noBacklog := ScheduleJob{WorkflowID: "wf", IssueStatuses: []string{"todo"}}
	if noBacklog.MatchesIssueStatus("") {
		t.Error("empty status must only match jobs that scan backlog")
	}
}

func TestNormalizeScheduleSource(t *testing.T) {
	for _, raw := range []string{"", " all ", "BOTH"} {
		got, err := NormalizeScheduleSource(raw)
		if err != nil || got != "" {
			t.Fatalf("NormalizeScheduleSource(%q)=%q %v", raw, got, err)
		}
	}
	for _, raw := range []string{"gongfeng", " TAPD "} {
		got, err := NormalizeScheduleSource(raw)
		if err != nil {
			t.Fatal(err)
		}
		want := "gongfeng"
		if raw != "gongfeng" {
			want = "tapd"
		}
		if got != want {
			t.Fatalf("NormalizeScheduleSource(%q)=%q", raw, got)
		}
	}
	if _, err := NormalizeScheduleSource("local"); err == nil {
		t.Fatal("unknown source must be rejected")
	}
	both := ScheduleJob{}
	if !both.MatchesIssueSource("gongfeng") || !both.MatchesIssueSource("tapd") || !both.MatchesIssueSource("local") {
		t.Fatal("empty source must match every issue")
	}
	only := ScheduleJob{Source: "gongfeng"}
	if !only.MatchesIssueSource("gongfeng") || only.MatchesIssueSource("tapd") || only.MatchesIssueSource("local") {
		t.Fatal("gongfeng source must exclude TAPD and local issues")
	}
}
