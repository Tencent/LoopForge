package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCheckpointRejectsUnreadableTrackedFileIndex(t *testing.T) {
	for _, failure := range []string{"invalid-index", "git-failure", "partial-output", "output-overflow"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, output)
				}
			}
			git("init", "-q")
			file := filepath.Join(root, "node_modules", "vendored.js")
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("tracked asset"), 0600); err != nil {
				t.Fatal(err)
			}
			git("add", "-f", "node_modules/vendored.js")
			if failure == "invalid-index" {
				if err := os.WriteFile(filepath.Join(root, ".git", "index"), []byte("broken index"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				script := "#!/bin/sh\nexit 1\n"
				if failure == "partial-output" {
					script = "#!/bin/sh\nprintf 'node_modules/vendored.js\\0'\nexit 1\n"
				}
				if failure == "output-overflow" {
					script = "#!/bin/sh\nhead -c 16777217 /dev/zero\n"
				}
				bin := t.TempDir()
				if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			if archive, err := CreateWorkspaceCheckpoint(root); err == nil || archive != nil {
				t.Fatalf("unreadable index authorized an incomplete snapshot: archive=%d bytes, err=%v", len(archive), err)
			}
			if got, err := os.ReadFile(file); err != nil || string(got) != "tracked asset" {
				t.Fatalf("original asset changed: %q, %v", got, err)
			}
		})
	}
}
