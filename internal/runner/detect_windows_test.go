package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWindowsDetectRuntimesUsesNativeCLI(t *testing.T) {
	shims := t.TempDir()
	for _, name := range []string{"codex.CMD", "codex.bat", "codex.exe.cmd"} {
		if err := os.WriteFile(filepath.Join(shims, name), []byte("@echo batch shim"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	native := t.TempDir()
	binary := filepath.Join(native, "codex.exe")
	source := filepath.Join(native, "main.go")
	program := `package main
import ("fmt"; "os")
func main() {
    if len(os.Args) != 2 || os.Args[1] != "--version" { os.Exit(1) }
    fmt.Println("codex native test 1.0.0")
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-o", binary, source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native CLI fixture: %v\n%s", err, output)
	}

	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("PATHEXT", ".CMD;.BAT;.EXE")
	t.Setenv("PATH", shims)
	for _, candidate := range []string{"codex", filepath.Join(shims, "codex.CMD"), filepath.Join(shims, "codex.bat")} {
		runtimes := DetectRuntimes(context.Background(), map[string]string{"codex": candidate})
		if path, ok := providerBinaryPath(runtimes, "codex"); ok {
			t.Fatalf("batch shim %q reported as runtime %q", candidate, path)
		}
		if _, err := resolveCommandPath([]string{candidate, "exec"}); err == nil {
			t.Fatalf("batch shim %q accepted for a host task", candidate)
		}
	}

	t.Setenv("PATH", strings.Join([]string{shims, native}, string(os.PathListSeparator)))
	runtimes := DetectRuntimes(context.Background(), nil)
	for _, rt := range runtimes {
		if rt.Provider != "codex" {
			continue
		}
		if rt.BinaryPath != binary || rt.Version != "codex native test 1.0.0" {
			t.Fatalf("native runtime was not probed: %+v", rt)
		}
		argv, err := resolveCommandPath([]string{"codex", "exec", "prompt"})
		if err != nil || len(argv) != 3 || argv[0] != binary || argv[1] != "exec" || argv[2] != "prompt" {
			t.Fatalf("host task did not resolve the probed executable: %v %v", argv, err)
		}
		return
	}
	t.Fatal("native executable behind the batch shims was not reported")
}
