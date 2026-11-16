package runtime

import (
	"os/exec"
	goruntime "runtime"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestCreateProcessSandboxRejectsOtherOS(t *testing.T) {
	_, err := createProcessSandbox("demo", "not-"+goruntime.GOOS, "DemoOS", "sh", "x", t.TempDir(), domain.SandboxProfile{}, SandboxRequirements{})
	if err == nil || err.Error() != "demo sandbox requires DemoOS" {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateProcessSandboxMissingBinary(t *testing.T) {
	_, err := createProcessSandbox("demo", goruntime.GOOS, "DemoOS", "agentflow-no-such-binary", "x", t.TempDir(), domain.SandboxProfile{}, SandboxRequirements{})
	if err == nil || !strings.HasPrefix(err.Error(), "demo sandbox unavailable: ") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateProcessSandboxBuildsSandbox(t *testing.T) {
	want, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	workspace := t.TempDir()
	profile := domain.SandboxProfile{Driver: "demo"}
	sb, err := createProcessSandbox("demo", goruntime.GOOS, "DemoOS", "sh", "sb-1", workspace, profile, SandboxRequirements{})
	if err != nil {
		t.Fatal(err)
	}
	ps, ok := sb.(*processSandbox)
	if !ok {
		t.Fatalf("type = %T", sb)
	}
	if ps.ID() != "sb-1" || ps.Workspace() != workspace || ps.Driver() != "demo" || ps.binary != want || ps.profile.Driver != "demo" {
		t.Fatalf("sandbox = %+v", ps)
	}
}

func TestCreateSeatbeltAndBwrapKeepErrorText(t *testing.T) {
	if goruntime.GOOS != "darwin" {
		if _, err := createSeatbelt("x", t.TempDir(), domain.SandboxProfile{}, SandboxRequirements{}); err == nil || err.Error() != "seatbelt sandbox requires macOS" {
			t.Fatalf("seatbelt err = %v", err)
		}
	}
	if goruntime.GOOS != "linux" {
		if _, err := createBwrap("x", t.TempDir(), domain.SandboxProfile{}, SandboxRequirements{}); err == nil || err.Error() != "bwrap sandbox requires Linux" {
			t.Fatalf("bwrap err = %v", err)
		}
	}
}
