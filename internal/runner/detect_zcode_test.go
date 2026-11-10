package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
)

func zcodeRuntime(t *testing.T, script string) (domain.RunnerRuntime, bool) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "zcode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rt := range DetectRuntimes(context.Background(), nil) {
		if rt.Provider == "zcode" {
			return rt, true
		}
	}
	return domain.RunnerRuntime{}, false
}

func TestDetectZCodeProbesWithItsOwnFlags(t *testing.T) {
	rt, ok := zcodeRuntime(t, "#!/bin/sh\n[ \"$*\" = \"--output-format stream-json --mode yolo --version\" ] && echo 0.17.1 && exit 0\nexit 1\n")
	if !ok || rt.Version != "0.17.1" || rt.Status != domain.RuntimeStatusOnline {
		t.Fatalf("official zcode: %+v %v", rt, ok)
	}
	// A same-named CLI that rejects the flags is not reported.
	if rt, ok := zcodeRuntime(t, "#!/bin/sh\n[ \"$*\" = \"--version\" ] && echo 1.0.0 && exit 0\necho unknown option >&2\nexit 1\n"); ok {
		t.Fatalf("incompatible zcode reported: %+v", rt)
	}
}
