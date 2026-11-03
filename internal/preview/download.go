package preview

import (
	"archive/tar"
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"agentflow/internal/domain"
	"github.com/klauspost/compress/zstd"
)

var (
	ErrPreviewNotReady = errors.New("preview is not ready for download")
	ErrPreviewExpired  = errors.New("preview has expired or been deleted")
)

// OpenDownload converts the stored bundle to ZIP without rebuilding the site
// or depending on its workspace. Close removes the temporary download file.
func (s *Service) OpenDownload(ctx context.Context, p domain.RunPreview) (io.ReadSeekCloser, error) {
	if p.Status == domain.PreviewExpired || p.Status == domain.PreviewDeleted || p.Status == domain.PreviewDeleting ||
		(!p.ExpiresAt.IsZero() && !p.ExpiresAt.After(s.clock())) {
		return nil, ErrPreviewExpired
	}
	if !p.Serveable() {
		return nil, ErrPreviewNotReady
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	src, obj, err := s.store.Open(ctx, bundleKey(p))
	if err != nil {
		return nil, err
	}
	defer src.Close()
	limits := s.limits().withDefaults(Config{
		MaxFiles: DefaultMaxFiles, MaxCompressed: DefaultMaxCompressed, MaxExpanded: DefaultMaxExpanded,
	})
	if obj.Size > limits.MaxCompressed {
		return nil, ErrLimitExceeded
	}
	tmp, err := os.OpenRoot(os.TempDir())
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp("", "agentflow-preview-download-*.zip")
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}
	download := &previewDownload{File: file, dir: tmp, name: filepath.Base(file.Name())}
	ok := false
	defer func() {
		if !ok {
			_ = download.Close()
		}
	}()
	if err := writeDownloadZIP(ctx, src, p.EntryFile, limits, file); err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	_ = s.repo.MarkPreviewAccessed(p.ID, s.clock())
	ok = true
	return download, nil
}

// previewDownload removes its file through a root on the temp directory, so
// removal can never leave that directory.
type previewDownload struct {
	*os.File
	dir  *os.Root
	name string
}

func (f *previewDownload) Close() error {
	return errors.Join(f.File.Close(), f.dir.Remove(f.name), f.dir.Close())
}

// writeDownloadZIP preserves original build files, including their relative
// paths. Preview session scripts and backend settings are never injected.
func writeDownloadZIP(ctx context.Context, src io.Reader, entryFile string, limits Limits, dst io.Writer) error {
	dec, err := zstd.NewReader(&downloadReader{ctx, src}, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return err
	}
	defer dec.Close()
	tr := tar.NewReader(dec)
	zw := zip.NewWriter(dst)
	seen := map[string]bool{}
	var expanded int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		if (hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA) || hdr.Linkname != "" {
			return ErrUnsupportedEntry
		}
		name, err := SanitizeArchiveMember(hdr.Name)
		if err != nil {
			return err
		}
		if seen[name] {
			return fmt.Errorf("duplicate preview file %q", name)
		}
		if len(seen) >= limits.MaxFiles || hdr.Size > limits.MaxExpanded-expanded {
			return ErrLimitExceeded
		}
		seen[name] = true
		expanded += hdr.Size
		zh := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: hdr.ModTime}
		zh.SetMode(0o644)
		member, err := zw.CreateHeader(zh)
		if err != nil {
			return err
		}
		n, err := io.Copy(member, &downloadReader{ctx, tr})
		if err != nil {
			return err
		}
		if n != hdr.Size {
			return fmt.Errorf("preview file %q size mismatch", name)
		}
	}
	if !seen[entryFile] {
		return ErrMissingIndex
	}
	if _, err := io.Copy(io.Discard, &downloadReader{ctx, dec}); err != nil {
		return err
	}
	return zw.Close()
}

type downloadReader struct {
	ctx context.Context
	src io.Reader
}

func (r *downloadReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.src.Read(p)
}
