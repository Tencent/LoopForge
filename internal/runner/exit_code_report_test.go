package runner

import (
	"errors"
	"os/exec"
	"testing"

	"agentflow/internal/domain"
)

// A failure before the executor spawned — a missing CLI, unusable credentials,
// an absent model — has no exit status. Reporting the zero value made every
// such error read "… (exit 0)" in the run log, which points the investigation
// at a process that never existed.
func TestReportedExitCodeSkipsPreflightFailures(t *testing.T) {
	preflight := execResult{status: domain.RunnerTaskFailed, errMsg: "Pi 需要指定模型"}
	if got := reportedExitCode(preflight); got != nil {
		t.Fatalf("pre-flight failure reported exit %d, want no exit code", *got)
	}
	crashed := execResult{status: domain.RunnerTaskFailed, exitCode: 127, ran: true}
	if got := reportedExitCode(crashed); got == nil || *got != 127 {
		t.Fatalf("executor that ran exit code = %v, want 127", got)
	}
	succeeded := execResult{status: domain.RunnerTaskSuccess, exitCode: 0, ran: true}
	if got := reportedExitCode(succeeded); got == nil || *got != 0 {
		t.Fatalf("successful task exit code = %v, want 0", got)
	}
}

func TestProcessRan(t *testing.T) {
	if !processRan(nil) {
		t.Fatal("a clean exit ran")
	}
	exitErr := exec.Command("false").Run()
	if !processRan(exitErr) {
		t.Fatal("a non-zero exit still ran")
	}
	if code := exitCode(exitErr); code != 1 {
		t.Fatalf("exitCode = %d, want 1", code)
	}
	if processRan(errors.New("exec: \"pi\": executable file not found in $PATH")) {
		t.Fatal("a spawn failure never ran")
	}
}
