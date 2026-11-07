package preview

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"
)

const tarEpoch = int64(0)

// ArchiveDir writes a normalized tar.zst of sourcePath, which must live inside
// workspace. The walker refuses symlinks, devices, sockets, FIFOs, and
// hardlinks, and it never shells out to tar.
func ArchiveDir(workspace, sourcePath, indexFile string, limits Limits, dst io.Writer) (Result, error) {
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return Result{}, fmt.Errorf("resolve workspace: %w", err)
	}
	rel, err := SanitizeRelPath(sourcePath)
	if err != nil {
		return Result{}, err
	}
	indexFile, err = SanitizeRelPath(indexFile)
	if err != nil {
		return Result{}, err
	}
	root := filepath.Join(workspace, filepath.FromSlash(rel))
	info, err := os.Lstat(root)
	if err != nil {
		return Result{}, fmt.Errorf("open preview source: %w", err)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("preview source %q is not a directory", rel)
	}
	if err := rejectSpecial(root, info); err != nil {
		return Result{}, err
	}

	entries, expanded, err := collectRegularFiles(workspace, root, limits)
	if err != nil {
		return Result{}, err
	}
	if !containsIndex(entries, indexFile) {
		return Result{}, fmt.Errorf("%w: %s", ErrMissingIndex, indexFile)
	}

	hash := sha256.New()
	counter := &countWriter{w: io.MultiWriter(dst, hash), limit: limits.MaxCompressed}
	enc, err := zstd.NewWriter(counter, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return Result{}, fmt.Errorf("zstd encoder: %w", err)
	}
	tw := tar.NewWriter(enc)
	for _, item := range entries {
		if err := writeTarFile(tw, item); err != nil {
			_ = tw.Close()
			_ = enc.Close()
			return Result{}, err
		}
	}
	if err := tw.Close(); err != nil {
		_ = enc.Close()
		return Result{}, err
	}
	if err := enc.Close(); err != nil {
		return Result{}, err
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	return Result{
		StorageKey:     ContentKey(sum),
		SHA256:         sum,
		CompressedSize: counter.n,
		ExpandedSize:   expanded,
		FileCount:      len(entries),
		EntryFile:      indexFile,
	}, nil
}

type fileEntry struct {
	abs  string
	name string
	mode os.FileMode
	size int64
}

func collectRegularFiles(workspace, root string, limits Limits) ([]fileEntry, int64, error) {
	var (
		entries  []fileEntry
		expanded int64
	)
	err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := rejectSpecial(abs, info); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if _, err := SanitizeArchiveMember(rel); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s", ErrUnsupportedEntry, rel)
		}
		if limits.MaxFiles > 0 && len(entries)+1 > limits.MaxFiles {
			return fmt.Errorf("%w: more than %d files", ErrLimitExceeded, limits.MaxFiles)
		}
		expanded += info.Size()
		if limits.MaxExpanded > 0 && expanded > limits.MaxExpanded {
			return fmt.Errorf("%w: expanded size", ErrLimitExceeded)
		}
		if _, err := filepath.Rel(workspace, abs); err != nil {
			return fmt.Errorf("%w: %s", ErrUnsafePath, rel)
		}
		entries = append(entries, fileEntry{abs: abs, name: rel, mode: info.Mode(), size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries, expanded, nil
}

func containsIndex(entries []fileEntry, indexFile string) bool {
	for _, item := range entries {
		if item.name == indexFile {
			return true
		}
	}
	return false
}

func writeTarFile(tw *tar.Writer, item fileEntry) error {
	src, err := os.Open(item.abs)
	if err != nil {
		return err
	}
	defer src.Close()
	mode := int64(0o644)
	if item.mode&0o111 != 0 {
		mode = 0o755
	}
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     item.name,
		Mode:     mode,
		Size:     item.size,
		ModTime:  time.Unix(tarEpoch, 0).UTC(),
		Uid:      0,
		Gid:      0,
		Uname:    "",
		Gname:    "",
		Format:   tar.FormatUSTAR,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		hdr.Format = tar.FormatPAX
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
	}
	n, err := io.Copy(tw, src)
	if err != nil {
		return err
	}
	if n != item.size {
		return fmt.Errorf("preview file %s changed during archive", item.name)
	}
	return nil
}

func rejectSpecial(path string, info os.FileInfo) error {
	mode := info.Mode()
	if mode&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symlink %s", ErrUnsupportedEntry, path)
	}
	if mode&(os.ModeDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeCharDevice) != 0 {
		return fmt.Errorf("%w: special file %s", ErrUnsupportedEntry, path)
	}
	if info.IsDir() {
		return nil
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
		return fmt.Errorf("%w: hardlink %s", ErrUnsupportedEntry, path)
	}
	return nil
}

type countWriter struct {
	w     io.Writer
	n     int64
	limit int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	if c.limit > 0 && c.n+int64(len(p)) > c.limit {
		return 0, fmt.Errorf("%w: compressed size", ErrLimitExceeded)
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// InspectTarZst validates a runner-uploaded bundle before it is committed.
func InspectTarZst(src io.Reader, indexFile string, limits Limits) (Result, error) {
	indexFile, err := SanitizeRelPath(indexFile)
	if err != nil {
		return Result{}, err
	}
	hash := sha256.New()
	counter := &countWriter{w: hash, limit: limits.MaxCompressed}
	tee := io.TeeReader(src, counter)
	dec, err := zstd.NewReader(tee)
	if err != nil {
		return Result{}, fmt.Errorf("zstd decoder: %w", err)
	}
	defer dec.Close()
	tr := tar.NewReader(dec)
	var (
		files    int
		expanded int64
		found    bool
	)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, fmt.Errorf("read preview tar: %w", err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, tar.TypeRegA:
		default:
			return Result{}, fmt.Errorf("%w: tar type %v", ErrUnsupportedEntry, hdr.Typeflag)
		}
		name, err := SanitizeArchiveMember(hdr.Name)
		if err != nil {
			return Result{}, err
		}
		if hdr.Linkname != "" {
			return Result{}, fmt.Errorf("%w: link %s", ErrUnsupportedEntry, name)
		}
		files++
		if limits.MaxFiles > 0 && files > limits.MaxFiles {
			return Result{}, fmt.Errorf("%w: more than %d files", ErrLimitExceeded, limits.MaxFiles)
		}
		expanded += hdr.Size
		if limits.MaxExpanded > 0 && expanded > limits.MaxExpanded {
			return Result{}, fmt.Errorf("%w: expanded size", ErrLimitExceeded)
		}
		copied, err := io.Copy(io.Discard, io.LimitReader(tr, hdr.Size+1))
		if err != nil {
			return Result{}, err
		}
		if copied != hdr.Size {
			return Result{}, fmt.Errorf("tar member %s size mismatch", name)
		}
		if name == indexFile {
			found = true
		}
	}
	if _, err := io.Copy(io.Discard, dec); err != nil && !errors.Is(err, io.EOF) {
		return Result{}, err
	}
	if !found {
		return Result{}, fmt.Errorf("%w: %s", ErrMissingIndex, indexFile)
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	return Result{
		StorageKey:     ContentKey(sum),
		SHA256:         sum,
		CompressedSize: counter.n,
		ExpandedSize:   expanded,
		FileCount:      files,
		EntryFile:      indexFile,
	}, nil
}

// ExtractTarZst materializes a validated archive into dest. dest is replaced
// atomically when the extract succeeds.
func ExtractTarZst(src io.Reader, dest string, limits Limits) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "expand-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	dec, err := zstd.NewReader(src)
	if err != nil {
		return err
	}
	defer dec.Close()
	tr := tar.NewReader(dec)
	var (
		files    int
		expanded int64
	)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			return fmt.Errorf("%w: tar type %v", ErrUnsupportedEntry, hdr.Typeflag)
		}
		name, err := SanitizeArchiveMember(hdr.Name)
		if err != nil {
			return err
		}
		files++
		if limits.MaxFiles > 0 && files > limits.MaxFiles {
			return fmt.Errorf("%w: more than %d files", ErrLimitExceeded, limits.MaxFiles)
		}
		expanded += hdr.Size
		if limits.MaxExpanded > 0 && expanded > limits.MaxExpanded {
			return fmt.Errorf("%w: expanded size", ErrLimitExceeded)
		}
		full := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		copied, copyErr := io.Copy(out, io.LimitReader(tr, hdr.Size))
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if copied != hdr.Size {
			return fmt.Errorf("extracted %s size mismatch", name)
		}
	}
	_ = os.RemoveAll(dest)
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	return nil
}

// ReadTarZstMember extracts one regular file from a compressed archive into
// memory. The rest of the tree is never written to disk.
func ReadTarZstMember(src io.Reader, name string, limits Limits) ([]byte, error) {
	want, err := SanitizeArchiveMember(name)
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(src)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	tr := tar.NewReader(dec)
	var files int
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("%w: tar type %v", ErrUnsupportedEntry, hdr.Typeflag)
		}
		member, err := SanitizeArchiveMember(hdr.Name)
		if err != nil {
			return nil, err
		}
		files++
		if limits.MaxFiles > 0 && files > limits.MaxFiles {
			return nil, fmt.Errorf("%w: more than %d files", ErrLimitExceeded, limits.MaxFiles)
		}
		if member != want {
			if _, err := io.Copy(io.Discard, io.LimitReader(tr, hdr.Size+1)); err != nil {
				return nil, err
			}
			continue
		}
		if limits.MaxExpanded > 0 && hdr.Size > limits.MaxExpanded {
			return nil, fmt.Errorf("%w: expanded size", ErrLimitExceeded)
		}
		body, err := io.ReadAll(io.LimitReader(tr, hdr.Size+1))
		if err != nil {
			return nil, err
		}
		if int64(len(body)) != hdr.Size {
			return nil, fmt.Errorf("extracted %s size mismatch", member)
		}
		return body, nil
	}
	return nil, os.ErrNotExist
}

var historyCalls = [][]byte{[]byte("pushState"), []byte("replaceState")}

// UsesHistoryAPI reports whether any page or script in the archive calls
// history.pushState/replaceState, which every client-side router does.
func UsesHistoryAPI(src io.Reader, limits Limits) (bool, error) {
	dec, err := zstd.NewReader(src)
	if err != nil {
		return false, err
	}
	defer dec.Close()
	tr := tar.NewReader(dec)
	var files int
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		files++
		if limits.MaxFiles > 0 && files > limits.MaxFiles {
			return false, fmt.Errorf("%w: more than %d files", ErrLimitExceeded, limits.MaxFiles)
		}
		switch strings.ToLower(path.Ext(hdr.Name)) {
		case ".js", ".mjs", ".cjs", ".html", ".htm":
		default:
			continue
		}
		if found, err := containsAny(io.LimitReader(tr, hdr.Size), historyCalls); err != nil || found {
			return found, err
		}
	}
}

func containsAny(r io.Reader, needles [][]byte) (bool, error) {
	overlap := 0
	for _, needle := range needles {
		overlap = max(overlap, len(needle)-1)
	}
	buf := make([]byte, 64<<10)
	kept := 0
	for {
		n, err := r.Read(buf[kept:])
		chunk := buf[:kept+n]
		for _, needle := range needles {
			if bytes.Contains(chunk, needle) {
				return true, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		kept = copy(buf, chunk[max(0, len(chunk)-overlap):])
	}
}
