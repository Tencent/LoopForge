package runtime

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceCheckpointGitMetadataDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		content string
		want    string
	}{
		{"linked worktree", "repo/.git", "gitdir: /outside/private-metadata\n", "use a standalone checkout"},
		{"borrowed objects", "repo/.git/objects/info/alternates", "/outside/private-objects\n", "materialize borrowed objects"},
		{"relative alternates", ".git/objects/info/alternates", "../../../outside/objects\n", "materialize borrowed objects"},
		{"empty alternates", ".git/objects/info/alternates", "", ""},
		{"ordinary file", "objects/info/alternates", "ordinary content\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(tc.path))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			err := CreateWorkspaceCheckpointTo(root, io.Discard)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.path) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected actionable diagnostic for %s, got %v", tc.path, err)
			} else if strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "../../../outside") {
				t.Fatalf("diagnostic disclosed metadata target: %v", err)
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil || string(content) != tc.content {
				t.Fatalf("capture changed source metadata: %q, %v", content, readErr)
			}
		})
	}
}

func TestWorkspaceCheckpointRejectsSymlinkAlternateStore(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".git", "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/outside/private-objects", path); err != nil {
		t.Fatal(err)
	}
	err := CreateWorkspaceCheckpointTo(root, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "materialize borrowed objects") || strings.Contains(err.Error(), "private-objects") {
		t.Fatalf("expected safe alternate-store diagnostic, got %v", err)
	}
}

func TestWorkspaceCheckpointSharedCloneRequiresIndependentObjects(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	clone := filepath.Join(root, "clone")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Checkpoint Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Checkpoint Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return string(out)
	}
	git("init", "-q", source)
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("synthetic fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", source, "add", ".")
	git("-C", source, "commit", "-qm", "fixture")
	git("clone", "-q", "--shared", source, clone)
	if err := CreateWorkspaceCheckpointTo(clone, io.Discard); err == nil || !strings.Contains(err.Error(), ".git/objects/info/alternates") {
		t.Fatalf("shared clone must not produce a supposedly portable checkpoint: %v", err)
	}
	// Only the synthetic clone is changed. Copy borrowed objects before removing
	// the alternate, then prove the checkpoint survives loss of the source.
	git("-C", clone, "repack", "-a", "-d")
	if err := os.Remove(filepath.Join(clone, ".git", "objects", "info", "alternates")); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := CreateWorkspaceCheckpointTo(clone, &archive); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err := RestoreWorkspaceCheckpointFrom(restored, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	if got := git("-C", restored, "show", "HEAD:tracked.txt"); got != "synthetic fixture\n" {
		t.Fatalf("restored object content = %q", got)
	}
	git("-C", restored, "fsck", "--full")
}
