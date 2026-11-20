package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNPMCLIInstallArgs(t *testing.T) {
	base := []string{"install", "--prefix", "/stage", "--no-audit", "--no-fund", "--omit=dev", "--save-exact"}
	strict := npmCLIInstallArgs("/stage", onDemandCLI{Package: "opencode-ai@1.0.0"})
	if want := append(append([]string{}, base...), "--engine-strict", "opencode-ai@1.0.0"); !reflect.DeepEqual(strict, want) {
		t.Fatalf("strict args = %v, want %v", strict, want)
	}
	relaxed := npmCLIInstallArgs("/stage", onDemandCLI{Package: "dsh@2", RelaxEngines: true})
	if want := append(append([]string{}, base...), "--engine-strict=false", "dsh@2"); !reflect.DeepEqual(relaxed, want) {
		t.Fatalf("relaxed args = %v, want %v", relaxed, want)
	}
}

func TestActivateNPMStageReplacesCurrent(t *testing.T) {
	parent := t.TempDir()
	old := filepath.Join(parent, "current", "stale.txt")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(parent, ".install-x")
	bin := filepath.Join(stage, "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := activateNPMStage(parent, stage, "tool")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(parent, "current", "node_modules", ".bin", "tool"); path != want || !fileIsExecutable(path) {
		t.Fatalf("path = %q, want executable %q", path, want)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("stale current content should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("stage should be renamed away, stat err = %v", err)
	}
}

func TestActivateNPMStageMissingBinary(t *testing.T) {
	parent := t.TempDir()
	stage := filepath.Join(parent, ".install-y")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := activateNPMStage(parent, stage, "tool")
	if err == nil || !strings.Contains(err.Error(), "安装后没有找到可执行文件 tool") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "current")); !os.IsNotExist(err) {
		t.Fatalf("current must not be created on failure, stat err = %v", err)
	}
}
