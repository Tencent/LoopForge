package domain

import (
	"testing"
	"time"
)

func TestRunHumanEditable(t *testing.T) {
	cases := []struct {
		run  Run
		want bool
	}{
		{Run{Status: "running"}, true},
		{Run{Status: "waiting"}, true},
		{Run{Status: "succeeded"}, true},
		{Run{Status: "cancelled", CancelRequested: true}, true},
		{Run{Status: "running", CancelRequested: true}, false},
		{Run{Status: "queued"}, false},
		{Run{Status: "running", WorkspaceCleanedAt: time.Now()}, false},
	}
	for _, c := range cases {
		if got := c.run.HumanEditable(); got != c.want {
			t.Errorf("HumanEditable(%+v) = %v want %v", c.run, got, c.want)
		}
	}
}

func TestRunHasActiveNode(t *testing.T) {
	cases := []struct {
		name  string
		nodes map[string]*NodeRun
		want  bool
	}{
		{"no nodes", nil, false},
		{"nil node", map[string]*NodeRun{"a": nil}, false},
		{"running", map[string]*NodeRun{"a": {Status: "succeeded"}, "b": {Status: "running"}}, true},
		{"pending input", map[string]*NodeRun{"a": nil, "b": {Status: "pending_input"}}, true},
		{"waiting approval", map[string]*NodeRun{"a": {Status: "waiting_approval"}}, false},
		{"finished", map[string]*NodeRun{"a": {Status: "succeeded"}, "b": {Status: "failed"}, "c": {Status: "cancelled"}}, false},
	}
	for _, c := range cases {
		if got := (Run{Nodes: c.nodes}).HasActiveNode(); got != c.want {
			t.Errorf("%s: HasActiveNode() = %v want %v", c.name, got, c.want)
		}
	}
}
