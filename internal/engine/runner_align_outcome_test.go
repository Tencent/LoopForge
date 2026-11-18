package engine

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestRunnerAlignOutcomeCleanMerge(t *testing.T) {
	for _, result := range []string{`{}`, `{"head":"abc123"}`} {
		final := domain.RunnerTask{Status: domain.RunnerTaskSuccess, Result: result}
		if err := runnerAlignOutcome(final, "integrate", "main"); err != nil {
			t.Fatalf("result %s: err = %v, want nil", result, err)
		}
	}
}

func TestRunnerAlignOutcomeRunnerFailure(t *testing.T) {
	cases := []struct {
		name string
		task domain.RunnerTask
		want string
	}{
		{"runner error", domain.RunnerTask{Status: domain.RunnerTaskFailed, Error: "  git fetch failed \n"}, "git fetch failed"},
		{"failed fallback", domain.RunnerTask{Status: domain.RunnerTaskFailed}, "align_baseline failed on runner"},
		{"canceled fallback", domain.RunnerTask{Status: domain.RunnerTaskCanceled, Error: "   "}, "align_baseline canceled on runner"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runnerAlignOutcome(tc.task, "integrate", "main")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			var conflict *AlignConflictError
			if errors.As(err, &conflict) {
				t.Fatalf("runner failure must not be an AlignConflictError")
			}
		})
	}
}

func TestRunnerAlignOutcomeInvalidResult(t *testing.T) {
	final := domain.RunnerTask{Status: domain.RunnerTaskSuccess, Result: "not json"}
	err := runnerAlignOutcome(final, "integrate", "main")
	if err == nil || !strings.HasPrefix(err.Error(), "decode align result: ") {
		t.Fatalf("err = %v, want decode align result error", err)
	}
}

func TestRunnerAlignOutcomeConflict(t *testing.T) {
	final := domain.RunnerTask{
		Status: domain.RunnerTaskSuccess,
		Result: `{"head":"abc","conflicts":["a.go","b.go"],"blocks":[{"file":"a.go","ours":"x","theirs":"y"}]}`,
	}
	err := runnerAlignOutcome(final, "agentflow/integrate", "main")
	var conflict *AlignConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *AlignConflictError", err)
	}
	want := &AlignConflictError{
		Integrate: "agentflow/integrate",
		Default:   "main",
		Files:     []string{"a.go", "b.go"},
		Blocks:    []domain.ConflictBlock{{File: "a.go", Ours: "x", Theirs: "y"}},
	}
	if !reflect.DeepEqual(conflict, want) {
		t.Fatalf("conflict = %+v, want %+v", conflict, want)
	}
}
