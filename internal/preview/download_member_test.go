package preview

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestDownloadEntryNameSkipsDirectories(t *testing.T) {
	name, skip, err := downloadEntryName(&tar.Header{Name: "assets/", Typeflag: tar.TypeDir})
	if err != nil || !skip || name != "" {
		t.Fatalf("dir = (%q, %v, %v), want skip", name, skip, err)
	}
}

func TestDownloadEntryNameRejectsNonRegularEntries(t *testing.T) {
	for _, hdr := range []*tar.Header{
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "index.html"},
		{Name: "hard", Typeflag: tar.TypeLink, Linkname: "index.html"},
		{Name: "fifo", Typeflag: tar.TypeFifo},
		{Name: "reg-with-link", Typeflag: tar.TypeReg, Linkname: "index.html"},
	} {
		name, skip, err := downloadEntryName(hdr)
		if err != ErrUnsupportedEntry || skip || name != "" {
			t.Fatalf("%s = (%q, %v, %v), want bare ErrUnsupportedEntry", hdr.Name, name, skip, err)
		}
	}
}

func TestDownloadEntryNameSanitizesRegularFiles(t *testing.T) {
	for _, typ := range []byte{tar.TypeReg, tar.TypeRegA} {
		name, skip, err := downloadEntryName(&tar.Header{Name: "./assets/app.js", Typeflag: typ})
		if err != nil || skip || name != "assets/app.js" {
			t.Fatalf("type %v = (%q, %v, %v), want assets/app.js", typ, name, skip, err)
		}
	}
	if _, _, err := downloadEntryName(&tar.Header{Name: "../escape.js", Typeflag: tar.TypeReg}); err == nil {
		t.Fatal("escaping member name was accepted")
	}
}

func TestCopyDownloadMemberWritesDeflatedMember(t *testing.T) {
	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	body := "body{color:red}"
	hdr := &tar.Header{Name: "assets/app.css", Size: int64(len(body)), ModTime: modified}
	if err := copyDownloadMember(context.Background(), zw, strings.NewReader(body), "assets/app.css", hdr); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("ZIP files = %d, want 1", len(zr.File))
	}
	file := zr.File[0]
	if file.Name != "assets/app.css" || file.Method != zip.Deflate || file.Mode().Perm() != 0o644 || !file.Modified.Equal(modified) {
		t.Fatalf("header = name %q method %d mode %v modified %v", file.Name, file.Method, file.Mode(), file.Modified)
	}
	r, err := file.Open()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(got) != body {
		t.Fatalf("content = %q, %v", got, err)
	}
}

func TestCopyDownloadMemberRejectsSizeMismatch(t *testing.T) {
	zw := zip.NewWriter(io.Discard)
	hdr := &tar.Header{Name: "index.html", Size: 10}
	err := copyDownloadMember(context.Background(), zw, strings.NewReader("short"), "index.html", hdr)
	if err == nil || err.Error() != `preview file "index.html" size mismatch` {
		t.Fatalf("err = %v, want size mismatch", err)
	}
}

func TestCopyDownloadMemberHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	zw := zip.NewWriter(io.Discard)
	hdr := &tar.Header{Name: "index.html", Size: 2}
	if err := copyDownloadMember(ctx, zw, strings.NewReader("ok"), "index.html", hdr); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
