package repo

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

// EvolutionRepo owns asset evolution state: autonomy policies, findings,
// proposals, replays, immutable asset versions, run asset pins, feedback
// signals and pinned regression samples. It never writes catalog assets;
// releasing a version into an agent or rule is the caller's job.
type EvolutionRepo struct {
	*deps
}

type evoPolicyRow struct {
	AssetKind string     `gorm:"column:asset_kind;primaryKey;size:16"`
	AssetID   string     `gorm:"column:asset_id;primaryKey;size:128"`
	ProjectID string     `gorm:"column:project_id;size:64;index:idx_evo_policies_project"`
	Level     int        `gorm:"column:level"`
	Payload   []byte     `gorm:"column:payload;type:json"`
	Updated   *time.Time `gorm:"column:updated_at"`
}

func (evoPolicyRow) TableName() string { return "evo_policies" }

type evoFindingRow struct {
	ID        string     `gorm:"column:id;primaryKey;size:64"`
	ProjectID string     `gorm:"column:project_id;size:64;index:idx_evo_findings_project_status,priority:1"`
	Status    string     `gorm:"column:status;size:32;index:idx_evo_findings_project_status,priority:2"`
	AssetKind string     `gorm:"column:asset_kind;size:16;index:idx_evo_findings_asset,priority:1"`
	AssetID   string     `gorm:"column:asset_id;size:128;index:idx_evo_findings_asset,priority:2"`
	Payload   []byte     `gorm:"column:payload;type:json"`
	Created   *time.Time `gorm:"column:created_at"`
	Updated   *time.Time `gorm:"column:updated_at"`
}

func (evoFindingRow) TableName() string { return "evo_findings" }

type evoProposalRow struct {
	ID        string     `gorm:"column:id;primaryKey;size:64"`
	ProjectID string     `gorm:"column:project_id;size:64;index:idx_evo_proposals_project"`
	Status    string     `gorm:"column:status;size:32;index:idx_evo_proposals_status"`
	AssetKind string     `gorm:"column:asset_kind;size:16;index:idx_evo_proposals_asset,priority:1"`
	AssetID   string     `gorm:"column:asset_id;size:128;index:idx_evo_proposals_asset,priority:2"`
	Payload   []byte     `gorm:"column:payload;type:json"`
	Created   *time.Time `gorm:"column:created_at"`
	Updated   *time.Time `gorm:"column:updated_at"`
}

func (evoProposalRow) TableName() string { return "evo_proposals" }

type evoReplayRow struct {
	ID         string     `gorm:"column:id;primaryKey;size:64"`
	ProposalID string     `gorm:"column:proposal_id;size:64;index:idx_evo_replays_proposal"`
	RunID      string     `gorm:"column:run_id;size:64;index:idx_evo_replays_run"`
	Status     string     `gorm:"column:status;size:32"`
	Payload    []byte     `gorm:"column:payload;type:json"`
	Created    *time.Time `gorm:"column:created_at"`
	Updated    *time.Time `gorm:"column:updated_at"`
}

func (evoReplayRow) TableName() string { return "evo_replays" }

type assetVersionRow struct {
	AssetKind  string     `gorm:"column:asset_kind;primaryKey;size:16"`
	AssetID    string     `gorm:"column:asset_id;primaryKey;size:128"`
	Version    int        `gorm:"column:version;primaryKey;autoIncrement:false"`
	Hash       string     `gorm:"column:hash;size:32"`
	Source     string     `gorm:"column:source;size:32"`
	ProposalID string     `gorm:"column:proposal_id;size:64"`
	Payload    []byte     `gorm:"column:payload;type:json"`
	Created    *time.Time `gorm:"column:created_at"`
}

func (assetVersionRow) TableName() string { return "asset_versions" }

type runAssetPinRow struct {
	RunID     string     `gorm:"column:run_id;primaryKey;size:64"`
	AssetKind string     `gorm:"column:asset_kind;primaryKey;size:16"`
	AssetID   string     `gorm:"column:asset_id;primaryKey;size:128"`
	Version   int        `gorm:"column:version"`
	Hash      string     `gorm:"column:hash;size:32"`
	Created   *time.Time `gorm:"column:created_at"`
}

func (runAssetPinRow) TableName() string { return "run_asset_pins" }

type evoSignalRow struct {
	ID        string     `gorm:"column:id;primaryKey;size:64"`
	ProjectID string     `gorm:"column:project_id;size:64;index:idx_evo_signals_project_created,priority:1"`
	RunID     string     `gorm:"column:run_id;size:64;index:idx_evo_signals_run"`
	Kind      string     `gorm:"column:kind;size:32"`
	Payload   []byte     `gorm:"column:payload;type:json"`
	Created   *time.Time `gorm:"column:created_at;index:idx_evo_signals_project_created,priority:2"`
}

func (evoSignalRow) TableName() string { return "evo_signals" }

type evoSampleRow struct {
	ProjectID string     `gorm:"column:project_id;primaryKey;size:64"`
	RunID     string     `gorm:"column:run_id;primaryKey;size:64"`
	Payload   []byte     `gorm:"column:payload;type:json"`
	Created   *time.Time `gorm:"column:created_at"`
}

func (evoSampleRow) TableName() string { return "evo_samples" }

// ---------- policies ----------

func (r *EvolutionRepo) EvoPolicy(kind, id string) (domain.EvoPolicy, bool) {
	var row evoPolicyRow
	if err := r.DB.Where("asset_kind = ? AND asset_id = ?", kind, id).Take(&row).Error; err != nil {
		return domain.EvoPolicy{}, false
	}
	var p domain.EvoPolicy
	if json.Unmarshal(row.Payload, &p) != nil {
		p = domain.EvoPolicy{}
	}
	p.AssetKind, p.AssetID, p.ProjectID, p.Level = row.AssetKind, row.AssetID, row.ProjectID, row.Level
	return p, true
}

// EvoLevel returns the asset's autonomy level, defaulting to L2.
func (r *EvolutionRepo) EvoLevel(kind, id string) int {
	if p, ok := r.EvoPolicy(kind, id); ok {
		return domain.NormalizeEvoLevel(p.Level)
	}
	return domain.EvoDefaultLevel
}

func (r *EvolutionRepo) EvoPolicies(projectID string) []domain.EvoPolicy {
	var rows []evoPolicyRow
	q := r.DB.Model(&evoPolicyRow{})
	if projectID = strings.TrimSpace(projectID); projectID != "" {
		q = q.Where("project_id = ? OR project_id = ''", projectID)
	}
	if err := q.Find(&rows).Error; err != nil {
		return []domain.EvoPolicy{}
	}
	out := make([]domain.EvoPolicy, 0, len(rows))
	for _, row := range rows {
		var p domain.EvoPolicy
		_ = json.Unmarshal(row.Payload, &p)
		p.AssetKind, p.AssetID, p.ProjectID, p.Level = row.AssetKind, row.AssetID, row.ProjectID, row.Level
		out = append(out, p)
	}
	return out
}

func (r *EvolutionRepo) PutEvoPolicy(p domain.EvoPolicy) error {
	if !domain.ValidEvoAssetKind(p.AssetKind) || strings.TrimSpace(p.AssetID) == "" {
		return errors.New("evolution policy needs a valid asset kind and id")
	}
	p.Level = domain.NormalizeEvoLevel(p.Level)
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	return upsert(r.DB, "evo_policies", map[string]any{
		"asset_kind": p.AssetKind, "asset_id": p.AssetID, "project_id": p.ProjectID,
		"level": p.Level, "payload": mustJSON(p), "updated_at": timePtr(p.UpdatedAt),
	})
}

// ---------- findings ----------

func (r *EvolutionRepo) PutEvoFinding(f domain.EvoFinding) error {
	if strings.TrimSpace(f.ID) == "" {
		return errors.New("finding id is required")
	}
	now := time.Now().UTC()
	if f.CreatedAt.IsZero() {
		f.CreatedAt = now
	}
	f.UpdatedAt = now
	return upsert(r.DB, "evo_findings", map[string]any{
		"id": f.ID, "project_id": f.ProjectID, "status": f.Status, "asset_kind": f.AssetKind, "asset_id": f.AssetID,
		"payload": mustJSON(f), "created_at": timePtr(f.CreatedAt), "updated_at": timePtr(f.UpdatedAt),
	})
}

func (r *EvolutionRepo) EvoFinding(id string) (domain.EvoFinding, bool) {
	var row evoFindingRow
	if err := r.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.EvoFinding{}, false
	}
	var f domain.EvoFinding
	if json.Unmarshal(row.Payload, &f) != nil {
		return domain.EvoFinding{}, false
	}
	f.Status = row.Status
	return f, true
}

// EvoFindings lists a project's findings, newest first. Empty statuses keep all.
func (r *EvolutionRepo) EvoFindings(projectID string, statuses ...string) []domain.EvoFinding {
	var rows []evoFindingRow
	q := r.DB.Model(&evoFindingRow{}).Where("project_id = ?", projectID)
	if len(statuses) > 0 {
		q = q.Where("status IN ?", statuses)
	}
	if err := q.Order("updated_at DESC").Limit(200).Find(&rows).Error; err != nil {
		return []domain.EvoFinding{}
	}
	out := make([]domain.EvoFinding, 0, len(rows))
	for _, row := range rows {
		var f domain.EvoFinding
		if json.Unmarshal(row.Payload, &f) == nil {
			f.Status = row.Status
			out = append(out, f)
		}
	}
	return out
}

// OpenEvoFindingForAsset returns the open finding already tracking an asset.
func (r *EvolutionRepo) OpenEvoFindingForAsset(projectID, kind, id string) (domain.EvoFinding, bool) {
	var row evoFindingRow
	err := r.DB.Where("project_id = ? AND asset_kind = ? AND asset_id = ? AND status = ?", projectID, kind, id, domain.EvoFindingOpen).
		Order("updated_at DESC").Limit(1).Take(&row).Error
	if err != nil {
		return domain.EvoFinding{}, false
	}
	var f domain.EvoFinding
	if json.Unmarshal(row.Payload, &f) != nil {
		return domain.EvoFinding{}, false
	}
	f.Status = row.Status
	return f, true
}

// ---------- proposals ----------

func (r *EvolutionRepo) PutEvoProposal(p domain.EvoProposal) error {
	if strings.TrimSpace(p.ID) == "" || !domain.ValidEvoAssetKind(p.AssetKind) || strings.TrimSpace(p.AssetID) == "" {
		return errors.New("proposal needs id, asset kind and asset id")
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	stored := p
	stored.Replays = nil
	return upsert(r.DB, "evo_proposals", map[string]any{
		"id": p.ID, "project_id": p.ProjectID, "status": p.Status, "asset_kind": p.AssetKind, "asset_id": p.AssetID,
		"payload": mustJSON(stored), "created_at": timePtr(p.CreatedAt), "updated_at": timePtr(p.UpdatedAt),
	})
}

func (r *EvolutionRepo) evoProposalFromRow(row evoProposalRow, withReplays bool) (domain.EvoProposal, bool) {
	var p domain.EvoProposal
	if json.Unmarshal(row.Payload, &p) != nil {
		return domain.EvoProposal{}, false
	}
	p.Status = row.Status
	if withReplays {
		p.Replays = r.EvoReplays(p.ID)
	}
	return p, true
}

func (r *EvolutionRepo) EvoProposal(id string) (domain.EvoProposal, bool) {
	var row evoProposalRow
	if err := r.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.EvoProposal{}, false
	}
	return r.evoProposalFromRow(row, true)
}

// EvoProposals lists proposals, newest first. Empty projectID spans all
// projects (the replay monitor); empty statuses keep every status.
func (r *EvolutionRepo) EvoProposals(projectID string, limit int, statuses ...string) []domain.EvoProposal {
	var rows []evoProposalRow
	q := r.DB.Model(&evoProposalRow{})
	if projectID != "" {
		q = q.Where("project_id = ?", projectID)
	}
	if len(statuses) > 0 {
		q = q.Where("status IN ?", statuses)
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if err := q.Order("updated_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return []domain.EvoProposal{}
	}
	out := make([]domain.EvoProposal, 0, len(rows))
	for _, row := range rows {
		if p, ok := r.evoProposalFromRow(row, true); ok {
			out = append(out, p)
		}
	}
	return out
}

// OpenEvoProposalForAsset returns a drafting/validating/pending/canary
// proposal of the asset. Only one open proposal per asset is allowed.
func (r *EvolutionRepo) OpenEvoProposalForAsset(kind, id string) (domain.EvoProposal, bool) {
	var row evoProposalRow
	err := r.DB.Where("asset_kind = ? AND asset_id = ? AND status IN ?", kind, id, domain.EvoOpenProposalStatuses).
		Order("updated_at DESC").Limit(1).Take(&row).Error
	if err != nil {
		return domain.EvoProposal{}, false
	}
	return r.evoProposalFromRow(row, false)
}

// ActiveEvoCanaries lists proposals whose trial still splits new runs. Run
// start calls it, so replays are not loaded.
func (r *EvolutionRepo) ActiveEvoCanaries() []domain.EvoProposal {
	var rows []evoProposalRow
	if err := r.DB.Where("status = ?", domain.EvoProposalCanary).Order("updated_at DESC").Limit(50).Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.EvoProposal, 0, len(rows))
	for _, row := range rows {
		if p, ok := r.evoProposalFromRow(row, false); ok && p.TrialActive() {
			out = append(out, p)
		}
	}
	return out
}

// EvoCanaryOverride is the engine's lookup for a trial run's candidate. It
// ignores the proposal's current status: a run keeps the body it started
// with even if the trial ends while it runs.
func (r *EvolutionRepo) EvoCanaryOverride(id string) (domain.EvoAssetOverride, bool) {
	var row evoProposalRow
	if strings.TrimSpace(id) == "" || r.DB.Where("id = ?", id).Take(&row).Error != nil {
		return domain.EvoAssetOverride{}, false
	}
	p, ok := r.evoProposalFromRow(row, false)
	if !ok || p.Canary == nil || strings.TrimSpace(p.CandidateContent) == "" {
		return domain.EvoAssetOverride{}, false
	}
	switch p.AssetKind {
	case domain.EvoAssetAgent:
		return domain.EvoAssetOverride{AgentPrompts: map[string]string{p.AssetID: p.CandidateContent}}, true
	case domain.EvoAssetRule:
		return domain.EvoAssetOverride{RuleContents: map[string]string{p.AssetID: p.CandidateContent}}, true
	}
	return domain.EvoAssetOverride{}, false
}

// ---------- replays ----------

func (r *EvolutionRepo) PutEvoReplay(v domain.EvoReplay) error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.ProposalID) == "" {
		return errors.New("replay needs id and proposal id")
	}
	now := time.Now().UTC()
	created := now
	var existing evoReplayRow
	if r.DB.Select("created_at").Where("id = ?", v.ID).Take(&existing).Error == nil && existing.Created != nil {
		created = *existing.Created
	}
	return upsert(r.DB, "evo_replays", map[string]any{
		"id": v.ID, "proposal_id": v.ProposalID, "run_id": v.RunID, "status": v.Status,
		"payload": mustJSON(v), "created_at": timePtr(created), "updated_at": timePtr(now),
	})
}

func (r *EvolutionRepo) EvoReplays(proposalID string) []domain.EvoReplay {
	var rows []evoReplayRow
	if err := r.DB.Where("proposal_id = ?", proposalID).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.EvoReplay, 0, len(rows))
	for _, row := range rows {
		var v domain.EvoReplay
		if json.Unmarshal(row.Payload, &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

func (r *EvolutionRepo) EvoReplay(id string) (domain.EvoReplay, bool) {
	var row evoReplayRow
	if err := r.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.EvoReplay{}, false
	}
	var v domain.EvoReplay
	if json.Unmarshal(row.Payload, &v) != nil {
		return domain.EvoReplay{}, false
	}
	return v, true
}

// EvoReplayOverrides is the engine's lookup for a replay run's candidate.
func (r *EvolutionRepo) EvoReplayOverrides(id string) (domain.EvoAssetOverride, bool) {
	if strings.TrimSpace(id) == "" {
		return domain.EvoAssetOverride{}, false
	}
	v, ok := r.EvoReplay(id)
	if !ok {
		return domain.EvoAssetOverride{}, false
	}
	return v.Overrides, true
}

// ---------- asset versions ----------

func assetVersionFromRow(row assetVersionRow) domain.AssetVersion {
	var v domain.AssetVersion
	_ = json.Unmarshal(row.Payload, &v)
	v.AssetKind, v.AssetID, v.Version, v.Hash, v.Source, v.ProposalID = row.AssetKind, row.AssetID, row.Version, row.Hash, row.Source, row.ProposalID
	if row.Created != nil {
		v.CreatedAt = *row.Created
	}
	return v
}

// AssetVersions lists an asset's immutable versions, newest first.
func (r *EvolutionRepo) AssetVersions(kind, id string) []domain.AssetVersion {
	var rows []assetVersionRow
	if err := r.DB.Where("asset_kind = ? AND asset_id = ?", kind, id).Order("version DESC").Find(&rows).Error; err != nil {
		return []domain.AssetVersion{}
	}
	out := make([]domain.AssetVersion, 0, len(rows))
	for _, row := range rows {
		out = append(out, assetVersionFromRow(row))
	}
	return out
}

func (r *EvolutionRepo) AssetVersion(kind, id string, version int) (domain.AssetVersion, bool) {
	var row assetVersionRow
	if err := r.DB.Where("asset_kind = ? AND asset_id = ? AND version = ?", kind, id, version).Take(&row).Error; err != nil {
		return domain.AssetVersion{}, false
	}
	return assetVersionFromRow(row), true
}

func (r *EvolutionRepo) LatestAssetVersion(kind, id string) (domain.AssetVersion, bool) {
	var row assetVersionRow
	if err := r.DB.Where("asset_kind = ? AND asset_id = ?", kind, id).Order("version DESC").Limit(1).Take(&row).Error; err != nil {
		return domain.AssetVersion{}, false
	}
	return assetVersionFromRow(row), true
}

// AssetVersionByHash finds the newest version carrying exactly this body.
func (r *EvolutionRepo) AssetVersionByHash(kind, id, hash string) (domain.AssetVersion, bool) {
	var row assetVersionRow
	if err := r.DB.Where("asset_kind = ? AND asset_id = ? AND hash = ?", kind, id, hash).Order("version DESC").Limit(1).Take(&row).Error; err != nil {
		return domain.AssetVersion{}, false
	}
	return assetVersionFromRow(row), true
}

// AppendAssetVersion stores a new immutable version numbered after the
// latest one. Versions are never updated or deleted.
func (r *EvolutionRepo) AppendAssetVersion(v domain.AssetVersion) (domain.AssetVersion, error) {
	if !domain.ValidEvoAssetKind(v.AssetKind) || strings.TrimSpace(v.AssetID) == "" {
		return domain.AssetVersion{}, errors.New("asset version needs a valid asset kind and id")
	}
	v.Hash = domain.ContentHash(v.Content)
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		lastErr = r.DB.Transaction(func(tx *gorm.DB) error {
			var maxVersion sql.NullInt64
			if err := tx.Model(&assetVersionRow{}).Where("asset_kind = ? AND asset_id = ?", v.AssetKind, v.AssetID).
				Select("MAX(version)").Row().Scan(&maxVersion); err != nil {
				return err
			}
			v.Version = 1
			if maxVersion.Valid {
				v.Version = int(maxVersion.Int64) + 1
			}
			v.Current = false
			return tx.Create(&assetVersionRow{
				AssetKind: v.AssetKind, AssetID: v.AssetID, Version: v.Version, Hash: v.Hash, Source: v.Source,
				ProposalID: v.ProposalID, Payload: mustJSON(v), Created: timePtr(v.CreatedAt),
			}).Error
		})
		if lastErr == nil {
			return v, nil
		}
		if !errors.Is(lastErr, gorm.ErrDuplicatedKey) {
			break
		}
	}
	return domain.AssetVersion{}, fmt.Errorf("asset version write: %w", lastErr)
}

// ---------- run pins ----------

// PutRunAssetPins records what asset bodies a run started with. Existing
// pins are kept: a run's pins never change after start.
func (r *EvolutionRepo) PutRunAssetPins(pins []domain.RunAssetPin) error {
	for _, pin := range pins {
		if pin.RunID == "" || pin.AssetID == "" {
			continue
		}
		if pin.CreatedAt.IsZero() {
			pin.CreatedAt = time.Now().UTC()
		}
		if _, err := insertIgnore(r.DB, "run_asset_pins", map[string]any{
			"run_id": pin.RunID, "asset_kind": pin.AssetKind, "asset_id": pin.AssetID,
			"version": pin.Version, "hash": pin.Hash, "created_at": timePtr(pin.CreatedAt),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *EvolutionRepo) RunAssetPins(runID string) []domain.RunAssetPin {
	var rows []runAssetPinRow
	if err := r.DB.Where("run_id = ?", runID).Find(&rows).Error; err != nil {
		return []domain.RunAssetPin{}
	}
	out := make([]domain.RunAssetPin, 0, len(rows))
	for _, row := range rows {
		pin := domain.RunAssetPin{RunID: row.RunID, AssetKind: row.AssetKind, AssetID: row.AssetID, Version: row.Version, Hash: row.Hash}
		if row.Created != nil {
			pin.CreatedAt = *row.Created
		}
		out = append(out, pin)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AssetKind != out[j].AssetKind {
			return out[i].AssetKind < out[j].AssetKind
		}
		return out[i].AssetID < out[j].AssetID
	})
	return out
}

// RunIDsPinnedTo lists runs (newest first) that started with an asset.
func (r *EvolutionRepo) RunIDsPinnedTo(kind, id string, since time.Time, limit int) []string {
	var ids []string
	q := r.DB.Model(&runAssetPinRow{}).Where("asset_kind = ? AND asset_id = ?", kind, id)
	if !since.IsZero() {
		q = q.Where("created_at >= ?", since)
	}
	if limit <= 0 {
		limit = 200
	}
	if err := q.Order("created_at DESC").Limit(limit).Pluck("run_id", &ids).Error; err != nil {
		return nil
	}
	return ids
}

// AssetPinsBetween lists pins of an asset created in [since, until), newest
// first. Zero bounds are open. Trials and post-release watches split the
// runs by pin hash: the body each run actually started with.
func (r *EvolutionRepo) AssetPinsBetween(kind, id string, since, until time.Time, limit int) []domain.RunAssetPin {
	var rows []runAssetPinRow
	q := r.DB.Model(&runAssetPinRow{}).Where("asset_kind = ? AND asset_id = ?", kind, id)
	if !since.IsZero() {
		q = q.Where("created_at >= ?", since)
	}
	if !until.IsZero() {
		q = q.Where("created_at < ?", until)
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if err := q.Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.RunAssetPin, 0, len(rows))
	for _, row := range rows {
		pin := domain.RunAssetPin{RunID: row.RunID, AssetKind: row.AssetKind, AssetID: row.AssetID, Version: row.Version, Hash: row.Hash}
		if row.Created != nil {
			pin.CreatedAt = *row.Created
		}
		out = append(out, pin)
	}
	return out
}

// ---------- signals & samples ----------

func (r *EvolutionRepo) AddEvoSignal(s domain.EvoSignal) error {
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.RunID) == "" {
		return errors.New("signal needs id and run id")
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	return upsert(r.DB, "evo_signals", map[string]any{
		"id": s.ID, "project_id": s.ProjectID, "run_id": s.RunID, "kind": s.Kind,
		"payload": mustJSON(s), "created_at": timePtr(s.CreatedAt),
	})
}

func (r *EvolutionRepo) EvoSignals(projectID string, since time.Time) []domain.EvoSignal {
	var rows []evoSignalRow
	q := r.DB.Model(&evoSignalRow{}).Where("project_id = ?", projectID)
	if !since.IsZero() {
		q = q.Where("created_at >= ?", since)
	}
	if err := q.Order("created_at DESC").Limit(500).Find(&rows).Error; err != nil {
		return []domain.EvoSignal{}
	}
	out := make([]domain.EvoSignal, 0, len(rows))
	for _, row := range rows {
		var s domain.EvoSignal
		if json.Unmarshal(row.Payload, &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

func (r *EvolutionRepo) PutEvoSample(s domain.EvoSample) error {
	if strings.TrimSpace(s.ProjectID) == "" || strings.TrimSpace(s.RunID) == "" {
		return errors.New("sample needs project id and run id")
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	return upsert(r.DB, "evo_samples", map[string]any{
		"project_id": s.ProjectID, "run_id": s.RunID, "payload": mustJSON(s), "created_at": timePtr(s.CreatedAt),
	})
}

func (r *EvolutionRepo) DeleteEvoSample(projectID, runID string) error {
	return r.DB.Where("project_id = ? AND run_id = ?", projectID, runID).Delete(&evoSampleRow{}).Error
}

func (r *EvolutionRepo) EvoSamples(projectID string) []domain.EvoSample {
	var rows []evoSampleRow
	if err := r.DB.Where("project_id = ?", projectID).Order("created_at DESC").Limit(200).Find(&rows).Error; err != nil {
		return []domain.EvoSample{}
	}
	out := make([]domain.EvoSample, 0, len(rows))
	for _, row := range rows {
		var s domain.EvoSample
		if json.Unmarshal(row.Payload, &s) == nil {
			out = append(out, s)
		}
	}
	return out
}
