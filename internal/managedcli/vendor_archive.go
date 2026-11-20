package managedcli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"
)

const (
	maxExpandedVendorArchive = 1 << 30
	maxVendorMembers         = 20000
)

func extractVendorArchive(ctx context.Context, provider, archive, dest string) error {
	src, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer src.Close()
	zr, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	if err := os.Mkdir(dest, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	expanded := &io.LimitedReader{R: zr, N: maxExpandedVendorArchive + (maxVendorMembers+1)*1024}
	reader := tar.NewReader(expanded)
	var total int64
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := reader.Next()
		if err == io.EOF {
			_, err = io.Copy(io.Discard, expanded) // finish the gzip checksum check
			if expanded.N == 0 {
				return errors.New("expanded vendor archive exceeds size limit")
			}
			return err
		}
		if err != nil {
			return err
		}
		if i >= maxVendorMembers || h.Size < 0 || h.Size > maxExpandedVendorArchive-total {
			return errors.New("expanded vendor archive exceeds size limit")
		}
		total += h.Size
		name := path.Clean(h.Name)
		if provider == "agy" {
			if name != "antigravity" {
				continue
			}
			if h.Typeflag != tar.TypeReg {
				return errors.New("Antigravity archive entry is not a regular file")
			}
			name = "agy"
		} else {
			if name == "dist-package" && h.Typeflag == tar.TypeDir {
				continue
			}
			var ok bool
			name, ok = strings.CutPrefix(name, "dist-package/")
			if !ok {
				return errors.New("Cursor archive entry escapes its distribution directory")
			}
		}
		if err := extractVendorMember(root, reader, name, h); err != nil {
			return err
		}
	}
}

func extractVendorMember(root *os.Root, reader io.Reader, name string, h *tar.Header) error {
	if h.Typeflag == tar.TypeDir {
		return root.MkdirAll(name, 0700)
	}
	if err := root.MkdirAll(path.Dir(name), 0700); err != nil {
		return err
	}
	switch h.Typeflag {
	case tar.TypeReg:
		mode := os.FileMode(0600)
		if h.Mode&0111 != 0 {
			mode |= 0100
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, err = io.CopyN(file, reader, h.Size)
		return errors.Join(err, file.Close())
	case tar.TypeSymlink:
		target := path.Join(path.Dir(name), h.Linkname)
		if path.IsAbs(h.Linkname) || target == ".." || strings.HasPrefix(target, "../") {
			return errors.New("vendor archive symlink escapes its installation")
		}
		return root.Symlink(h.Linkname, name)
	default:
		return errors.New("unsupported vendor archive entry type")
	}
}
