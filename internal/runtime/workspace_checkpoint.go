package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Checkpoints exclude execution credentials and reproducible dependency caches.
// A bounded archive that cannot be saved must never authorize workspace deletion.
const WorkspaceCheckpointMaxBytes = 12 << 20

// Inline RPCs retain their original bound. Large archives use bounded chunks
// and disk-backed streams, never one large JSON value or database cell.
const WorkspaceCheckpointArchiveMaxBytes = 512 << 20
const WorkspaceCheckpointChunkBytes = 4 << 20
const workspaceCheckpointExpandedMax = 2 << 30

// checkpointRestoreExcludedNames are path components a restored archive must
// never contain.
var checkpointRestoreExcludedNames = []string{".agentflow", "node_modules", ".cache", "__pycache__"}

// checkpointExcludedNames are skipped when capturing. Older valid archives can
// contain .zvec-grep, so it is excluded from new captures only and must not
// make those existing snapshots unrestorable.
var checkpointExcludedNames = append(slices.Clone(checkpointRestoreExcludedNames), ".zvec-grep")

func checkpointPathHasComponent(path string, names []string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if slices.Contains(names, part) {
			return true
		}
	}
	return false
}

func checkpointExcluded(path string) bool {
	return checkpointPathHasComponent(path, checkpointExcludedNames)
}

func checkpointRestoreExcluded(path string) bool {
	return checkpointPathHasComponent(path, checkpointRestoreExcludedNames)
}

func checkpointPath(path string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid checkpoint path")
	}
	return clean, nil
}
func CreateWorkspaceCheckpoint(root string) ([]byte, error) {
	var buffer bytes.Buffer
	err := CreateWorkspaceCheckpointTo(root, &checkpointBoundedWriter{writer: &buffer, left: WorkspaceCheckpointMaxBytes})
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), err
}

func CreateWorkspaceCheckpointTo(root string, destination io.Writer) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace is not a directory")
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer base.Close()
	zipped, err := gzip.NewWriterLevel(&checkpointBoundedWriter{writer: destination, left: WorkspaceCheckpointArchiveMaxBytes}, gzip.BestSpeed)
	if err != nil {
		return err
	}
	writer := tar.NewWriter(&checkpointBoundedWriter{writer: zipped, left: workspaceCheckpointExpandedMax})
	var expanded int64
	entries := 0
	err = fs.WalkDir(base.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		if checkpointExcluded(path) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		entries++
		if entries > 100000 {
			return fmt.Errorf("workspace checkpoint exceeds file count limit")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := checkpointEntryHeader(base, path, info)
		if err != nil {
			return err
		}
		expanded += header.Size
		if expanded > workspaceCheckpointExpandedMax {
			return fmt.Errorf("workspace checkpoint exceeds expanded size limit")
		}
		if err = writer.WriteHeader(header); err != nil {
			return err
		}
		if filepath.Base(path) == ".git" && info.Mode().IsRegular() {
			return fmt.Errorf("external Git worktree metadata cannot be checkpointed: %s", path)
		}
		if info.Mode().IsRegular() {
			return copyCheckpointFile(base, writer, path, header.Size)
		}
		return nil
	})
	closeTar := writer.Close()
	closeZip := zipped.Close()
	if err != nil {
		return err
	}
	if closeTar != nil {
		return closeTar
	}
	if closeZip != nil {
		return closeZip
	}
	return nil
}

// checkpointEntryHeader builds the tar header for one workspace entry: symlink
// targets are recorded verbatim, entries other than directories, regular files
// and symlinks are rejected, and ownership is cleared so archives do not depend
// on the host's users.
func checkpointEntryHeader(base *os.Root, path string, info fs.FileInfo) (*tar.Header, error) {
	link := ""
	if info.Mode()&os.ModeSymlink != 0 {
		// Links are archived verbatim, including absolute or external targets
		// that a repository checkout can legitimately contain. Rejecting them
		// left such workspaces unsaved and therefore never reclaimed.
		var err error
		link, err = base.Readlink(path)
		if err != nil {
			return nil, err
		}
	} else if !info.IsDir() && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unsupported checkpoint file: %s", path)
	}
	header, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return nil, err
	}
	header.Name = filepath.ToSlash(path)
	header.Uid = 0
	header.Gid = 0
	header.Uname = ""
	header.Gname = ""
	return header, nil
}

// copyCheckpointFile streams size bytes of the regular file at path into
// writer, reporting a copy failure before a close failure.
func copyCheckpointFile(base *os.Root, writer io.Writer, path string, size int64) error {
	file, err := base.Open(path)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyN(writer, file, size)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func RestoreWorkspaceCheckpoint(root string, content []byte) error {
	if len(content) == 0 || len(content) > WorkspaceCheckpointMaxBytes {
		return fmt.Errorf("invalid workspace checkpoint size")
	}
	return RestoreWorkspaceCheckpointFrom(root, bytes.NewReader(content))
}

func RestoreWorkspaceCheckpointFrom(root string, source io.Reader) error {
	if _, err := os.Lstat(root); err == nil {
		return fmt.Errorf("workspace already exists; refusing to overwrite")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(root), ".workspace-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	base, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer base.Close()
	compressed := &io.LimitedReader{R: source, N: WorkspaceCheckpointArchiveMaxBytes + 1}
	zipped, err := gzip.NewReader(compressed)
	if err != nil {
		return err
	}
	defer zipped.Close()
	limited := &io.LimitedReader{R: zipped, N: workspaceCheckpointExpandedMax + 1}
	reader := tar.NewReader(limited)
	var expanded int64
	seen := map[string]bool{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		path, err := checkpointPath(header.Name)
		if err != nil {
			return err
		}
		if checkpointRestoreExcluded(path) || seen[path] {
			return fmt.Errorf("invalid checkpoint entry: %s", path)
		}
		if len(seen) >= 100000 {
			return fmt.Errorf("checkpoint exceeds file count limit")
		}
		seen[path] = true
		expanded += header.Size
		if header.Size < 0 || expanded > workspaceCheckpointExpandedMax {
			return fmt.Errorf("checkpoint expanded size exceeds limit")
		}
		if err = restoreCheckpointEntry(base, path, header, reader); err != nil {
			return err
		}
	}
	// Finish validating the gzip checksum before exposing any restored files.
	if _, err = io.Copy(io.Discard, limited); err != nil {
		return err
	}
	if limited.N == 0 || compressed.N == 0 {
		return fmt.Errorf("checkpoint expanded size exceeds limit")
	}
	return os.Rename(stage, root)
}

// restoreCheckpointEntry materializes one validated tar entry at path inside
// the staging root. Regular file content is read from reader, which must be
// positioned at the entry body.
func restoreCheckpointEntry(base *os.Root, path string, header *tar.Header, reader io.Reader) error {
	if err := base.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	mode := os.FileMode(header.Mode) & 0777
	switch header.Typeflag {
	case tar.TypeDir:
		return base.MkdirAll(path, mode)
	case tar.TypeReg:
		file, err := base.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, err = io.CopyN(file, reader, header.Size)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		return err
	case tar.TypeSymlink:
		// The target is restored as inert data, exactly like git checkout
		// does. os.Root refuses to traverse any link leaving the staging
		// root, so later entries can never be written through it.
		return base.Symlink(header.Linkname, path)
	default:
		return fmt.Errorf("unsupported checkpoint entry")
	}
}

func EmptyWorkspaceCheckpoint() []byte {
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(zipped)
	_ = writer.Close()
	_ = zipped.Close()
	return buffer.Bytes()
}

func ValidateWorkspaceCheckpoint(content []byte) error {
	if len(content) == 0 || len(content) > WorkspaceCheckpointMaxBytes {
		return fmt.Errorf("invalid workspace checkpoint size")
	}
	return ValidateWorkspaceCheckpointFrom(bytes.NewReader(content))
}
func ValidateWorkspaceCheckpointFrom(source io.Reader) error {
	temp, err := os.MkdirTemp("", "workspace-checkpoint-validate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	return RestoreWorkspaceCheckpointFrom(filepath.Join(temp, "files"), source)
}

// Bound both compressed bytes in memory and the complete tar stream, including
// headers/padding. An archive accepted by capture must fit the restore limit.
type checkpointBoundedWriter struct {
	writer io.Writer
	left   int64
}

func (w *checkpointBoundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.left {
		return 0, fmt.Errorf("workspace checkpoint exceeds size limit; keep original workspace")
	}
	n, err := w.writer.Write(p)
	w.left -= int64(n)
	return n, err
}
