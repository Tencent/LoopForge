package preview

import (
	"bytes"
	"context"
	"io"
	"strings"
	"time"
)

// Object is the metadata returned when opening a stored preview bundle.
type Object struct {
	Key    string
	SHA256 string
	Size   int64
}

// Store persists compressed preview bundles. The production implementation
// keeps tar.zst in MySQL so any API instance can serve the same Run.
type Store interface {
	Put(ctx context.Context, key string, src io.Reader, size int64, sha256 string) error
	Open(ctx context.Context, key string) (io.ReadSeekCloser, Object, error)
	Delete(ctx context.Context, key string) error
}

type memBlob struct{ *bytes.Reader }

func (memBlob) Close() error { return nil }

// ContentKey is the content-addressed blob id: lowercase SHA-256 hex.
func ContentKey(sha256Hex string) string {
	return strings.ToLower(strings.TrimSpace(sha256Hex))
}

// Limits bound a single archive. Zero values fall back to Config.
type Limits struct {
	MaxFiles      int
	MaxCompressed int64
	MaxExpanded   int64
}

func (l Limits) withDefaults(cfg Config) Limits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = cfg.MaxFiles
	}
	if l.MaxCompressed <= 0 {
		l.MaxCompressed = cfg.MaxCompressed
	}
	if l.MaxExpanded <= 0 {
		l.MaxExpanded = cfg.MaxExpanded
	}
	return l
}

// Result is the outcome of a successful static archive.
type Result struct {
	StorageKey     string
	SHA256         string
	CompressedSize int64
	ExpandedSize   int64
	FileCount      int
	EntryFile      string
}

// Clock is injectable so hold/TTL tests do not sleep.
type Clock func() time.Time

func utcNow() time.Time { return time.Now().UTC() }
