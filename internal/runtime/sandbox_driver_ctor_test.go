package runtime

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

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
