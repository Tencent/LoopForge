package runtime

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
)

// The same absolute workspace path is used in the sandbox so command bindings
// and their generated configuration can keep referring to it.
const openSandboxImportWorkspace = `
workspace=$1
archive=$2
parent=$(dirname -- "$workspace")
umask 022
mkdir -p -- "$parent"
umask 077
stage=$(mktemp -d "$parent/.loopforge-import.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT
tar -xpzf "$archive" --no-same-owner -C "$stage"
if [ "$(id -u)" -eq 0 ]; then
	chown -hR "$3:$4" "$stage"
fi
rm -rf -- "$workspace"
mv -- "$stage" "$workspace"
`

const openSandboxExportWorkspace = `
umask 077
tar --format=pax --pax-option=delete=atime,delete=ctime --hard-dereference \
	--exclude='./.agentflow/private-home/*/.codex/tmp' -czf "$2" -C "$1" .
`

type workspaceTransferEntry struct {
	mode   fs.FileMode
	target string
	digest [sha256.Size]byte
}

func workspaceTransferLinkIsLocal(path, target string) bool {
	return target != "" && !filepath.IsAbs(target) && filepath.IsLocal(filepath.Join(filepath.Dir(path), target))
}

func workspaceTransferSkips(path string) bool {
	// Codex puts links to binaries in the sandbox image in its temp directory.
	// Keep the CLI's durable home and journals portable across commands.
	parts := strings.SplitN(filepath.ToSlash(path), "/", 6)
	return len(parts) >= 5 && parts[0] == ".agentflow" && parts[1] == "private-home" &&
		parts[3] == ".codex" && parts[4] == "tmp"
}

func (s *openSandbox) uploadWorkspace(ctx context.Context, instance *opensandbox.Sandbox, workspace string, skip func(string) bool, uid, gid int32) (map[string]workspaceTransferEntry, error) {
	base, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	archive, err := os.CreateTemp("", "loopforge-opensandbox-*.tar.gz")
	if err != nil {
		return nil, err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()

	zipped, err := gzip.NewWriterLevel(&checkpointBoundedWriter{writer: archive, left: WorkspaceCheckpointArchiveMaxBytes}, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	writer := tar.NewWriter(&checkpointBoundedWriter{writer: zipped, left: workspaceCheckpointExpandedMax})
	baseline, err := readWorkspaceTransfer(ctx, base, writer, skip)
	if err := errors.Join(err, writer.Close(), zipped.Close()); err != nil {
		return nil, err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	remote := "/tmp/loopforge-workspace-" + rand.Text() + ".tar.gz"
	defer removeOpenSandboxArchive(ctx, instance, remote)
	if err := uploadOpenSandboxFile(ctx, instance, remote, archive); err != nil {
		return nil, err
	}
	_, err = s.runCommand(ctx, instance, opensandbox.RunCommandRequest{
		Cwd: "/", Argv: []string{"/bin/sh", "-ec", openSandboxImportWorkspace, "loopforge-workspace-import", workspace, remote,
			strconv.FormatInt(int64(uid), 10), strconv.FormatInt(int64(gid), 10)},
	}, CommandSpec{}, nil)
	return baseline, err
}

func (s *openSandbox) downloadWorkspace(ctx context.Context, instance *opensandbox.Sandbox, workspace string, skip func(string) bool, baseline map[string]workspaceTransferEntry) error {
	remote := "/tmp/loopforge-workspace-" + rand.Text() + ".tar.gz"
	defer removeOpenSandboxArchive(ctx, instance, remote)
	if _, err := s.runCommand(ctx, instance, opensandbox.RunCommandRequest{
		Cwd: "/", Argv: []string{"/bin/sh", "-ec", openSandboxExportWorkspace, "loopforge-workspace-export", workspace, remote},
	}, CommandSpec{}, nil); err != nil {
		return err
	}
	archive, err := instance.DownloadFile(ctx, remote, "")
	if err != nil {
		return err
	}
	defer archive.Close()

	stagePath, err := os.MkdirTemp(filepath.Dir(workspace), ".opensandbox-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stagePath)
	stage, err := os.OpenRoot(stagePath)
	if err != nil {
		return err
	}
	defer stage.Close()
	directories, err := unpackWorkspaceArchive(ctx, stage, archive, skip)
	if err != nil {
		return err
	}
	// Descendants precede their parents before a chmod can restrict access.
	slices.SortFunc(directories, func(a, b checkpointDirectory) int {
		return len(b.path) - len(a.path)
	})
	for _, directory := range directories {
		if err := stage.Chmod(directory.path, directory.mode); err != nil {
			return err
		}
	}
	return publishOpenSandboxWorkspace(ctx, workspace, stage, baseline, skip)
}

func uploadOpenSandboxFile(ctx context.Context, instance *opensandbox.Sandbox, remote string, data io.Reader) error {
	return instance.UploadFile(ctx, data, opensandbox.UploadFileOptions{
		FileName: filepath.Base(remote),
		// Execd decodes the JSON integer as octal digits.
		Metadata: opensandbox.FileMetadata{Path: remote, Mode: 600},
	})
}

func removeOpenSandboxArchive(ctx context.Context, instance *opensandbox.Sandbox, remote string) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = instance.DeleteFiles(cleanup, []string{remote})
}

func readWorkspaceTransfer(ctx context.Context, base *os.Root, writer *tar.Writer, skip func(string) bool) (map[string]workspaceTransferEntry, error) {
	entries := make(map[string]workspaceTransferEntry)
	var expanded int64
	err := fs.WalkDir(base.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || path == "." {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if skip(path) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if len(entries) >= 100000 {
			return errors.New("workspace exceeds file count limit")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && filepath.Base(path) == ".git" {
			return fmt.Errorf("external Git worktree cannot be transferred: %s", path)
		}
		header, err := checkpointEntryHeader(base, path, info)
		if err != nil {
			return err
		}
		expanded += header.Size
		if expanded > workspaceCheckpointExpandedMax {
			return errors.New("workspace exceeds expanded size limit")
		}
		if header.Typeflag == tar.TypeSymlink {
			if !workspaceTransferLinkIsLocal(path, header.Linkname) {
				return fmt.Errorf("link leaves workspace: %s", path)
			}
			if _, err := base.Stat(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("cannot resolve workspace link %s: %w", path, err)
			}
		}
		record := workspaceTransferEntry{mode: info.Mode().Type() | info.Mode().Perm(), target: header.Linkname}
		if writer != nil {
			header.Mode &= 0777
			header.Format = tar.FormatPAX
			if err := writer.WriteHeader(header); err != nil {
				return err
			}
		}
		if info.Mode().IsRegular() {
			hash := sha256.New()
			var output io.Writer = hash
			if writer != nil {
				output = io.MultiWriter(hash, writer)
			}
			if err := withWorkspaceTransferFile(base, path, info, func(file *os.File) error {
				_, err := io.CopyN(output, file, header.Size)
				return err
			}); err != nil {
				return err
			}
			copy(record.digest[:], hash.Sum(nil))
		}
		entries[path] = record
		return nil
	})
	return entries, err
}

func withWorkspaceTransferFile(base *os.Root, path string, info fs.FileInfo, read func(*os.File) error) error {
	file, err := base.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	same := func() bool {
		actual, err := file.Stat()
		return err == nil && actual.Mode().IsRegular() && os.SameFile(info, actual) &&
			info.Mode() == actual.Mode() && info.Size() == actual.Size() && info.ModTime().Equal(actual.ModTime())
	}
	if !same() {
		return fmt.Errorf("workspace file changed: %s", path)
	}
	if err := read(file); err != nil {
		return err
	}
	if !same() {
		return fmt.Errorf("workspace file changed: %s", path)
	}
	return nil
}

func publishOpenSandboxWorkspace(ctx context.Context, workspace string, stage *os.Root, baseline map[string]workspaceTransferEntry, skip func(string) bool) error {
	remote, err := readWorkspaceTransfer(ctx, stage, nil, skip)
	if err != nil || maps.Equal(remote, baseline) {
		return err
	}
	local, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer local.Close()
	original, err := local.Stat(".")
	if err != nil {
		return err
	}
	current, err := readWorkspaceTransfer(ctx, local, nil, skip)
	if err != nil {
		return err
	}

	// Local changes made while the command ran are overlaid on the private
	// staging tree. A conflicting edit leaves the current workspace intact.
	paths := make(map[string]struct{}, len(baseline)+len(current))
	for path := range baseline {
		paths[path] = struct{}{}
	}
	for path := range current {
		paths[path] = struct{}{}
	}
	var changes []string
	for path := range paths {
		old, hadOld := baseline[path]
		host, hasHost := current[path]
		guest, hasGuest := remote[path]
		if (hadOld == hasHost && old == host) || (hasGuest == hasHost && guest == host) {
			continue
		}
		if hadOld != hasGuest || old != guest {
			return fmt.Errorf("并行修改了工作区文件 %s", path)
		}
		changes = append(changes, path)
	}
	slices.Sort(changes)
	for _, path := range slices.Backward(changes) {
		old, exists := remote[path]
		host, keep := current[path]
		if exists && !(old.mode.IsDir() && keep && host.mode.IsDir()) {
			// Remove only this entry. A remote child in a removed local
			// directory is a conflict, so do not remove it recursively.
			if err := stage.Remove(path); err != nil {
				return fmt.Errorf("同步工作区文件 %s：%w", path, err)
			}
			delete(remote, path)
		}
	}
	for _, path := range changes {
		host, keep := current[path]
		if !keep {
			continue
		}
		parent := filepath.Dir(path)
		if parent != "." && !remote[parent].mode.IsDir() {
			return fmt.Errorf("工作区父目录已发生变化：%s", parent)
		}
		if err := copyWorkspaceTransferEntry(local, stage, path); err != nil {
			return fmt.Errorf("保留工作区文件 %s：%w", path, err)
		}
		remote[path] = host
	}
	// Overlaying a tree can change the resolution of a relative link.
	if len(changes) > 0 {
		if _, err := readWorkspaceTransfer(ctx, stage, nil, skip); err != nil {
			return err
		}
	}
	if err := stage.Chmod(".", original.Mode().Perm()); err != nil {
		return err
	}
	verified, err := readWorkspaceTransfer(ctx, local, nil, skip)
	if err != nil {
		return err
	}
	info, err := os.Lstat(workspace)
	if err != nil {
		return err
	}
	if !os.SameFile(original, info) || !maps.Equal(current, verified) {
		return errors.New("提交结果时工作区发生了变化")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return exchangeOpenSandboxWorkspace(stage.Name(), workspace)
}

func copyWorkspaceTransferEntry(local, stage *os.Root, path string) error {
	info, err := local.Lstat(path)
	if err != nil {
		return err
	}
	header, err := checkpointEntryHeader(local, path, info)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		if err := restoreCheckpointEntry(stage, path, header, nil); err != nil {
			return err
		}
		if info.IsDir() {
			return stage.Chmod(path, info.Mode().Perm())
		}
		return nil
	}
	if err := withWorkspaceTransferFile(local, path, info, func(file *os.File) error {
		return restoreCheckpointEntry(stage, path, header, file)
	}); err != nil {
		return err
	}
	if err := stage.Chmod(path, info.Mode().Perm()); err != nil {
		return err
	}
	return stage.Chtimes(path, info.ModTime(), info.ModTime())
}
