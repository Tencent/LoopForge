package preview

import (
	"time"

	"agentflow/internal/domain"
)

// Repository is the persistence the preview service needs; *repo.Store
// satisfies it. Depending on an interface keeps the database layer (gorm and
// the SQLite driver) out of agentflow-runner, which only uses ArchiveDir and
// is cross-compiled for every execution-machine platform.
type Repository interface {
	BlobRepository
	PutPreview(p domain.RunPreview) error
	ClaimPreviewArchive(id string, now, until time.Time) (domain.RunPreview, bool, error)
	MarkPreviewReadyIfArchiving(p domain.RunPreview) (bool, error)
	FailPreviewIfOpen(id, errText string, now time.Time) (bool, error)
	Preview(id string) (domain.RunPreview, bool)
	PreviewByRun(runID string) (domain.RunPreview, bool)
	PreviewsByStatus(status string) []domain.RunPreview
	ExpiredReadyPreviews(now time.Time) []domain.RunPreview
	MarkPreviewAccessed(id string, now time.Time) error
	UpdatePreviewBackendURL(id, backendURL string) error
	SetRunWorkspaceCleanupHold(runID string, until time.Time) error
	ClearRunWorkspaceCleanupHold(runID string) error
	PutPreviewSession(s domain.PreviewSession) error
	PreviewSessionByTokenHash(hash string) (domain.PreviewSession, bool)
	RevokePreviewSession(id string, now time.Time) error
	TouchPreviewSession(id string, expiresAt, accessedAt time.Time) error
	DeletePreview(id string) error
	CountReadyStorageKey(key string) (int64, error)
	Run(id string) (domain.Run, bool)
}

// BlobRepository stores content-addressed preview bundles.
type BlobRepository interface {
	PutPreviewBlob(sha256Hex string, content []byte) error
	PreviewBlob(sha256Hex string) ([]byte, int64, bool)
	DeletePreviewBlob(sha256Hex string) error
}
