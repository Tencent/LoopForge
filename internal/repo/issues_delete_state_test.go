package repo

import "testing"

func TestIssueDeletionStateAddTask(t *testing.T) {
	for _, tc := range []struct {
		status     string
		successful bool
		blocked    bool
	}{
		{"completed", true, false},
		{"succeeded", true, false},
		{"failed", false, false},
		{"cancelled", false, false},
		{"running", false, true},
		{"queued", false, true},
	} {
		t.Run(tc.status, func(t *testing.T) {
			var s IssueDeletionState
			s.addTask("task-1", tc.status)
			if !s.hasHistory || s.successful != tc.successful || (s.Block != nil) != tc.blocked {
				t.Fatalf("state = %+v, want successful=%v blocked=%v", s, tc.successful, tc.blocked)
			}
			if tc.blocked && (s.Block.Code != "active" || s.Block.TaskID != "task-1" || s.Block.RunID != "") {
				t.Fatalf("block = %+v", s.Block)
			}
			if len(s.runIDs) != 0 {
				t.Fatalf("addTask must not record run ids: %v", s.runIDs)
			}
		})
	}
}

func TestIssueDeletionStateAddTaskKeepsFirstBlock(t *testing.T) {
	s := IssueDeletionState{Block: &IssueDeletionBlock{Code: "clarifying"}}
	s.addTask("task-1", "running")
	if s.Block.Code != "clarifying" {
		t.Fatalf("block overwritten: %+v", s.Block)
	}
}

func TestIssueDeletionStateAddRun(t *testing.T) {
	for _, tc := range []struct {
		name       string
		run        issueDeletionRun
		successful bool
		blocked    bool
	}{
		{"succeeded", issueDeletionRun{ID: "r", Status: "succeeded"}, true, false},
		{"failed", issueDeletionRun{ID: "r", Status: "failed"}, false, false},
		{"running", issueDeletionRun{ID: "r", Status: "running"}, false, true},
		{"terminal with active nodes", issueDeletionRun{ID: "r", Status: "failed", ActiveNodes: true}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s IssueDeletionState
			s.addRun(tc.run)
			if !s.hasHistory || s.successful != tc.successful || (s.Block != nil) != tc.blocked {
				t.Fatalf("state = %+v, want successful=%v blocked=%v", s, tc.successful, tc.blocked)
			}
			if len(s.runIDs) != 1 || s.runIDs[0] != "r" {
				t.Fatalf("runIDs = %v", s.runIDs)
			}
			if tc.blocked && (s.Block.Code != "active" || s.Block.RunID != "r") {
				t.Fatalf("block = %+v", s.Block)
			}
		})
	}
}

func TestIssueDeletionStateAddRunBlockPriority(t *testing.T) {
	var s IssueDeletionState
	s.addTask("task-1", "running")
	s.addRun(issueDeletionRun{ID: "run-1", Status: "running"})
	if s.Block.RunID != "run-1" || s.Block.TaskID != "" {
		t.Fatalf("task block should upgrade to run block: %+v", s.Block)
	}
	s.addRun(issueDeletionRun{ID: "run-2", Status: "running"})
	if s.Block.RunID != "run-1" {
		t.Fatalf("existing run block overwritten: %+v", s.Block)
	}
	if len(s.runIDs) != 2 {
		t.Fatalf("runIDs = %v", s.runIDs)
	}

	clarifying := IssueDeletionState{Block: &IssueDeletionBlock{Code: "clarifying"}}
	clarifying.addRun(issueDeletionRun{ID: "run-1", Status: "running"})
	if clarifying.Block.Code != "clarifying" {
		t.Fatalf("clarifying block overwritten: %+v", clarifying.Block)
	}

	succeeded := IssueDeletionState{successful: true}
	succeeded.addRun(issueDeletionRun{ID: "run-1", Status: "failed"})
	if !succeeded.successful {
		t.Fatal("earlier success must be kept")
	}
}
