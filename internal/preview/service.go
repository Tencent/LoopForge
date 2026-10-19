package preview

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// Host is the engine-facing callback surface. The preview package never
// imports engine, so finish/cleanup stay on the existing engine paths.
type Host interface {
	RunWorkspace(runID string) string
	// RunWorkspaceFacts reports what the run's execution machine found in
	// its workspace; ok is false when RunWorkspace must be inspected instead.
	RunWorkspaceFacts(runID string) (domain.RunnerWorkspaceFacts, bool)
	QueuePreviewArchive(run domain.Run, spec domain.RunnerPreviewArchiveSpec) error
	CancelPreviewArchive(runID string)
	ReleaseWorkspace(runID string)
	AppendRunEvent(runID, typ, msg string)
}

type Service struct {
	repo      *repo.Store
	store     Store
	cfg       Config
	host      Host
	now       Clock
	archiveMu sync.Mutex
	archiving map[string]struct{}
	routedMu  sync.Mutex
	routed    map[string]bool
}

func NewService(st *repo.Store, store Store, cfg Config) *Service {
	if store == nil {
		store = NewDBStore(st)
	}
	return &Service{repo: st, store: store, cfg: cfg, now: utcNow}
}

func (s *Service) SetHost(h Host) { s.host = h }

func (s *Service) Enabled() bool { return s != nil }

func (s *Service) Config() Config {
	if s == nil {
		return Config{}
	}
	return s.cfg
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return utcNow()
}

func (s *Service) limits() Limits {
	return Limits{MaxFiles: s.cfg.MaxFiles, MaxCompressed: s.cfg.MaxCompressed, MaxExpanded: s.cfg.MaxExpanded}
}

// BeginFinalization records a pending preview and holds workspace cleanup.
// It returns true when the caller must not delete the workspace yet.
// A preview that is already ready, skipped, or gone is left as it is.
func (s *Service) BeginFinalization(run domain.Run) bool {
	return s.beginFinalization(run, false)
}

// ArchiveBuiltPreview starts the static preview when the build node succeeds,
// including while a later human gate is still open. A new successful build
// replaces a finished bundle. An archive already in progress is left alone.
func (s *Service) ArchiveBuiltPreview(run domain.Run) bool {
	return s.beginFinalization(run, true)
}

func (s *Service) beginFinalization(run domain.Run, fromBuild bool) bool {
	if !s.Enabled() {
		return false
	}
	contract := run.WorkflowSnapshot.Preview
	if !contract.Static() {
		return false
	}
	if _, err := SanitizeRelPath(contract.SourcePath); err != nil {
		s.failNew(run, contract, err)
		return false
	}
	if _, err := SanitizeRelPath(contract.Index()); err != nil {
		s.failNew(run, contract, err)
		return false
	}
	now := s.clock()
	preview := domain.RunPreview{
		ID:            repo.NewPreviewID(run.ID),
		RunID:         run.ID,
		ProjectID:     run.WorkflowSnapshot.ProjectID,
		Kind:          contract.KindOrStatic(),
		Status:        domain.PreviewPending,
		SourcePath:    contract.SourcePath,
		EntryFile:     contract.Index(),
		SPAFallback:   contract.SPAFallback,
		OwnerRunnerID: strings.TrimSpace(run.ExecutionContext.ExecutionRunnerID),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	replacedKey := ""
	if existing, ok := s.repo.PreviewByRun(run.ID); ok {
		switch existing.Status {
		case domain.PreviewPending, domain.PreviewArchiving:
			return existing.ActiveHold()
		case domain.PreviewReady, domain.PreviewSkipped, domain.PreviewDeleted, domain.PreviewDeleting, domain.PreviewExpired:
			if !fromBuild {
				return false
			}
		}
		if existing.Status == domain.PreviewReady {
			replacedKey = existing.StorageKey
		}
		preview = existing
		preview.Status = domain.PreviewPending
		preview.ErrorText = ""
		preview.StorageKey = ""
		preview.ContentSHA256 = ""
		preview.CompressedSize = 0
		preview.ExpandedSize = 0
		preview.FileCount = 0
		preview.UploadTokenHash = ""
		preview.ArchiveLeaseUntil = time.Time{}
		preview.CreatedAt = now
		preview.UpdatedAt = now
	}
	// The build node already decided this workspace has a frontend. Runner
	// workspaces are not on the control plane, so a missing local checkout
	// must not turn a finished build into a skip.
	if !fromBuild && s.host != nil {
		skip := false
		if facts, ok := s.host.RunWorkspaceFacts(run.ID); ok {
			skip = !facts.Frontend
		} else {
			skip = s.shouldSkipNonFrontend(run, s.host.RunWorkspace(run.ID))
		}
		if skip {
			s.Skip(run, "工作区没有前端工程，已跳过预览")
			return false
		}
	}
	if err := s.repo.PutPreview(preview); err != nil {
		log.Printf("preview create %s: %v", run.ID, err)
		return false
	}
	if replacedKey != "" && s.store != nil {
		if n, err := s.repo.CountReadyStorageKey(replacedKey); err == nil && n == 0 {
			if err := s.store.Delete(context.Background(), replacedKey); err != nil {
				log.Printf("preview replace blob %s: %v", run.ID, err)
			}
		}
	}
	until := now.Add(s.cfg.FinalizationTimeout)
	if err := s.repo.SetRunWorkspaceCleanupHold(run.ID, until); err != nil {
		log.Printf("preview hold %s: %v", run.ID, err)
		return false
	}
	s.event(run.ID, "preview.archive.queued", "preview finalizer queued for "+contract.SourcePath)
	if s.host == nil {
		return true
	}
	if runnerID := strings.TrimSpace(run.ExecutionContext.ExecutionRunnerID); runnerID != "" && run.ExecutionContext.ExecutionMode == domain.ExecutionModeRunner {
		token, hash, err := newOpaqueToken()
		if err != nil {
			s.Fail(preview, err)
			return false
		}
		preview.Status = domain.PreviewArchiving
		preview.UploadTokenHash = hash
		preview.UploadMaxBytes = s.cfg.MaxCompressed
		preview.UploadExpiresAt = until
		preview.OwnerRunnerID = runnerID
		if err := s.repo.PutPreview(preview); err != nil {
			s.Fail(preview, err)
			return false
		}
		spec := domain.RunnerPreviewArchiveSpec{
			PreviewID:   preview.ID,
			RunID:       run.ID,
			SourcePath:  contract.SourcePath,
			IndexFile:   contract.Index(),
			SPAFallback: contract.SPAFallback,
			MaxFiles:    s.cfg.MaxFiles,
			MaxBytes:    s.cfg.MaxCompressed,
			UploadURL:   "/runners/previews/" + preview.ID + "/content",
			UploadToken: token,
		}
		if err := s.host.QueuePreviewArchive(run, spec); err != nil {
			s.Fail(preview, err)
			return false
		}
		s.event(run.ID, "preview.archive.started", "waiting for runner to archive "+contract.SourcePath)
		return true
	}
	go s.finalizeLocal(run, preview)
	return true
}

func (s *Service) tryLocalArchive(id string) bool {
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()
	if s.archiving == nil {
		s.archiving = map[string]struct{}{}
	}
	if _, ok := s.archiving[id]; ok {
		return false
	}
	s.archiving[id] = struct{}{}
	return true
}

func (s *Service) endLocalArchive(id string) {
	s.archiveMu.Lock()
	delete(s.archiving, id)
	s.archiveMu.Unlock()
}

func (s *Service) finalizeLocal(run domain.Run, preview domain.RunPreview) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.FinalizationTimeout)
	defer cancel()
	workspace := ""
	if s.host != nil {
		workspace = s.host.RunWorkspace(run.ID)
	}
	if err := s.ArchiveLocal(ctx, run, preview, workspace); err != nil && !errors.Is(err, ErrPreviewBusy) && !errors.Is(err, ErrPreviewGone) {
		s.Fail(preview, err)
	}
}

func (s *Service) ArchiveLocal(ctx context.Context, run domain.Run, preview domain.RunPreview, workspace string) error {
	if strings.TrimSpace(workspace) == "" {
		return errors.New("run workspace is not available for preview archive")
	}
	if !s.tryLocalArchive(preview.ID) {
		return ErrPreviewBusy
	}
	defer s.endLocalArchive(preview.ID)
	lease := s.cfg.FinalizationTimeout
	if lease <= 0 {
		lease = DefaultFinalizeAfter
	}
	live, claimed, err := s.repo.ClaimPreviewArchive(preview.ID, s.clock(), s.clock().Add(lease))
	if err != nil {
		return err
	}
	if !claimed {
		return ErrPreviewBusy
	}
	preview = live
	s.event(run.ID, "preview.archive.started", "archiving "+preview.SourcePath)
	tmp, err := os.CreateTemp("", "preview-*.tar.zst")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	result, err := ArchiveDir(workspace, preview.SourcePath, preview.EntryFile, s.limits(), tmp)
	if err != nil {
		if s.skipIfNonFrontend(run, workspace, err) {
			return nil
		}
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := s.store.Put(ctx, result.StorageKey, tmp, result.CompressedSize, result.SHA256); err != nil {
		return err
	}
	return s.markReady(preview, result, run)
}

func (s *Service) AcceptUpload(ctx context.Context, previewID, runnerID, token string, body io.Reader) error {
	preview, ok := s.repo.Preview(previewID)
	if !ok {
		return errors.New("preview not found")
	}
	if preview.OwnerRunnerID != "" && preview.OwnerRunnerID != runnerID {
		return errors.New("preview upload runner mismatch")
	}
	if preview.UploadTokenHash == "" || !tokenHashEqual(token, preview.UploadTokenHash) {
		return errors.New("preview upload token mismatch")
	}
	now := s.clock()
	if !preview.UploadExpiresAt.IsZero() && now.After(preview.UploadExpiresAt) {
		return errors.New("preview upload token expired")
	}
	if preview.Status != domain.PreviewPending && preview.Status != domain.PreviewArchiving {
		return errors.New("preview is not accepting uploads")
	}
	s.event(preview.RunID, "preview.archive.uploading", "receiving preview bundle")
	tmp, err := os.CreateTemp("", "preview-upload-*.tar.zst")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	limited := &countWriter{w: tmp, limit: preview.UploadMaxBytes}
	if limited.limit <= 0 {
		limited.limit = s.cfg.MaxCompressed
	}
	if _, err := io.Copy(limited, body); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	result, err := InspectTarZst(tmp, preview.EntryFile, s.limits())
	if err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := s.store.Put(ctx, result.StorageKey, tmp, result.CompressedSize, result.SHA256); err != nil {
		return err
	}
	run, _ := s.repo.Run(preview.RunID)
	return s.markReady(preview, result, run)
}

func (s *Service) markReady(preview domain.RunPreview, result Result, run domain.Run) error {
	now := s.clock()
	retention := s.cfg.DefaultRetention
	if run.WorkflowSnapshot.Preview != nil {
		if custom := run.WorkflowSnapshot.Preview.Retention(); custom > 0 {
			retention = custom
		}
	}
	if retention <= 0 || retention > MaxRetention {
		retention = DefaultRetention
	}
	preview.Status = domain.PreviewReady
	preview.StorageKey = result.StorageKey
	preview.ContentSHA256 = result.SHA256
	preview.CompressedSize = result.CompressedSize
	preview.ExpandedSize = result.ExpandedSize
	preview.FileCount = result.FileCount
	preview.EntryFile = result.EntryFile
	preview.ErrorText = ""
	preview.UploadTokenHash = ""
	preview.ArchiveLeaseUntil = time.Time{}
	preview.ExpiresAt = now.Add(retention)
	preview.UpdatedAt = now
	ok, err := s.repo.MarkPreviewReadyIfArchiving(preview)
	if err != nil {
		return err
	}
	if !ok {
		return ErrPreviewGone
	}
	_ = s.repo.ClearRunWorkspaceCleanupHold(preview.RunID)
	s.event(preview.RunID, "preview.archive.ready", "static preview ready")
	if s.host != nil {
		s.host.ReleaseWorkspace(preview.RunID)
	}
	return nil
}

func (s *Service) shouldSkipNonFrontend(run domain.Run, workspace string) bool {
	if strings.TrimSpace(workspace) == "" {
		return false
	}
	if found, _ := domain.DetectFrontendProject(workspace); found {
		return false
	}
	return !previewEntryExists(workspace, run.WorkflowSnapshot.Preview)
}

func (s *Service) skipIfNonFrontend(run domain.Run, workspace string, err error) bool {
	if err == nil || !errors.Is(err, ErrMissingIndex) {
		return false
	}
	if found, _ := domain.DetectFrontendProject(workspace); found {
		return false
	}
	s.Skip(run, "工作区没有前端工程，已跳过预览")
	if s.host != nil {
		s.host.ReleaseWorkspace(run.ID)
	}
	return true
}

func previewEntryExists(workspace string, contract *domain.PreviewContract) bool {
	if strings.TrimSpace(workspace) == "" || contract == nil {
		return false
	}
	source, err := SanitizeRelPath(contract.SourceOrDist())
	if err != nil {
		return false
	}
	index, err := SanitizeRelPath(contract.Index())
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(workspace, filepath.FromSlash(source), filepath.FromSlash(index)))
	return err == nil
}

// Skip records that preview was intentionally not produced. The run stays
// succeeded; the finalizer must not revive this row as pending.
func (s *Service) Skip(run domain.Run, reason string) {
	if !s.Enabled() {
		return
	}
	contract := run.WorkflowSnapshot.Preview
	now := s.clock()
	reason = clipPreviewError(reason)
	if reason == "" {
		reason = "工作区没有前端工程，已跳过预览"
	}
	preview := domain.RunPreview{
		ID:          repo.NewPreviewID(run.ID),
		RunID:       run.ID,
		ProjectID:   run.WorkflowSnapshot.ProjectID,
		Kind:        contract.KindOrStatic(),
		Status:      domain.PreviewSkipped,
		SourcePath:  contract.SourcePath,
		EntryFile:   contract.Index(),
		SPAFallback: contract.SPAFallback,
		ErrorText:   reason,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if existing, ok := s.repo.PreviewByRun(run.ID); ok {
		switch existing.Status {
		case domain.PreviewReady, domain.PreviewSkipped, domain.PreviewDeleted, domain.PreviewDeleting, domain.PreviewExpired:
			return
		}
		preview = existing
		preview.Status = domain.PreviewSkipped
		preview.ErrorText = reason
		preview.UploadTokenHash = ""
		preview.ArchiveLeaseUntil = time.Time{}
		preview.UpdatedAt = now
	}
	if err := s.repo.PutPreview(preview); err != nil {
		log.Printf("preview skip %s: %v", run.ID, err)
		return
	}
	_ = s.repo.ClearRunWorkspaceCleanupHold(preview.RunID)
	if s.host != nil {
		s.host.CancelPreviewArchive(preview.RunID)
	}
	s.event(preview.RunID, "preview.skipped", reason)
}

func (s *Service) Fail(preview domain.RunPreview, err error) {
	if err == nil || errors.Is(err, ErrPreviewBusy) || errors.Is(err, ErrPreviewGone) {
		return
	}
	msg := clipPreviewError(err.Error())
	ok, putErr := s.repo.FailPreviewIfOpen(preview.ID, msg, s.clock())
	if putErr != nil {
		log.Printf("preview fail persist %s: %v", preview.ID, putErr)
		return
	}
	if !ok {
		return
	}
	_ = s.repo.ClearRunWorkspaceCleanupHold(preview.RunID)
	s.event(preview.RunID, "preview.archive.failed", msg)
	if s.host != nil {
		s.host.ReleaseWorkspace(preview.RunID)
	}
}

func (s *Service) failNew(run domain.Run, contract *domain.PreviewContract, err error) {
	preview := domain.RunPreview{
		ID: repo.NewPreviewID(run.ID), RunID: run.ID, ProjectID: run.WorkflowSnapshot.ProjectID,
		Kind: contract.KindOrStatic(), Status: domain.PreviewFailed, SourcePath: contract.SourcePath,
		EntryFile: contract.Index(), ErrorText: clipPreviewError(err.Error()),
		CreatedAt: s.clock(), UpdatedAt: s.clock(),
	}
	_ = s.repo.PutPreview(preview)
	s.event(run.ID, "preview.archive.failed", preview.ErrorText)
}

func (s *Service) OnArchiveTaskCompleted(task domain.RunnerTask) {
	if task.Action != domain.RunnerTaskActionArchivePreview {
		return
	}
	preview, ok := s.repo.PreviewByRun(task.RunID)
	if !ok {
		return
	}
	if preview.Status == domain.PreviewReady {
		_ = s.repo.ClearRunWorkspaceCleanupHold(preview.RunID)
		if s.host != nil {
			s.host.ReleaseWorkspace(preview.RunID)
		}
		return
	}
	if task.Status == domain.RunnerTaskSuccess {
		return
	}
	s.Fail(preview, errors.New(strings.TrimSpace(task.Error)))
}

func (s *Service) Reconcile() {
	if !s.Enabled() {
		return
	}
	now := s.clock()
	for _, preview := range s.repo.PreviewsByStatus(domain.PreviewPending) {
		s.reconcileOne(preview, now)
	}
	for _, preview := range s.repo.PreviewsByStatus(domain.PreviewArchiving) {
		s.reconcileOne(preview, now)
	}
	for _, preview := range s.repo.ExpiredReadyPreviews(now) {
		s.expire(preview)
	}
	s.sweepOrphans(now)
}

func (s *Service) reconcileOne(preview domain.RunPreview, now time.Time) {
	run, ok := s.repo.Run(preview.RunID)
	if !ok {
		s.Fail(preview, errors.New("run missing during preview reconcile"))
		return
	}
	timeoutAt := preview.CreatedAt.Add(s.cfg.FinalizationTimeout)
	if !preview.UploadExpiresAt.IsZero() && preview.UploadExpiresAt.Before(timeoutAt) {
		timeoutAt = preview.UploadExpiresAt
	}
	if now.After(timeoutAt) {
		s.Fail(preview, errors.New("preview finalization timed out"))
		return
	}
	if preview.OwnerRunnerID != "" && run.ExecutionContext.ExecutionMode == domain.ExecutionModeRunner {
		return
	}
	if s.host == nil {
		return
	}
	workspace := s.host.RunWorkspace(run.ID)
	if err := s.ArchiveLocal(context.Background(), run, preview, workspace); err != nil {
		if errors.Is(err, ErrPreviewBusy) || errors.Is(err, ErrPreviewGone) {
			return
		}
		s.Fail(preview, err)
	}
}

func (s *Service) expire(preview domain.RunPreview) {
	preview.Status = domain.PreviewDeleting
	preview.UpdatedAt = s.clock()
	_ = s.repo.PutPreview(preview)
	if preview.StorageKey != "" {
		if n, err := s.repo.CountReadyStorageKey(preview.StorageKey); err == nil && n == 0 {
			_ = s.store.Delete(context.Background(), preview.StorageKey)
		}
	}
	preview.Status = domain.PreviewExpired
	preview.StorageKey = ""
	_ = s.repo.PutPreview(preview)
	s.event(preview.RunID, "preview.expired", "preview bundle expired")
}

func (s *Service) sweepOrphans(now time.Time) {
	_ = now
}

func (s *Service) CreateSession(preview domain.RunPreview, req *http.Request, backendURL string) (domain.PreviewSession, string, error) {
	if !preview.Serveable() {
		return domain.PreviewSession{}, "", errors.New("preview is not ready")
	}
	if strings.TrimSpace(backendURL) == "" && strings.TrimSpace(preview.BackendURL) != "" {
		backendURL = strings.TrimSpace(preview.BackendURL)
	}
	backendURL, err := domain.NormalizePreviewBackendURL(backendURL)
	if err != nil {
		return domain.PreviewSession{}, "", err
	}
	token, hash, err := newOpaqueToken()
	if err != nil {
		return domain.PreviewSession{}, "", err
	}
	now := s.clock()
	session := domain.PreviewSession{
		PreviewID:  preview.ID,
		TokenHash:  hash,
		BackendURL: backendURL,
		ExpiresAt:  now.Add(s.cfg.SessionTTL),
		CreatedAt:  now,
	}
	if err := s.repo.PutPreviewSession(session); err != nil {
		return domain.PreviewSession{}, "", err
	}
	stored, ok := s.repo.PreviewSessionByTokenHash(hash)
	if ok {
		session = stored
	}
	_ = s.repo.MarkPreviewAccessed(preview.ID, now)
	return session, s.PublicURL(token, req), nil
}

func (s *Service) SetPreviewBackend(previewID, backendURL string) (domain.RunPreview, error) {
	preview, ok := s.repo.Preview(previewID)
	if !ok {
		return domain.RunPreview{}, ErrPreviewNotFound
	}
	normalized, err := domain.NormalizePreviewBackendURL(backendURL)
	if err != nil {
		return domain.RunPreview{}, err
	}
	if err := s.repo.UpdatePreviewBackendURL(preview.ID, normalized); err != nil {
		return domain.RunPreview{}, err
	}
	preview.BackendURL = normalized
	return preview, nil
}

func (s *Service) LookupSession(token string) (domain.PreviewSession, domain.RunPreview, bool) {
	hash := hashToken(token)
	session, ok := s.repo.PreviewSessionByTokenHash(hash)
	if !ok || !session.Live(s.clock()) {
		return domain.PreviewSession{}, domain.RunPreview{}, false
	}
	preview, ok := s.repo.Preview(session.PreviewID)
	if !ok || !preview.Serveable() {
		return domain.PreviewSession{}, domain.RunPreview{}, false
	}
	if _, runOK := s.repo.Run(preview.RunID); !runOK {
		return domain.PreviewSession{}, domain.RunPreview{}, false
	}
	return session, preview, true
}

func (s *Service) RenewSession(session domain.PreviewSession) {
	now := s.clock()
	_ = s.repo.TouchPreviewSession(session.ID, now.Add(s.cfg.SessionTTL), now)
	_ = s.repo.MarkPreviewAccessed(session.PreviewID, now)
}

func (s *Service) RevokeSession(id string) error {
	return s.repo.RevokePreviewSession(id, s.clock())
}

func (s *Service) DeletePreview(preview domain.RunPreview) error {
	preview.Status = domain.PreviewDeleting
	if err := s.repo.PutPreview(preview); err != nil {
		return err
	}
	if preview.StorageKey != "" {
		if n, err := s.repo.CountReadyStorageKey(preview.StorageKey); err == nil && n == 0 {
			_ = s.store.Delete(context.Background(), preview.StorageKey)
		}
	}
	if err := s.repo.DeletePreview(preview.ID); err != nil {
		return err
	}
	s.event(preview.RunID, "preview.deleted", "preview deleted")
	return nil
}

func (s *Service) DeleteForRun(runID string) error {
	if !s.Enabled() {
		return nil
	}
	preview, ok := s.repo.PreviewByRun(runID)
	if !ok {
		return nil
	}
	if s.host != nil {
		s.host.CancelPreviewArchive(runID)
	}
	_ = s.repo.ClearRunWorkspaceCleanupHold(runID)
	return s.DeletePreview(preview)
}

func (s *Service) PublicURL(token string, req *http.Request) string {
	scheme, host := s.publicOrigin(req)
	return scheme + "://" + host + PreviewBasePath(token) + "/"
}

func (s *Service) publicOrigin(req *http.Request) (scheme, host string) {
	scheme = s.cfg.PublicScheme
	if scheme == "" {
		scheme = "https"
	}
	if req != nil {
		if proto := strings.TrimSpace(req.Header.Get("X-Forwarded-Proto")); proto != "" {
			scheme = strings.TrimSpace(strings.Split(proto, ",")[0])
		} else if req.TLS == nil {
			scheme = "http"
		}
	}
	host = strings.TrimSpace(s.cfg.PublicHost)
	if host == "" && req != nil {
		host = req.Host
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return scheme, host
}

func (s *Service) event(runID, typ, msg string) {
	if s.host != nil {
		s.host.AppendRunEvent(runID, typ, msg)
	}
}

func newOpaqueToken() (plain, hash string, err error) {
	raw := make([]byte, 16)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	plain = hex.EncodeToString(raw)
	return plain, hashToken(plain), nil
}

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func tokenHashEqual(plain, hash string) bool {
	return hashToken(plain) == hash
}

func clipPreviewError(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > 800 {
		return msg[:800]
	}
	return msg
}
