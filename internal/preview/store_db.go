package preview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"agentflow/internal/repo"
)

// DBStore keeps tar.zst blobs in MySQL (sqlite in tests). Serving reads the
// blob back out and extracts the requested member in memory; no shared disk.
type DBStore struct {
	repo *repo.Store
}

func NewDBStore(st *repo.Store) *DBStore {
	return &DBStore{repo: st}
}

func (s *DBStore) Put(ctx context.Context, key string, src io.Reader, size int64, sha256Hex string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var (
		data []byte
		err  error
	)
	if size > 0 {
		data, err = io.ReadAll(io.LimitReader(src, size+1))
	} else {
		data, err = io.ReadAll(src)
	}
	if err != nil {
		return fmt.Errorf("read preview blob: %w", err)
	}
	if size > 0 && int64(len(data)) != size {
		return fmt.Errorf("preview object size mismatch: got %d want %d", len(data), size)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if sha256Hex != "" && !strings.EqualFold(got, sha256Hex) {
		return errors.New("preview object sha256 mismatch")
	}
	_ = key
	return s.repo.PutPreviewBlob(ContentKey(got), data)
}

func (s *DBStore) Open(ctx context.Context, key string) (io.ReadSeekCloser, Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, Object{}, err
	}
	data, size, ok := s.repo.PreviewBlob(ContentKey(key))
	if !ok {
		return nil, Object{}, fmt.Errorf("preview blob %s not found", key)
	}
	return memBlob{bytes.NewReader(data)}, Object{Key: key, SHA256: ContentKey(key), Size: size}, nil
}

func (s *DBStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.repo.DeletePreviewBlob(ContentKey(key))
}
