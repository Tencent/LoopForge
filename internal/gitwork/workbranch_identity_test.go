package gitwork

import (
	"context"
	"slices"
	"testing"
)

func TestWithFallbackIdentity(t *testing.T) {
	ctx := context.Background()
	args := []string{"commit", "-q", "-m", "msg"}

	t.Run("missing identity gets LoopForge prefix", func(t *testing.T) {
		workspace := t.TempDir()
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		if _, err := C(ctx, workspace, "init", "-q"); err != nil {
			t.Fatal(err)
		}
		got := withFallbackIdentity(ctx, workspace, args)
		want := append([]string{"-c", "user.name=LoopForge", "-c", "user.email=loopforge@local"}, args...)
		if !slices.Equal(got, want) {
			t.Fatalf("withFallbackIdentity() = %q, want %q", got, want)
		}
	})

	t.Run("configured identity is left untouched", func(t *testing.T) {
		workspace := t.TempDir()
		if _, err := C(ctx, workspace, "init", "-q"); err != nil {
			t.Fatal(err)
		}
		if _, err := C(ctx, workspace, "config", "user.email", "dev@example.com"); err != nil {
			t.Fatal(err)
		}
		got := withFallbackIdentity(ctx, workspace, args)
		if !slices.Equal(got, args) {
			t.Fatalf("withFallbackIdentity() = %q, want %q", got, args)
		}
	})
}
