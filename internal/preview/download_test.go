package preview

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/klauspost/compress/zstd"
)

func TestDownloadZIPPreservesBuildFiles(t *testing.T) {
	limits := Limits{MaxFiles: 20, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20}
	files := map[string]string{
		"index.html":      `<script src="/assets/app.js"></script>`,
		"assets/app.js":   `fetch("/api/items")`,
		"assets/app.css":  "body{color:red}",
		"images/logo.bin": "\x00\xff\x01",
	}
	var out bytes.Buffer
	if err := writeDownloadZIP(context.Background(), bytes.NewReader(archiveOf(t, limits, files)), "index.html", limits, &out); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != len(files) {
		t.Fatalf("ZIP files = %d, want %d", len(zr.File), len(files))
	}
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		_ = r.Close()
		want, exists := files[file.Name]
		if err != nil || !exists || string(body) != want {
			t.Fatalf("ZIP file %s changed: %q, %v", file.Name, body, err)
		}
	}
}

func TestDownloadZIPRejectsInvalidBundles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []tar.Header
		limits  Limits
		want    error
	}{
		{"traversal", []tar.Header{{Name: "../index.html", Typeflag: tar.TypeReg}}, Limits{}, ErrUnsafePath},
		{"symlink", []tar.Header{{Name: "index.html", Typeflag: tar.TypeSymlink, Linkname: "secret"}}, Limits{}, ErrUnsupportedEntry},
		{"hardlink", []tar.Header{{Name: "index.html", Typeflag: tar.TypeLink, Linkname: "secret"}}, Limits{}, ErrUnsupportedEntry},
		{"file count", []tar.Header{{Name: "index.html"}, {Name: "app.js"}}, Limits{MaxFiles: 1}, ErrLimitExceeded},
		{"expanded size", []tar.Header{{Name: "index.html", Size: 2}}, Limits{MaxExpanded: 1}, ErrLimitExceeded},
		{"missing index", []tar.Header{{Name: "app.js"}}, Limits{}, ErrMissingIndex},
		{"duplicate file", []tar.Header{{Name: "index.html"}, {Name: "index.html"}}, Limits{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var archive bytes.Buffer
			enc, err := zstd.NewWriter(&archive)
			if err != nil {
				t.Fatal(err)
			}
			tw := tar.NewWriter(enc)
			for _, hdr := range tc.headers {
				if err := tw.WriteHeader(&hdr); err != nil {
					t.Fatal(err)
				}
				if hdr.Size > 0 {
					if _, err := tw.Write(make([]byte, hdr.Size)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := enc.Close(); err != nil {
				t.Fatal(err)
			}
			limits := tc.limits.withDefaults(Config{MaxFiles: 20, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20})
			err = writeDownloadZIP(context.Background(), &archive, "index.html", limits, io.Discard)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	if err := writeDownloadZIP(context.Background(), bytes.NewBufferString("not a bundle"), "index.html", Limits{}, io.Discard); err == nil {
		t.Fatal("corrupt bundle accepted")
	}
}

func TestDownloadClosesTemporaryFileAndHonorsCancellation(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	limits := Limits{MaxFiles: 20, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20}
	blob := archiveOf(t, limits, map[string]string{"index.html": "hello"})
	db := NewDBStore(store)
	if err := db.Put(context.Background(), "", bytes.NewReader(blob), int64(len(blob)), ""); err != nil {
		t.Fatal(err)
	}
	result, err := InspectTarZst(bytes.NewReader(blob), "index.html", limits)
	if err != nil {
		t.Fatal(err)
	}
	p := domain.RunPreview{ID: "download", RunID: "download", Status: domain.PreviewReady, StorageKey: result.StorageKey, EntryFile: "index.html"}
	if err := store.PutPreview(p); err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, db, Config{})
	download, err := svc.OpenDownload(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	name := download.(*previewDownload).Name()
	if err := download.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary ZIP survived Close: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.OpenDownload(ctx, p); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled download: %v", err)
	}
	p.ExpiresAt = time.Now().Add(-time.Second)
	if _, err := svc.OpenDownload(context.Background(), p); !errors.Is(err, ErrPreviewExpired) {
		t.Fatalf("expired download: %v", err)
	}
}
