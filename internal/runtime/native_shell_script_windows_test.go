package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func windowsTestShell(t *testing.T) string {
	t.Helper()
	for _, path := range []string{`C:\Program Files\Git\bin\sh.exe`, `C:\Program Files\Git\usr\bin\sh.exe`} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if path, err := exec.LookPath("sh"); err == nil {
		return path
	}
	t.Skip("Git/MSYS shell is not installed")
	return ""
}

func TestWindowsHostShellPreservesLongSourceAndInput(t *testing.T) {
	temporary := filepath.Join(t.TempDir(), "临时 shell '目录")
	if err := os.MkdirAll(temporary, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMP", temporary)
	t.Setenv("TEMP", temporary)
	workspace := t.TempDir()
	script := ": \"" + strings.Repeat("x", 25000) + "\"\nIFS= read -r line\nprintf '%s|%s|%s|%s' \"$line\" \"$0\" \"$1\" \"$BOUND\""
	command := WithCommandEnvironment(CommandSpec{Provider: "script", Argv: []string{windowsTestShell(t), "-lc", script, "original-zero", "quoted \"中文\""}, Stdin: strings.NewReader("stdin '中文'\n")}, map[string]string{"BOUND": "bound 中文"})
	out, err := (&localSandbox{workspace: workspace}).Exec(context.Background(), command, nil)
	if err != nil {
		t.Fatalf("long shell: %v\n%s", err, out)
	}
	if strings.TrimSuffix(out, "\n") != "stdin '中文'|original-zero|quoted \"中文\"|bound 中文" {
		t.Fatalf("shell altered source, stdin, arguments or environment: %q", out)
	}
	entries, err := os.ReadDir(temporary)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private script was not cleaned: entries=%d err=%v", len(entries), err)
	}
	if command.Argv[2] != script {
		t.Fatal("preparing command changed its original argv")
	}
}

func TestWindowsHostShellLeavesSmallCommandsAndProviders(t *testing.T) {
	for _, argv := range [][]string{{windowsTestShell(t), "-lc", "printf ok"}, {"codex", "exec", strings.Repeat("x", 25000)}} {
		command := CommandSpec{Argv: argv}
		got, cleanup, err := PrepareHostShellCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		if !reflect.DeepEqual(got.Argv, argv) {
			t.Fatal("small shell or provider invocation was changed")
		}
	}
}

func TestWindowsHostShellRejectsWSLAliasesAndPrefersGit(t *testing.T) {
	root := t.TempDir()
	aliases := filepath.Join(root, "WindowsApps")
	gitBin := filepath.Join(root, "Git", "bin")
	for _, directory := range []string{aliases, gitBin} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "sh.exe"), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", aliases)
	t.Setenv("ProgramFiles", filepath.Join(root, "missing"))
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("LOCALAPPDATA", "")
	if _, err := findWindowsHostShell("sh"); err == nil {
		t.Fatal("WSL alias was selected as a host shell")
	}
	t.Setenv("ProgramFiles", root)
	command := CommandSpec{Argv: []string{"sh", "-lc", "printf ok"}}
	prepared, cleanup, err := PrepareHostShellCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if prepared.Argv[0] != filepath.Join(gitBin, "sh.exe") || command.Argv[0] != "sh" {
		t.Fatalf("host shell did not select Git without mutating argv: %v", prepared.Argv)
	}
}
