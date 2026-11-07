package runtime

import (
	"agentflow/internal/domain"
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceCheckpointStreamPreservesGitAndLargeArtifacts(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	if err := os.MkdirAll(repository, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Checkpoint Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Checkpoint Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return string(out)
	}
	write := func(path, content string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	git(repository, "init", "-q", "-b", "feature/unpushed")
	write("repo/source.txt", "initial\n")
	write("repo/deleted.txt", "tracked\n")
	large, err := os.Create(filepath.Join(repository, "binary.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.CopyN(large, rand.Reader, 13<<20); err != nil {
		t.Fatal(err)
	}
	large.Close()
	git(repository, "add", ".")
	git(repository, "commit", "-qm", "baseline")
	write("repo/source.txt", "local commit\n")
	git(repository, "commit", "-qam", "unpushed commit")
	write("repo/source.txt", "staged change\n")
	git(repository, "add", "source.txt")
	write("repo/source.txt", "unstaged change\n")
	if err = os.Remove(filepath.Join(repository, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	write("repo/.gitignore", "ignored-report.txt\n")
	write("repo/ignored-report.txt", "ignored but must survive\n")
	write("repo/vendor/local-patch.php", "preserve dependency edits\n")
	write("repo/.zvec-grep/index", "rebuildable cache\n")
	write(".agentflow/auth/token", "excluded\n")
	report, err := os.Create(filepath.Join(root, "large-report.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err = report.Truncate(270 << 20); err != nil {
		t.Fatal(err)
	}
	report.Close()
	head := git(repository, "rev-parse", "HEAD")
	staged, unstaged := git(repository, "diff", "--cached", "--binary"), git(repository, "diff", "--binary")
	archive, err := os.CreateTemp(t.TempDir(), "archive")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if err = CreateWorkspaceCheckpointTo(root, archive); err != nil {
		t.Fatal(err)
	}
	info, _ := archive.Stat()
	if info.Size() <= WorkspaceCheckpointMaxBytes {
		t.Fatal("fixture did not exceed old transport limit")
	}
	if _, err = archive.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err = RestoreWorkspaceCheckpointFrom(destination, archive); err != nil {
		t.Fatal(err)
	}
	restoredRepo := filepath.Join(destination, "repo")
	if git(restoredRepo, "rev-parse", "HEAD") != head || git(restoredRepo, "branch", "--show-current") != "feature/unpushed\n" {
		t.Fatal("unpushed commit or branch lost")
	}
	if git(restoredRepo, "diff", "--cached", "--binary") != staged || git(restoredRepo, "diff", "--binary") != unstaged {
		t.Fatal("staged/unstaged state changed")
	}
	git(restoredRepo, "fsck", "--full")
	for _, path := range []string{"repo/ignored-report.txt", "repo/vendor/local-patch.php"} {
		a, _ := os.ReadFile(filepath.Join(root, path))
		b, err := os.ReadFile(filepath.Join(destination, path))
		if err != nil || !bytes.Equal(a, b) {
			t.Fatal("file lost", path, err)
		}
	}
	if stat, err := os.Stat(filepath.Join(destination, "large-report.bin")); err != nil || stat.Size() != 270<<20 {
		t.Fatal("large expanded report lost", err)
	}
	for _, path := range []string{".agentflow", "repo/.zvec-grep"} {
		if _, err := os.Stat(filepath.Join(destination, path)); !os.IsNotExist(err) {
			t.Fatal("cache or credentials restored", path)
		}
	}
	a, _ := os.ReadFile(filepath.Join(repository, "binary.dat"))
	b, _ := os.ReadFile(filepath.Join(restoredRepo, "binary.dat"))
	if !bytes.Equal(a, b) {
		t.Fatal("binary content lost")
	}
}

func TestWorkspaceCheckpointTransfersBoundScopeIntegrityAndExpiry(t *testing.T) {
	var pool CheckpointTransfers
	defer pool.Close()
	ctx := context.Background()
	root := t.TempDir()
	data := make([]byte, WorkspaceCheckpointChunkBytes+1)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary"), data, 0755); err != nil {
		t.Fatal(err)
	}
	call := func(run, root, op string, info *domain.WorkspaceCheckpointTransfer) (domain.WorkspaceCheckpointTransfer, domain.RunnerWorkspaceResponse) {
		t.Helper()
		response := pool.Handle(ctx, root, domain.RunnerWorkspaceRequest{RunID: run, Operation: op, CheckpointTransfer: info})
		var result domain.WorkspaceCheckpointTransfer
		if response.Error == "" {
			if err := json.Unmarshal(response.Payload, &result); err != nil {
				t.Fatal(err)
			}
		}
		return result, response
	}
	export, response := call("run", root, domain.RunnerWorkspaceOperationCheckpointExport, nil)
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	if _, response = call("different-run", root, domain.RunnerWorkspaceOperationCheckpointRead, &domain.WorkspaceCheckpointTransfer{ID: export.ID}); response.Error == "" {
		t.Fatal("cross-run download allowed")
	}
	var archive bytes.Buffer
	for offset := int64(0); offset < export.Size; {
		chunk, response := call("run", root, domain.RunnerWorkspaceOperationCheckpointRead, &domain.WorkspaceCheckpointTransfer{ID: export.ID, Offset: offset})
		if response.Error != "" {
			t.Fatal(response.Error)
		}
		if len(chunk.Data) > WorkspaceCheckpointChunkBytes {
			t.Fatal("oversized frame")
		}
		archive.Write(chunk.Data)
		offset += int64(len(chunk.Data))
	}
	sum := sha256.Sum256(archive.Bytes())
	if export.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("export checksum mismatch")
	}
	destination := filepath.Join(t.TempDir(), "restored")
	upload, response := call("run", destination, domain.RunnerWorkspaceOperationCheckpointUpload, &domain.WorkspaceCheckpointTransfer{Size: export.Size, SHA256: export.SHA256})
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	if _, response = call("run", destination, domain.RunnerWorkspaceOperationCheckpointAppend, &domain.WorkspaceCheckpointTransfer{ID: upload.ID, Offset: 1, Data: []byte("x")}); response.Error == "" {
		t.Fatal("out-of-order write accepted")
	}
	if _, response = call("run", destination, domain.RunnerWorkspaceOperationCheckpointCommit, &domain.WorkspaceCheckpointTransfer{ID: upload.ID}); response.Error == "" {
		t.Fatal("incomplete upload published")
	}
	content := archive.Bytes()
	content[0] ^= 1
	for offset := 0; offset < len(content); {
		end := min(offset+WorkspaceCheckpointChunkBytes, len(content))
		if _, response = call("run", destination, domain.RunnerWorkspaceOperationCheckpointAppend, &domain.WorkspaceCheckpointTransfer{ID: upload.ID, Offset: int64(offset), Data: content[offset:end]}); response.Error != "" {
			t.Fatal(response.Error)
		}
		offset = end
	}
	if _, response = call("run", destination, domain.RunnerWorkspaceOperationCheckpointCommit, &domain.WorkspaceCheckpointTransfer{ID: upload.ID}); response.Error == "" {
		t.Fatal("corrupt upload published")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("partial destination exposed")
	}
	name := pool.items[export.ID].file.Name()
	pool.Sweep(time.Now().Add(6 * time.Minute))
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("expired transfer file leaked")
	}
}

func TestWorkspaceCheckpointLegacyIndexCacheRemainsRestorable(t *testing.T) {
	content := makeCheckpoint(t,
		&tar.Header{Name: ".zvec-grep", Typeflag: tar.TypeDir, Mode: 0755},
		&tar.Header{Name: ".zvec-grep/index", Typeflag: tar.TypeReg, Mode: 0644})
	destination := filepath.Join(t.TempDir(), "legacy")
	if err := RestoreWorkspaceCheckpoint(destination, content); err != nil {
		t.Fatal("new exclusion rejected existing valid archive", err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".zvec-grep/index")); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveCheckpointFileStaysInTempDir(t *testing.T) {
	for _, base := range []string{
		"..",
		"../etc/passwd",
		"agentflow-checkpoint-../x",
		"agentflow-checkpoint-",
		"agentflow-checkpoint-foo/bar",
		"passwd",
	} {
		if checkpointTempBase(base) {
			t.Fatalf("accepted checkpoint name %q", base)
		}
	}
	if !checkpointTempBase("agentflow-checkpoint-abc123") {
		t.Fatal("rejected generated checkpoint name")
	}

	outside := t.TempDir()
	base := "agentflow-checkpoint-outside"
	outsidePath := filepath.Join(outside, base)
	created, err := os.Create(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = created.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := os.Open(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	removeCheckpointFile(opened)
	if err = opened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(outsidePath); err != nil {
		t.Fatal("checkpoint cleanup removed a file outside the temp directory", err)
	}

	temp, err := os.CreateTemp("", "agentflow-checkpoint-")
	if err != nil {
		t.Fatal(err)
	}
	name := temp.Name()
	if err = temp.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err = os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	removeCheckpointFile(opened)
	_ = opened.Close()
	if _, err = os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("temp checkpoint file was not removed", err)
	}
}
