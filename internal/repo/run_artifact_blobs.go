package repo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

const artifactBlobChunkBytes = 4 << 20

type runArtifactBlobRow struct {
	RunID  string `gorm:"primaryKey;size:64"`
	ID     string `gorm:"primaryKey;size:64"`
	Size   int64
	Digest string `gorm:"size:64"`
}

func (runArtifactBlobRow) TableName() string { return "run_artifact_blobs" }

type runArtifactBlobChunk struct {
	RunID    string `gorm:"primaryKey;size:64"`
	BlobID   string `gorm:"primaryKey;size:64"`
	Sequence int    `gorm:"primaryKey;autoIncrement:false"`
	Data     []byte `gorm:"type:mediumblob"`
}

func (runArtifactBlobChunk) TableName() string { return "run_artifact_blob_chunks" }

func (r *RunsRepo) SaveArtifactBlob(ctx context.Context, runID, id string, source io.Reader) (domain.ArtifactBlob, error) {
	var blob domain.ArtifactBlob
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		blob, err = saveArtifactBlob(tx, runID, id, source)
		return err
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return r.ArtifactBlob(ctx, runID, id)
	}
	return blob, err
}

// The manifest and chunks commit together. Upload retries can find the same
// immutable file when a completion response was lost.
func saveArtifactBlob(tx *gorm.DB, runID, id string, source io.Reader) (domain.ArtifactBlob, error) {
	var row runArtifactBlobRow
	err := tx.Where("run_id = ? AND id = ?", runID, id).Take(&row).Error
	if err == nil {
		return row.blob(), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ArtifactBlob{}, err
	}
	row = runArtifactBlobRow{RunID: runID, ID: id}
	if err := tx.Create(&row).Error; err != nil {
		return domain.ArtifactBlob{}, err
	}
	hash := sha256.New()
	buf := bytes.NewBuffer(make([]byte, 0, artifactBlobChunkBytes))
	source = io.LimitReader(source, domain.MaxSOPArtifactBytes+1)
	for seq := 0; ; seq++ {
		n, err := io.Copy(buf, io.LimitReader(source, artifactBlobChunkBytes))
		if err != nil {
			return domain.ArtifactBlob{}, err
		}
		row.Size += n
		if row.Size > domain.MaxSOPArtifactBytes {
			return domain.ArtifactBlob{}, domain.ErrArtifactTooLarge
		}
		if n > 0 {
			if err := tx.Create(&runArtifactBlobChunk{RunID: runID, BlobID: id, Sequence: seq, Data: buf.Bytes()}).Error; err != nil {
				return domain.ArtifactBlob{}, err
			}
			_, _ = hash.Write(buf.Bytes())
			buf.Reset()
		}
		if n < artifactBlobChunkBytes {
			break
		}
	}
	row.Digest = hex.EncodeToString(hash.Sum(nil))
	if err := tx.Model(&row).Updates(map[string]any{"size": row.Size, "digest": row.Digest}).Error; err != nil {
		return domain.ArtifactBlob{}, err
	}
	return row.blob(), nil
}

func (row runArtifactBlobRow) blob() domain.ArtifactBlob {
	return domain.ArtifactBlob{ID: row.ID, Size: row.Size, SHA256: row.Digest}
}

func (r *RunsRepo) ArtifactBlob(ctx context.Context, runID, id string) (domain.ArtifactBlob, error) {
	var row runArtifactBlobRow
	err := r.DB.WithContext(ctx).Where("run_id = ? AND id = ?", runID, id).Take(&row).Error
	return row.blob(), err
}

func (r *RunsRepo) OpenArtifactBlob(ctx context.Context, runID, id string) (domain.ArtifactBlob, io.ReadCloser, error) {
	blob, err := r.ArtifactBlob(ctx, runID, id)
	if err != nil {
		return blob, nil, err
	}
	rows, err := r.DB.WithContext(ctx).Model(&runArtifactBlobChunk{}).Select("data").
		Where("run_id = ? AND blob_id = ?", runID, id).Order("sequence").Rows()
	if err != nil {
		return blob, nil, err
	}
	return blob, &artifactBlobReader{rows: rows, remaining: blob.Size}, nil
}

type artifactBlobReader struct {
	rows      *sql.Rows
	chunk     []byte
	remaining int64
}

func (r *artifactBlobReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.chunk) == 0 {
		if !r.rows.Next() {
			if err := r.rows.Err(); err != nil {
				return 0, err
			}
			if r.remaining != 0 {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, io.EOF
		}
		if err := r.rows.Scan(&r.chunk); err != nil {
			return 0, err
		}
		if len(r.chunk) == 0 || int64(len(r.chunk)) > r.remaining {
			return 0, errors.New("invalid artifact chunk size")
		}
	}
	n := copy(p, r.chunk)
	r.chunk = r.chunk[n:]
	r.remaining -= int64(n)
	return n, nil
}

func (r *artifactBlobReader) Close() error { return r.rows.Close() }
