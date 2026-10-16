package repo

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PreviewsRepo struct {
	*deps
}

func previewFromRow(row runPreviewRow) domain.RunPreview {
	p := domain.RunPreview{
		ID: row.ID, RunID: row.RunID, ProjectID: row.ProjectID, Kind: row.Kind, Status: row.Status,
		SourcePath: row.SourcePath, StorageKey: row.StorageKey, ContentSHA256: row.ContentSHA256,
		CompressedSize: row.CompressedSize, ExpandedSize: row.ExpandedSize, FileCount: row.FileCount,
		EntryFile: row.EntryFile, SPAFallback: row.SPAFallback, BackendURL: row.BackendURL, OwnerRunnerID: row.OwnerRunnerID,
		ScreenshotKey: row.ScreenshotKey, ErrorText: row.ErrorText, UploadTokenHash: row.UploadTokenHash,
		UploadMaxBytes: row.UploadMaxBytes,
	}
	if row.UploadExpiresAt != nil {
		p.UploadExpiresAt = *row.UploadExpiresAt
	}
	if row.ArchiveLeaseUntil != nil {
		p.ArchiveLeaseUntil = *row.ArchiveLeaseUntil
	}
	if row.LastAccessedAt != nil {
		p.LastAccessedAt = *row.LastAccessedAt
	}
	if row.ExpiresAt != nil {
		p.ExpiresAt = *row.ExpiresAt
	}
	if row.Created != nil {
		p.CreatedAt = *row.Created
	}
	if row.Updated != nil {
		p.UpdatedAt = *row.Updated
	}
	return p
}

func previewRowValues(p domain.RunPreview) map[string]any {
	return map[string]any{
		"id": p.ID, "run_id": p.RunID, "project_id": p.ProjectID, "kind": p.Kind, "status": p.Status,
		"source_path": p.SourcePath, "storage_key": p.StorageKey, "content_sha256": p.ContentSHA256,
		"compressed_size": p.CompressedSize, "expanded_size": p.ExpandedSize, "file_count": p.FileCount,
		"entry_file": p.EntryFile, "spa_fallback": p.SPAFallback, "backend_url": p.BackendURL, "owner_runner_id": p.OwnerRunnerID,
		"screenshot_key": p.ScreenshotKey, "error_text": p.ErrorText, "upload_token_hash": p.UploadTokenHash,
		"upload_max_bytes": p.UploadMaxBytes, "upload_expires_at": timePtr(p.UploadExpiresAt),
		"archive_lease_until": timePtr(p.ArchiveLeaseUntil),
		"last_accessed_at":    timePtr(p.LastAccessedAt), "expires_at": timePtr(p.ExpiresAt),
		"created_at": timePtr(p.CreatedAt), "updated_at": timePtr(p.UpdatedAt),
	}
}

func (r *PreviewsRepo) PutPreview(p domain.RunPreview) error {
	if strings.TrimSpace(p.ID) == "" {
		p.ID = NewPreviewID(p.RunID)
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	return upsert(r.DB, "run_previews", previewRowValues(p))
}

// ClaimPreviewArchive atomically takes the local/reconcile archive lease.
// Only one caller can hold a live lease; expired archiving rows may be reclaimed.
func (r *PreviewsRepo) ClaimPreviewArchive(id string, now, until time.Time) (domain.RunPreview, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.RunPreview{}, false, nil
	}
	now = now.UTC()
	until = until.UTC()
	result := r.DB.Model(&runPreviewRow{}).
		Where("id = ? AND (status = ? OR (status = ? AND (archive_lease_until IS NULL OR archive_lease_until <= ?)))",
			id, domain.PreviewPending, domain.PreviewArchiving, now).
		Updates(map[string]any{
			"status":              domain.PreviewArchiving,
			"error_text":          "",
			"archive_lease_until": timePtr(until),
			"updated_at":          now,
		})
	if result.Error != nil {
		return domain.RunPreview{}, false, result.Error
	}
	if result.RowsAffected != 1 {
		return domain.RunPreview{}, false, nil
	}
	preview, ok := r.Preview(id)
	return preview, ok, nil
}

// MarkPreviewReadyIfArchiving writes the ready payload only from archiving.
// A concurrent Fail or delete therefore cannot be overwritten by a stale success.
func (r *PreviewsRepo) MarkPreviewReadyIfArchiving(p domain.RunPreview) (bool, error) {
	now := time.Now().UTC()
	p.Status = domain.PreviewReady
	p.UpdatedAt = now
	p.ArchiveLeaseUntil = time.Time{}
	p.ErrorText = ""
	p.UploadTokenHash = ""
	values := previewRowValues(p)
	delete(values, "id")
	delete(values, "run_id")
	delete(values, "created_at")
	result := r.DB.Model(&runPreviewRow{}).
		Where("id = ? AND status = ?", p.ID, domain.PreviewArchiving).
		Updates(values)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// FailPreviewIfOpen records a failure only while the preview is still pending
// or archiving. Ready/expired/deleted rows stay untouched.
func (r *PreviewsRepo) FailPreviewIfOpen(id, errText string, now time.Time) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	now = now.UTC()
	result := r.DB.Model(&runPreviewRow{}).
		Where("id = ? AND status IN ?", id, []string{domain.PreviewPending, domain.PreviewArchiving}).
		Updates(map[string]any{
			"status":              domain.PreviewFailed,
			"error_text":          errText,
			"upload_token_hash":   "",
			"archive_lease_until": nil,
			"updated_at":          now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (r *PreviewsRepo) Preview(id string) (domain.RunPreview, bool) {
	var row runPreviewRow
	if err := r.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.RunPreview{}, false
	}
	return previewFromRow(row), true
}

func (r *PreviewsRepo) PreviewByRun(runID string) (domain.RunPreview, bool) {
	var row runPreviewRow
	if err := r.DB.Where("run_id = ?", runID).Take(&row).Error; err != nil {
		return domain.RunPreview{}, false
	}
	return previewFromRow(row), true
}

func (r *PreviewsRepo) PreviewsByStatus(status string) []domain.RunPreview {
	var rows []runPreviewRow
	if err := r.DB.Where("status = ?", status).Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.RunPreview, 0, len(rows))
	for _, row := range rows {
		out = append(out, previewFromRow(row))
	}
	return out
}

func (r *PreviewsRepo) ExpiredReadyPreviews(now time.Time) []domain.RunPreview {
	var rows []runPreviewRow
	if err := r.DB.Where("status = ? AND expires_at IS NOT NULL AND expires_at <= ?", domain.PreviewReady, now).
		Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.RunPreview, 0, len(rows))
	for _, row := range rows {
		out = append(out, previewFromRow(row))
	}
	return out
}

func (r *PreviewsRepo) ReadyPreviewsByStorageKey(key string) []domain.RunPreview {
	var rows []runPreviewRow
	if err := r.DB.Where("storage_key = ? AND status = ?", key, domain.PreviewReady).Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.RunPreview, 0, len(rows))
	for _, row := range rows {
		out = append(out, previewFromRow(row))
	}
	return out
}

func (r *PreviewsRepo) MarkPreviewAccessed(id string, now time.Time) error {
	return r.DB.Model(&runPreviewRow{}).Where("id = ?", id).Updates(map[string]any{
		"last_accessed_at": now, "updated_at": now,
	}).Error
}

func (r *PreviewsRepo) UpdatePreviewBackendURL(id, backendURL string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("empty preview id")
	}
	backendURL = strings.TrimSpace(backendURL)
	now := time.Now().UTC()
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&runPreviewRow{}).
			Where("id = ?", id).
			Updates(map[string]any{
				"backend_url": backendURL,
				"updated_at":  now,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&previewSessionRow{}).
			Where("preview_id = ? AND revoked_at IS NULL", id).
			Update("backend_url", backendURL).Error
	})
}

func (r *PreviewsRepo) SetRunWorkspaceCleanupHold(runID string, until time.Time) error {
	return r.DB.Model(&runRow{}).Where("id = ?", runID).Update("workspace_cleanup_not_before", timePtr(until)).Error
}

func (r *PreviewsRepo) ClearRunWorkspaceCleanupHold(runID string) error {
	return r.DB.Model(&runRow{}).Where("id = ?", runID).Update("workspace_cleanup_not_before", nil).Error
}

func sessionFromRow(row previewSessionRow) domain.PreviewSession {
	s := domain.PreviewSession{ID: row.ID, PreviewID: row.PreviewID, TokenHash: row.TokenHash, BackendURL: row.BackendURL}
	if row.ExpiresAt != nil {
		s.ExpiresAt = *row.ExpiresAt
	}
	if row.Created != nil {
		s.CreatedAt = *row.Created
	}
	if row.LastAccessedAt != nil {
		s.LastAccessedAt = *row.LastAccessedAt
	}
	if row.RevokedAt != nil {
		s.RevokedAt = *row.RevokedAt
	}
	return s
}

func (r *PreviewsRepo) PutPreviewSession(s domain.PreviewSession) error {
	if strings.TrimSpace(s.ID) == "" {
		raw := make([]byte, 8)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		s.ID = "ps-" + hex.EncodeToString(raw)
	}
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	return upsert(r.DB, "preview_sessions", map[string]any{
		"id": s.ID, "preview_id": s.PreviewID, "token_hash": s.TokenHash, "backend_url": s.BackendURL,
		"expires_at": timePtr(s.ExpiresAt), "created_at": timePtr(s.CreatedAt),
		"last_accessed_at": timePtr(s.LastAccessedAt), "revoked_at": timePtr(s.RevokedAt),
	})
}

func (r *PreviewsRepo) PreviewSessionByTokenHash(hash string) (domain.PreviewSession, bool) {
	var row previewSessionRow
	if err := r.DB.Where("token_hash = ?", hash).Take(&row).Error; err != nil {
		return domain.PreviewSession{}, false
	}
	return sessionFromRow(row), true
}

func (r *PreviewsRepo) PreviewSession(id string) (domain.PreviewSession, bool) {
	var row previewSessionRow
	if err := r.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.PreviewSession{}, false
	}
	return sessionFromRow(row), true
}

func (r *PreviewsRepo) RevokePreviewSession(id string, now time.Time) error {
	return r.DB.Model(&previewSessionRow{}).Where("id = ?", id).Updates(map[string]any{
		"revoked_at": now,
	}).Error
}

func (r *PreviewsRepo) TouchPreviewSession(id string, expiresAt, accessedAt time.Time) error {
	return r.DB.Model(&previewSessionRow{}).Where("id = ?", id).Updates(map[string]any{
		"expires_at": timePtr(expiresAt), "last_accessed_at": timePtr(accessedAt),
	}).Error
}

func (r *PreviewsRepo) DeletePreview(id string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("preview_id = ?", id).Delete(&previewSessionRow{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&runPreviewRow{}).Error
	})
}

func NewPreviewID(runID string) string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "preview"
	}
	return "preview-" + runID
}

func (r *PreviewsRepo) CountReadyStorageKey(key string) (int64, error) {
	var n int64
	err := r.DB.Model(&runPreviewRow{}).Where("storage_key = ? AND status = ?", key, domain.PreviewReady).Count(&n).Error
	return n, err
}

func (r *PreviewsRepo) PutPreviewBlob(sha256Hex string, content []byte) error {
	sha256Hex = strings.ToLower(strings.TrimSpace(sha256Hex))
	if len(sha256Hex) != 64 {
		return fmt.Errorf("invalid preview blob sha256")
	}
	now := time.Now().UTC()
	row := previewBlobRow{SHA256: sha256Hex, Content: content, Size: int64(len(content)), Created: &now}
	return r.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

func (r *PreviewsRepo) PreviewBlob(sha256Hex string) ([]byte, int64, bool) {
	var row previewBlobRow
	if err := r.DB.Where("sha256 = ?", strings.ToLower(strings.TrimSpace(sha256Hex))).Take(&row).Error; err != nil {
		return nil, 0, false
	}
	return row.Content, row.Size, true
}

func (r *PreviewsRepo) DeletePreviewBlob(sha256Hex string) error {
	return r.DB.Where("sha256 = ?", strings.ToLower(strings.TrimSpace(sha256Hex))).Delete(&previewBlobRow{}).Error
}

func PreviewNotFound(runID string) error {
	return fmt.Errorf("preview for run %s not found", runID)
}
