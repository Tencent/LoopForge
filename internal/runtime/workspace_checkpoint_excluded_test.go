package runtime

import "testing"

func TestCheckpointExclusionNames(t *testing.T) {
	cases := []struct {
		path             string
		capture, restore bool
	}{
		{"src/main.go", false, false},
		{".agentflow", true, true},
		{"a/node_modules/x.js", true, true},
		{"pkg/.cache/blob", true, true},
		{"py/__pycache__/m.pyc", true, true},
		{".zvec-grep/index", true, false},
		{"deep/.zvec-grep", true, false},
		{".cache2/file", false, false},
		{"my_node_modules/a", false, false},
		{"docs/.agentflow.md", false, false},
	}
	for _, tc := range cases {
		if got := checkpointExcluded(tc.path); got != tc.capture {
			t.Errorf("checkpointExcluded(%q) = %v, want %v", tc.path, got, tc.capture)
		}
		if got := checkpointRestoreExcluded(tc.path); got != tc.restore {
			t.Errorf("checkpointRestoreExcluded(%q) = %v, want %v", tc.path, got, tc.restore)
		}
	}
}

func TestCheckpointExcludedNamesDoNotAliasRestoreNames(t *testing.T) {
	if len(checkpointRestoreExcludedNames) != 4 || len(checkpointExcludedNames) != 5 {
		t.Fatalf("unexpected exclusion lists: restore=%v capture=%v", checkpointRestoreExcludedNames, checkpointExcludedNames)
	}
}
