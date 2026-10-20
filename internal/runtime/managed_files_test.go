package runtime

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"agentflow/internal/domain"
)

func putSecurityFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func checkSecurityFixture(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != want {
		t.Fatalf("%s: got %q, %v; want %q", path, raw, err, want)
	}
}

func TestManagedFileRejectsEscapesAndSymlinks(t *testing.T) {
	for _, kind := range []string{"outside", "parent", "file"} {
		t.Run(kind, func(t *testing.T) {
			workspace, outside := t.TempDir(), t.TempDir()
			victim := filepath.Join(outside, "keep")
			putSecurityFixture(t, victim, "keep")
			target := victim
			switch kind {
			case "parent":
				if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(workspace, "link", "keep")
			case "file":
				target = filepath.Join(workspace, "link")
				if err := os.Symlink(victim, target); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeManagedFile(workspace, target, []byte("bad"), 0700); err == nil {
				t.Fatal("unsafe write accepted")
			}
			if _, err := readWorkspaceFile(workspace, target); err == nil {
				t.Fatal("unsafe read accepted")
			}
			checkSecurityFixture(t, victim, "keep")
		})
	}
}

func TestManagedFileReplacesHardlinkAndRestrictsPermissions(t *testing.T) {
	workspace := t.TempDir()
	victim := filepath.Join(t.TempDir(), "keep")
	putSecurityFixture(t, victim, "keep")
	target := filepath.Join(workspace, "managed")
	if err := os.Link(victim, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0777); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedFile(workspace, target, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	checkSecurityFixture(t, victim, "keep")
	checkSecurityFixture(t, target, "new")
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v, %v", info, err)
	}
}

func TestGitExcludesRejectSymlinks(t *testing.T) {
	for _, rel := range []string{".git", ".git/info", ".git/info/exclude"} {
		t.Run(rel, func(t *testing.T) {
			workspace, outside := t.TempDir(), t.TempDir()
			target := filepath.Join(workspace, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			victim := outside
			if rel == ".git/info/exclude" {
				victim = filepath.Join(outside, "keep")
				putSecurityFixture(t, victim, "keep")
			}
			if err := os.Symlink(victim, target); err != nil {
				t.Fatal(err)
			}
			if err := EnsureAgentFlowGitExcludes(workspace); err == nil {
				t.Fatal("symlink accepted")
			}
			if rel == ".git/info/exclude" {
				checkSecurityFixture(t, victim, "keep")
			}
			if _, err := os.Stat(filepath.Join(outside, "info")); !os.IsNotExist(err) {
				t.Fatalf("outside directory modified: %v", err)
			}
		})
	}
}

func TestMCPFilesRejectUnsafePathsBeforeDeleting(t *testing.T) {
	for _, name := range []string{"../escape", "/escape", "sub/../../escape"} {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			old := filepath.Join(workspace, ".agentflow", "mcp", "server", "old")
			putSecurityFixture(t, old, "old")
			server := domain.MCPServerDefinition{ID: "server", FileRefs: []domain.MCPFileRef{{Path: name}}}
			if err := materializeMCPFiles(workspace, server, "server", map[string][]byte{name: []byte("bad")}, "hash"); err == nil {
				t.Fatal("unsafe upload accepted")
			}
			checkSecurityFixture(t, old, "old")
		})
	}
}

func TestMCPFilesRejectSymlinkedTreesAndMarkers(t *testing.T) {
	for _, rel := range []string{".agentflow", ".agentflow/mcp", ".agentflow/mcp/server", ".agentflow/runtime/mcp-sources", ".agentflow/runtime/mcp-sources/server.sha256"} {
		t.Run(rel, func(t *testing.T) {
			workspace, outside := t.TempDir(), t.TempDir()
			target := filepath.Join(workspace, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			victim := outside
			if filepath.Ext(rel) == ".sha256" {
				victim = filepath.Join(outside, "marker")
				putSecurityFixture(t, victim, "hash")
			}
			keep := filepath.Join(outside, "keep")
			putSecurityFixture(t, keep, "keep")
			if err := os.Symlink(victim, target); err != nil {
				t.Fatal(err)
			}
			server := domain.MCPServerDefinition{ID: "server", FileRefs: []domain.MCPFileRef{{Path: "sub/main.py"}}}
			if err := materializeMCPFiles(workspace, server, "server", map[string][]byte{"sub/main.py": []byte("bad")}, "hash"); err == nil {
				t.Fatal("symlink accepted")
			}
			checkSecurityFixture(t, keep, "keep")
			if _, err := os.Stat(filepath.Join(outside, "sub", "main.py")); !os.IsNotExist(err) {
				t.Fatalf("outside file written: %v", err)
			}
		})
	}
}

func TestPrepareMCPCallRejectsSymlinks(t *testing.T) {
	for _, rel := range []string{".agentflow", ".agentflow/runtime", ".agentflow/runtime/mcp-servers.json", ".agentflow/runtime/bin/agentflow-helper", ".agentflow/runtime/bin/agentflow"} {
		t.Run(rel, func(t *testing.T) {
			workspace, outside := t.TempDir(), t.TempDir()
			target := filepath.Join(workspace, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			victim := outside
			if rel != ".agentflow" && rel != ".agentflow/runtime" {
				victim = filepath.Join(outside, "keep")
				putSecurityFixture(t, victim, "keep")
			}
			if err := os.Symlink(victim, target); err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareMCPCall(nil, workspace, nil); err == nil {
				t.Fatal("symlink accepted")
			}
			if victim != outside {
				checkSecurityFixture(t, victim, "keep")
			}
		})
	}
}

func TestGitAskPassRejectsSymlink(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, ".agentflow", "runtime", "git-askpass")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "keep")
	putSecurityFixture(t, victim, "keep")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}
	if err := writeGitAskPass(workspace, target); err == nil {
		t.Fatal("symlink accepted")
	}
	checkSecurityFixture(t, victim, "keep")
}

func TestWorkspacePreparationDoesNotReadOutsideState(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	putSecurityFixture(t, filepath.Join(outside, "workspace-preparation", "state.json"), `{"status":"ready"}`)
	if err := os.Symlink(outside, filepath.Join(workspace, ".agentflow")); err != nil {
		t.Fatal(err)
	}
	if state := ReadWorkspacePreparationStatus(workspace); state.Status != "unprepared" {
		t.Fatalf("read outside state: %#v", state)
	}
}

func TestEnvironmentInstallRejectsSymlinkStamp(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, ".agentflow", "environment", "env")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "keep")
	putSecurityFixture(t, victim, "keep")
	if err := os.Symlink(victim, filepath.Join(root, installStampName)); err != nil {
		t.Fatal(err)
	}
	sb := &provisionSandbox{workspace: workspace}
	activated := activatedEnvironment{Root: root, InstallRel: "install.sh", InstallScript: "echo bad"}
	if err := runEnvironmentInstall(context.Background(), sb, activated); err == nil {
		t.Fatal("symlink accepted")
	}
	if sb.commands != 0 {
		t.Fatal("installation ran before validating marker")
	}
	checkSecurityFixture(t, victim, "keep")
}

func TestTerminalHomeCleanupRestrictsLegacyPaths(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "keep")
	putSecurityFixture(t, victim, "keep")
	if err := removeTerminalHome(filepath.Dir(victim)); err == nil {
		t.Fatal("arbitrary cleanup accepted")
	}
	checkSecurityFixture(t, victim, "keep")
	home, err := os.MkdirTemp("", "agentflow-terminal-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	putSecurityFixture(t, filepath.Join(home, "data"), "temporary")
	if err := removeTerminalHome(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("legacy home remains: %v", err)
	}
}

func TestControlDirectoryConcurrentCreation(t *testing.T) {
	workspace := t.TempDir()
	var wg sync.WaitGroup
	start := make(chan struct{})
	errors := make(chan error, 64)
	for i := 0; i < cap(errors); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := EnsureControlDirectory(workspace, "runtime", "bin")
			if err != nil {
				errors <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("concurrent directory creation: %v", err)
	}
}
