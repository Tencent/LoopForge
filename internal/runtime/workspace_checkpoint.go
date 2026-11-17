package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Checkpoints exclude execution credentials and reproducible dependency caches.
// A bounded archive that cannot be saved must never authorize workspace deletion.
const WorkspaceCheckpointMaxBytes = 12 << 20

// Inline RPCs retain their original bound. Large archives use bounded chunks
// and disk-backed streams, never one large JSON value or database cell.
const WorkspaceCheckpointArchiveMaxBytes = 512 << 20
const WorkspaceCheckpointChunkBytes = 4 << 20
const workspaceCheckpointExpandedMax = 2 << 30
const checkpointGitOutputMaxBytes = 16 << 20

var checkpointGitIndexTimeout = 15 * time.Second

// checkpointRestoreExcludedNames are path components a restored archive must
// never contain (and never capture, even if committed inside a repository).
var checkpointRestoreExcludedNames = []string{".agentflow"}

// checkpointExcludedNames are skipped when capturing unless preserved as
// Git-tracked repository files (except checkpointRestoreExcludedNames).
var checkpointExcludedNames = append(slices.Clone(checkpointRestoreExcludedNames), "node_modules", ".cache", "__pycache__", ".zvec-grep")

// WorkspaceDirHasContent reports whether root already holds user or repository
// files on disk. An empty directory or one containing only the platform's
// runtime .agentflow directory holds no user workspace data.
func WorkspaceDirHasContent(root string) (bool, error) {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("workspace is not a directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() == ".agentflow" && entry.IsDir() {
			continue
		}
		return true, nil
	}
	return false, nil
}

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
	return CreateWorkspaceCheckpointContext(context.Background(), root)
}

func CreateWorkspaceCheckpointContext(ctx context.Context, root string) ([]byte, error) {
	var buffer bytes.Buffer
	err := CreateWorkspaceCheckpointToContext(ctx, root, &checkpointBoundedWriter{writer: &buffer, left: WorkspaceCheckpointMaxBytes})
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), err
}

func CreateWorkspaceCheckpointTo(root string, destination io.Writer) error {
	return CreateWorkspaceCheckpointToContext(context.Background(), root, destination)
}

func CreateWorkspaceCheckpointToContext(ctx context.Context, root string, destination io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
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
	tracker := &checkpointGitTracker{ctx: ctx, root: root, base: base}
	err = fs.WalkDir(base.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if path == "." {
			return nil
		}
		if checkpointExcluded(path) {
			if checkpointRestoreExcluded(path) || !tracker.keeps(path, entry.IsDir()) {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
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

type checkpointGitIndex struct {
	files map[string]bool
	dirs  map[string]bool
}

type checkpointLimitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *checkpointLimitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		b.overflow = true
		return 0, fmt.Errorf("git output exceeds limit")
	}
	return b.buf.Write(p)
}

// checkpointGitTracker lazily indexes Git-tracked files matching
// checkpointExcludedNames per repository inside root, so tracked files (such
// as committed web/*/node_modules assets) survive snapshots while untracked
// dependency installs are still skipped.
type checkpointGitTracker struct {
	ctx       context.Context
	root      string
	base      *os.Root
	repoFlags map[string]bool
	indexes   map[string]*checkpointGitIndex
}

func (t *checkpointGitTracker) keeps(rel string, isDir bool) bool {
	repoRel, ok := t.findRepo(pathpkg.Dir(filepath.ToSlash(rel)))
	if !ok {
		return false
	}
	idx := t.repoIndex(repoRel)
	if idx == nil || len(idx.files) == 0 {
		return false
	}
	repoFile := filepath.ToSlash(rel)
	if repoRel != "." {
		repoFile = strings.TrimPrefix(repoFile, repoRel+"/")
	}
	if isDir {
		return idx.dirs[repoFile] || idx.files[repoFile]
	}
	return idx.files[repoFile]
}

func (t *checkpointGitTracker) findRepo(startDir string) (string, bool) {
	if t.repoFlags == nil {
		t.repoFlags = map[string]bool{}
	}
	for dir := pathpkg.Clean(startDir); ; dir = pathpkg.Dir(dir) {
		if isRepo, checked := t.repoFlags[dir]; checked {
			if isRepo {
				return dir, true
			}
		} else {
			gitPath := ".git"
			if dir != "." {
				gitPath = pathpkg.Join(dir, ".git")
			}
			// Submodules and linked worktrees use a regular .git file (already
			// rejected when referencing external metadata), so only
			// self-contained .git directories are indexed here.
			info, err := t.base.Lstat(filepath.FromSlash(gitPath))
			isRepo = err == nil && info.IsDir()
			t.repoFlags[dir] = isRepo
			if isRepo {
				return dir, true
			}
		}
		if dir == "." || dir == "/" || dir == "" {
			return "", false
		}
	}
}

func (t *checkpointGitTracker) repoIndex(repoRel string) *checkpointGitIndex {
	if t.indexes == nil {
		t.indexes = map[string]*checkpointGitIndex{}
	}
	if idx, cached := t.indexes[repoRel]; cached {
		return idx
	}
	idx := &checkpointGitIndex{}
	t.indexes[repoRel] = idx
	if indexInfo, statErr := t.base.Lstat(filepath.FromSlash(pathpkg.Join(repoRel, ".git/index"))); statErr == nil {
		if !indexInfo.Mode().IsRegular() {
			return idx
		}
	} else if !os.IsNotExist(statErr) {
		return idx
	}
	repoAbs := t.root
	if repoRel != "." {
		repoAbs = filepath.Join(t.root, filepath.FromSlash(repoRel))
	}
	parentCtx := t.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	cmdCtx, cancel := context.WithTimeout(parentCtx, checkpointGitIndexTimeout)
	defer cancel()
	// Pin git-dir/work-tree and disable executable repo config keys, matching
	// gitwork.checkoutGit so scanning a sandbox-controlled repo stays safe.
	cmd := exec.CommandContext(cmdCtx, "git",
		"--git-dir="+filepath.Join(repoAbs, ".git"),
		"--work-tree="+repoAbs,
		"-c", "safe.directory=*",
		"-c", "core.fsmonitor=",
		"-c", "core.hooksPath="+os.DevNull,
		"-c", "core.pager=cat",
		"-c", "core.editor=true",
		"-c", "core.askPass=",
		"-c", "credential.helper=",
		"ls-files", "-z", "--",
	)
	cmd.WaitDelay = time.Second
	cmd.Dir = repoAbs
	env := make([]string, 0, len(os.Environ())+7)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT":
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
	)
	out := &checkpointLimitedBuffer{limit: checkpointGitOutputMaxBytes}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil || out.overflow || out.buf.Len() == 0 {
		return idx
	}
	idx.files = map[string]bool{}
	idx.dirs = map[string]bool{}
	for _, raw := range bytes.Split(out.buf.Bytes(), []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		p := filepath.ToSlash(string(raw))
		if !checkpointExcluded(p) || checkpointRestoreExcluded(p) {
			continue
		}
		idx.files[p] = true
		for dir := pathpkg.Dir(p); dir != "." && dir != ""; dir = pathpkg.Dir(dir) {
			if idx.dirs[dir] {
				break
			}
			idx.dirs[dir] = true
		}
	}
	return idx
}

func ensureReplaceableWorkspaceTarget(root string) (bool, error) {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("workspace already exists; refusing to overwrite")
	}
	hasContent, err := WorkspaceDirHasContent(root)
	if err != nil {
		return false, err
	}
	if hasContent {
		return false, fmt.Errorf("workspace already exists; refusing to overwrite")
	}
	return true, nil
}

func replaceRestoredWorkspace(stage, root string) error {
	return replaceRestoredWorkspaceWithRaceHook(stage, root, nil)
}

func replaceRestoredWorkspaceWithRaceHook(stage, root string, beforeRename func()) error {
	exists, err := ensureReplaceableWorkspaceTarget(root)
	if err != nil {
		return err
	}
	if !exists {
		return os.Rename(stage, root)
	}
	if beforeRename != nil {
		beforeRename()
	}
	backupParent, err := os.MkdirTemp(filepath.Dir(root), ".workspace-replace-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(backupParent)
	backup := filepath.Join(backupParent, "prev")
	if err := os.Rename(root, backup); err != nil {
		return err
	}
	hasContent, err := WorkspaceDirHasContent(backup)
	if err != nil || hasContent {
		_ = os.Rename(backup, root)
		if err != nil {
			return err
		}
		return fmt.Errorf("workspace already exists; refusing to overwrite")
	}
	if err := os.Rename(stage, root); err != nil {
		_ = os.Rename(backup, root)
		return err
	}
	return nil
}

func RestoreWorkspaceCheckpoint(root string, content []byte) error {
	if len(content) == 0 || len(content) > WorkspaceCheckpointMaxBytes {
		return fmt.Errorf("invalid workspace checkpoint size")
	}
	return RestoreWorkspaceCheckpointFrom(root, bytes.NewReader(content))
}

func RestoreWorkspaceCheckpointFrom(root string, source io.Reader) error {
	root = filepath.Clean(root)
	if _, err := ensureReplaceableWorkspaceTarget(root); err != nil {
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
	return replaceRestoredWorkspace(stage, root)
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
