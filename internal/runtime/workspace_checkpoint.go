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
	"strings"
)

// Checkpoints exclude execution credentials and reproducible dependency caches.
// A bounded archive that cannot be saved must never authorize workspace deletion.
const WorkspaceCheckpointMaxBytes = 12 << 20
const workspaceCheckpointExpandedMax = 256 << 20

func checkpointExcluded(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".agentflow" || part == "node_modules" || part == ".cache" || part == "__pycache__" {
			return true
		}
	}
	return false
}
func checkpointPath(path string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid checkpoint path")
	}
	return clean, nil
}
func CreateWorkspaceCheckpoint(root string) ([]byte, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace is not a directory")
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&checkpointBoundedWriter{writer: &buffer, left: WorkspaceCheckpointMaxBytes})
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
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = base.Readlink(path)
			if err != nil {
				return err
			}
			if filepath.IsAbs(link) {
				return fmt.Errorf("checkpoint symlink escapes workspace: %s", path)
			}
			if _, err = checkpointPath(filepath.Join(filepath.Dir(path), link)); err != nil {
				return fmt.Errorf("checkpoint symlink escapes workspace: %s", path)
			}
		} else if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported checkpoint file: %s", path)
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(path)
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""
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
			file, err := base.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(writer, file, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		if buffer.Len() > WorkspaceCheckpointMaxBytes {
			return fmt.Errorf("workspace checkpoint exceeds 12 MB; keep original workspace")
		}
		return nil
	})
	closeTar := writer.Close()
	closeZip := zipped.Close()
	if err != nil {
		return nil, err
	}
	if closeTar != nil {
		return nil, closeTar
	}
	if closeZip != nil {
		return nil, closeZip
	}
	if buffer.Len() > WorkspaceCheckpointMaxBytes {
		return nil, fmt.Errorf("workspace checkpoint exceeds 12 MB; keep original workspace")
	}
	return buffer.Bytes(), nil
}
func RestoreWorkspaceCheckpoint(root string, content []byte) error {
	if len(content) == 0 || len(content) > WorkspaceCheckpointMaxBytes {
		return fmt.Errorf("invalid workspace checkpoint size")
	}
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
	zipped, err := gzip.NewReader(bytes.NewReader(content))
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
		if checkpointExcluded(path) || seen[path] {
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
		if err = base.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			err = base.MkdirAll(path, os.FileMode(header.Mode)&0777)
		case tar.TypeReg:
			var file *os.File
			file, err = base.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode)&0777)
			if err == nil {
				_, err = io.CopyN(file, reader, header.Size)
				if err == nil {
					err = file.Sync()
				}
				closeErr := file.Close()
				if err == nil {
					err = closeErr
				}
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(header.Linkname) {
				return fmt.Errorf("checkpoint symlink escapes workspace")
			}
			if _, err = checkpointPath(filepath.Join(filepath.Dir(path), header.Linkname)); err != nil {
				return err
			}
			err = base.Symlink(header.Linkname, path)
		default:
			return fmt.Errorf("unsupported checkpoint entry")
		}
		if err != nil {
			return err
		}
	}
	// Finish validating the gzip checksum before exposing any restored files.
	if _, err = io.Copy(io.Discard, limited); err != nil {
		return err
	}
	if limited.N == 0 {
		return fmt.Errorf("checkpoint expanded size exceeds limit")
	}
	return os.Rename(stage, root)
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
	temp, err := os.MkdirTemp("", "workspace-checkpoint-validate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	return RestoreWorkspaceCheckpoint(filepath.Join(temp, "files"), content)
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
