package runtime

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestOverlayEnvironmentEmptyOverlayKeepsEnv(t *testing.T) {
	env := map[string]string{"A": "1"}
	got := overlayEnvironment(env, nil)
	if !reflect.DeepEqual(got, env) {
		t.Fatalf("overlayEnvironment(env, nil) = %v, want %v", got, env)
	}
	if got := overlayEnvironment(nil, map[string]string{}); got != nil {
		t.Fatalf("empty overlay must keep nil env, got %v", got)
	}
}

func TestOverlayEnvironmentProfileWinsExceptPath(t *testing.T) {
	env := map[string]string{"PATH": "/profile/bin", "LANG": "profile", "ONLY_PROFILE": "p"}
	overlay := map[string]string{"PATH": "/overlay/bin:/usr/bin", "LANG": "overlay", "ONLY_OVERLAY": "o"}
	got := overlayEnvironment(env, overlay)
	want := map[string]string{"PATH": "/overlay/bin:/usr/bin", "LANG": "profile", "ONLY_PROFILE": "p", "ONLY_OVERLAY": "o"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("overlayEnvironment = %v, want %v", got, want)
	}
	if env["PATH"] != "/profile/bin" {
		t.Fatalf("input env must not be mutated, got %v", env)
	}
}

func TestOverlayEnvironmentEmptyOverlayPathKeepsProfilePath(t *testing.T) {
	got := overlayEnvironment(map[string]string{"PATH": "/profile/bin"}, map[string]string{"PATH": "", "X": "1"})
	if got["PATH"] != "/profile/bin" || got["X"] != "1" {
		t.Fatalf("overlayEnvironment = %v", got)
	}
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
