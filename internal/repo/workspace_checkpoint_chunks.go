package repo

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"time"

	"gorm.io/gorm"
)

const workspaceCheckpointChunkBytes = 4 << 20
const workspaceCheckpointArchiveBytes = 512 << 20
const workspaceCheckpointInlineBytes = 12 << 20

type workspaceCheckpointChunk struct {
	RunID     string `gorm:"primaryKey;size:64"`
	ArchiveID string `gorm:"primaryKey;size:64"`
	Sequence  int    `gorm:"primaryKey"`
	Data      []byte `gorm:"type:mediumblob"`
	CreatedAt time.Time
}

func (workspaceCheckpointChunk) TableName() string { return "run_workspace_checkpoint_chunks" }

// Stage bounded rows, then publish the manifest by lease CAS. A failed save
// cannot replace the previous archive. Crashed staging rows expire on the next
// save; deleting a run also deletes all its chunks.
func (r *RunsRepo) SaveWorkspaceCheckpointStream(runID, token string, now time.Time, source io.Reader) error {
	// Keep the legacy representation for small archives, including during a
	// rolling upgrade. The lookahead is bounded independently of archive size.
	prefix, err := io.ReadAll(io.LimitReader(source, workspaceCheckpointInlineBytes+1))
	if err != nil {
		return err
	}
	if len(prefix) <= workspaceCheckpointInlineBytes {
		return r.SaveWorkspaceCheckpoint(runID, token, now, prefix)
	}
	source = io.MultiReader(bytes.NewReader(prefix), source)
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	id := hex.EncodeToString(raw)
	published := false
	defer func() {
		if !published {
			r.DB.Where("run_id = ? AND archive_id = ?", runID, id).Delete(&workspaceCheckpointChunk{})
		}
	}()
	if err := r.DB.Where("run_id = ? AND created_at < ? AND archive_id NOT IN (?)", runID, time.Now().UTC().Add(-time.Hour),
		r.DB.Model(&WorkspaceCheckpoint{}).Select("COALESCE(archive_id, '')").Where("run_id = ?", runID)).Delete(&workspaceCheckpointChunk{}).Error; err != nil {
		return err
	}
	hash := sha256.New()
	var size int64
	count := 0
	buffer := make([]byte, workspaceCheckpointChunkBytes)
	for {
		n, err := io.ReadFull(source, buffer)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		if n == 0 {
			break
		}
		size += int64(n)
		if size > workspaceCheckpointArchiveBytes {
			return fmt.Errorf("workspace checkpoint exceeds archive size limit")
		}
		var active int64
		if err := r.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_until > ?", runID, token, time.Now().UTC()).Count(&active).Error; err != nil {
			return err
		}
		if active != 1 {
			return ErrWorkspaceInUse
		}
		chunk := workspaceCheckpointChunk{RunID: runID, ArchiveID: id, Sequence: count, Data: buffer[:n], CreatedAt: time.Now().UTC()}
		if err := r.DB.Create(&chunk).Error; err != nil {
			return err
		}
		hash.Write(buffer[:n])
		count++
		if err == io.ErrUnexpectedEOF {
			break
		}
	}
	if size == 0 {
		return fmt.Errorf("empty workspace checkpoint")
	}
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := r.takeRunForUpdate(tx, runID); err != nil {
			return err
		}
		result := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_until > ?", runID, token, time.Now().UTC()).Updates(map[string]any{
			"archive": nil, "archive_id": id, "archive_bytes": size, "archive_chunks": count,
			"sha256": hex.EncodeToString(hash.Sum(nil)), "saved_at": now, "error": "", "save_failures": 0, "save_retry_at": nil,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkspaceInUse
		}
		return tx.Where("run_id = ? AND archive_id <> ?", runID, id).Delete(&workspaceCheckpointChunk{}).Error
	})
	published = err == nil
	return err
}

func (r *RunsRepo) OpenWorkspaceCheckpointArchive(runID string) (io.ReadCloser, error) {
	var item WorkspaceCheckpoint
	if err := r.DB.Where("run_id = ?", runID).Take(&item).Error; err != nil {
		return nil, err
	}
	if item.ArchiveID == "" {
		if len(item.Archive) == 0 {
			return nil, fmt.Errorf("该任务尚无可恢复的工作区快照；未保存的历史文件无法恢复")
		}
		sum := sha256.Sum256(item.Archive)
		if hex.EncodeToString(sum[:]) != item.SHA256 {
			return nil, fmt.Errorf("工作区快照校验失败")
		}
		return io.NopCloser(bytes.NewReader(item.Archive)), nil
	}
	if item.ArchiveBytes <= 0 || item.ArchiveBytes > workspaceCheckpointArchiveBytes || item.ArchiveChunks != int((item.ArchiveBytes+workspaceCheckpointChunkBytes-1)/workspaceCheckpointChunkBytes) || len(item.SHA256) != 64 {
		return nil, fmt.Errorf("工作区快照校验失败：invalid manifest")
	}
	reader := &workspaceCheckpointReader{repo: r, manifest: item, hash: sha256.New()}
	return io.NopCloser(reader), nil
}

type workspaceCheckpointReader struct {
	repo     *RunsRepo
	manifest WorkspaceCheckpoint
	hash     hash.Hash
	err      error
	next     int
	read     int64
	buffer   []byte
}

func (r *workspaceCheckpointReader) Read(data []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if len(data) == 0 {
		return 0, nil
	}
	if len(r.buffer) == 0 {
		if r.read == r.manifest.ArchiveBytes {
			return 0, io.EOF
		}
		var chunk workspaceCheckpointChunk
		if err := r.repo.DB.Where("run_id = ? AND archive_id = ? AND sequence = ?", r.manifest.RunID, r.manifest.ArchiveID, r.next).Take(&chunk).Error; err != nil {
			return 0, fmt.Errorf("read checkpoint chunk %d: %w", r.next, err)
		}
		expected := min(int64(workspaceCheckpointChunkBytes), r.manifest.ArchiveBytes-r.read)
		if int64(len(chunk.Data)) != expected {
			return 0, fmt.Errorf("invalid checkpoint chunk size")
		}
		r.buffer = chunk.Data
		r.next++
	}
	n := copy(data, r.buffer)
	r.hash.Write(data[:n])
	r.buffer = r.buffer[n:]
	r.read += int64(n)
	if r.read == r.manifest.ArchiveBytes && hex.EncodeToString(r.hash.Sum(nil)) != r.manifest.SHA256 {
		r.err = fmt.Errorf("工作区快照校验失败")
		return n, r.err
	}
	return n, nil
}
