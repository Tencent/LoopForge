package runtime

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestNativeSandboxAvailabilityRequiresSuccessfulLaunch(t *testing.T) {
	var binary string
	switch goruntime.GOOS {
	case "darwin":
		binary = "sandbox-exec"
	case "linux":
		binary = "bwrap"
	default:
		t.Skip("native sandbox probe requires macOS or Linux")
	}
	t.Run("blocked runtime", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "probe.log")
		t.Setenv("PATH", dir)
		// The executable exists but fails to start the sandbox.
		script := "#!/bin/sh\necho probe >> '" + marker + "'\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, binary), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if NativeSandboxAvailable(domain.SandboxDriverNative) {
				t.Fatal("failed probe reported a usable sandbox")
			}
		}
		if got, err := os.ReadFile(marker); err != nil || string(got) != "probe\n" {
			t.Fatalf("probe was not executed once: %q, %v", got, err)
		}
	})
	t.Run("usable runtime", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("PATH", dir)
		if err := os.WriteFile(filepath.Join(dir, binary), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if !NativeSandboxAvailable(domain.SandboxDriverNative) {
			t.Fatal("successful probe did not report a usable sandbox")
		}
	})
}

func TestCreateDriverSandboxLocal(t *testing.T) {
	workspace := t.TempDir()
	for _, driver := range []string{"", "local", " local "} {
		sb, err := createDriverSandbox(context.Background(), "sandbox", "id", workspace, domain.SandboxProfile{Driver: driver, Environment: map[string]string{"K": "V"}}, SandboxRequirements{}, domain.DockerfileDefinition{})
		if err != nil {
			t.Fatalf("driver %q: %v", driver, err)
		}
		local, ok := sb.(*localSandbox)
		if !ok || local.id != "id" || local.workspace != workspace || local.environment["K"] != "V" {
			t.Fatalf("driver %q: got %#v", driver, sb)
		}
	}
}

func TestCreateDriverSandboxUnknownDriverNamesCaller(t *testing.T) {
	for kind, want := range map[string]string{
		"sandbox":        `unsupported sandbox driver "typo"`,
		"runner sandbox": `unsupported runner sandbox driver "typo"`,
	} {
		sb, err := createDriverSandbox(context.Background(), kind, "id", t.TempDir(), domain.SandboxProfile{Driver: "typo"}, SandboxRequirements{}, domain.DockerfileDefinition{})
		if sb != nil || err == nil || err.Error() != want {
			t.Fatalf("kind %q: got %v, %v; want error %q", kind, sb, err, want)
		}
	}
}

func TestOpenRemoteSandboxRejectsUnknownDriver(t *testing.T) {
	_, err := OpenRemoteSandbox(t.Context(), "typo", t.TempDir(), domain.SandboxProfile{ID: "t", Driver: "typo-driver"}, SandboxRequirements{}, SandboxResources{})
	if err == nil || !strings.Contains(err.Error(), `unsupported runner sandbox driver "typo-driver"`) {
		t.Fatalf("unknown driver should fail closed, got %v", err)
	}
}
