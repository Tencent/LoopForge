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
