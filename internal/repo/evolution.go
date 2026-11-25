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
// signals and pinned regression samples. Catalog writes use explicit injection
// so release and rollback commit together with evolution state.
type EvolutionRepo struct {
	*deps
	catalog *CatalogRepo
}

var (
	ErrEvoStale    = errors.New("进化状态或资产基线已变化，请刷新后重试")
	ErrEvoBusy     = errors.New("该资产已有当前改进，请等待其结束")
	ErrEvoParallel = errors.New("该资产存在历史并行改进冲突，需要管理员处理")
)

type evoAssetGateRow struct {
	AssetKind string `gorm:"column:asset_kind;primaryKey;size:16"`
	AssetID   string `gorm:"column:asset_id;primaryKey;size:128"`
}

func (evoAssetGateRow) TableName() string { return "evo_asset_gates" }

type evoQueueRow struct {
	ID        string     `gorm:"column:id;primaryKey;size:64"`
	ProjectID string     `gorm:"column:project_id;size:64;index:idx_evo_queue_project_status,priority:1"`
	Status    string     `gorm:"column:status;size:32;index:idx_evo_queue_project_status,priority:2"`
	FindingID string     `gorm:"column:finding_id;size:64;uniqueIndex:idx_evo_queue_finding"`
	AssetKind string     `gorm:"column:asset_kind;size:16;index:idx_evo_queue_asset,priority:1"`
	AssetID   string     `gorm:"column:asset_id;size:128;index:idx_evo_queue_asset,priority:2"`
	Payload   []byte     `gorm:"column:payload;type:json"`
	Created   *time.Time `gorm:"column:created_at"`
	Updated   *time.Time `gorm:"column:updated_at"`
}

func (evoQueueRow) TableName() string { return "evo_queue" }

func (r *EvolutionRepo) assetTx(kind, id string, fn func(*EvolutionRepo) error) error {
	if !domain.ValidEvoAssetKind(kind) || strings.TrimSpace(id) == "" {
		return errors.New("invalid evolution asset")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row := evoAssetGateRow{AssetKind: kind, AssetID: id}
		if _, err := insertIgnore(tx, "evo_asset_gates", map[string]any{"asset_kind": kind, "asset_id": id}); err != nil {
			return err
		}
		if err := r.locking(tx.Where("asset_kind = ? AND asset_id = ?", kind, id)).Take(&row).Error; err != nil {
			return err
		}
		return fn(&EvolutionRepo{deps: &deps{DB: tx}, catalog: r.catalog})
	}, r.admissionTxOptions()...)
}

func (r *EvolutionRepo) currentAssetProposal(kind, id string) (domain.EvoProposal, bool, error) {
	var rows []evoProposalRow
	statuses := append(append([]string{}, domain.EvoOpenProposalStatuses...), domain.EvoProposalReleased)
	if err := r.DB.Where("asset_kind = ? AND asset_id = ? AND status IN ?", kind, id, statuses).Find(&rows).Error; err != nil {
		return domain.EvoProposal{}, false, err
	}
	var current domain.EvoProposal
	found := false
	for _, row := range rows {
		p, ok := r.evoProposalFromRow(row, false)
		if !ok {
			return current, false, ErrEvoParallel
		}
		if p.OccupiesAsset() {
			if found {
				return current, false, ErrEvoParallel
			}
			current, found = p, true
		}
	}
	return current, found, nil
}

func (r *EvolutionRepo) CurrentEvoProposalForAsset(kind, id string) (domain.EvoProposal, bool, error) {
	return r.currentAssetProposal(kind, id)
}

func (r *EvolutionRepo) EvoQueueItem(id string) (domain.EvoQueueItem, bool) {
	var row evoQueueRow
	if r.DB.Where("id = ?", id).Take(&row).Error != nil {
		return domain.EvoQueueItem{}, false
	}
	var item domain.EvoQueueItem
	if json.Unmarshal(row.Payload, &item) != nil {
		return domain.EvoQueueItem{}, false
	}
	item.Status = row.Status
	return item, true
}

func (r *EvolutionRepo) EvoQueue(projectID string) []domain.EvoQueueItem {
	var rows []evoQueueRow
	out := []domain.EvoQueueItem{}
	if r.DB.Where("project_id = ? AND status = ?", projectID, "queued").Order("created_at ASC, id ASC").Find(&rows).Error != nil {
		return out
	}
	for _, row := range rows {
		var item domain.EvoQueueItem
		if json.Unmarshal(row.Payload, &item) == nil {
			item.Status = row.Status
			out = append(out, item)
		}
	}
	return out
}

func (r *EvolutionRepo) saveQueue(item domain.EvoQueueItem, create bool) error {
	item.UpdatedAt = time.Now().UTC()
	row := evoQueueRow{ID: item.ID, ProjectID: item.ProjectID, Status: item.Status, FindingID: item.FindingID,
		AssetKind: item.AssetKind, AssetID: item.AssetID, Payload: mustJSON(item), Created: timePtr(item.CreatedAt), Updated: timePtr(item.UpdatedAt)}
	if create {
		return r.DB.Create(&row).Error
	}
	result := r.DB.Model(&evoQueueRow{}).Where("id = ? AND status = ?", item.ID, "queued").Updates(map[string]any{"status": item.Status, "payload": row.Payload, "updated_at": row.Updated})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrEvoStale
	}
	return nil
}

func (r *EvolutionRepo) RejectEvoQueue(id string) (domain.EvoQueueItem, error) {
	item, ok := r.EvoQueueItem(id)
	if !ok {
		return item, ErrEvoStale
	}
	err := r.assetTx(item.AssetKind, item.AssetID, func(tr *EvolutionRepo) error {
		var exists bool
		item, exists = tr.EvoQueueItem(id)
		if !exists || item.Status != "queued" {
			return ErrEvoStale
		}
		item.Status = "rejected"
		return tr.saveQueue(item, false)
	})
	return item, err
}

// SubmitEvoProposal admits one current improvement, or persists a later intent.
// startID is only set by the explicit, re-authorized queue-start endpoint.
func (r *EvolutionRepo) SubmitEvoProposal(p domain.EvoProposal, queueID, startID string) (domain.EvoProposal, *domain.EvoQueueItem, bool, error) {
	var queued *domain.EvoQueueItem
	created := false
	err := r.assetTx(p.AssetKind, p.AssetID, func(tr *EvolutionRepo) error {
		current, busy, err := tr.currentAssetProposal(p.AssetKind, p.AssetID)
		if err != nil {
			return err
		}
		if busy && current.ProjectID == p.ProjectID && current.FindingID == p.FindingID && p.FindingID != "" {
			p = current
			return nil
		}
		var existing evoQueueRow
		qerr := tr.DB.Where("finding_id = ?", p.FindingID).Take(&existing).Error
		if qerr != nil && !errors.Is(qerr, gorm.ErrRecordNotFound) {
			return qerr
		}
		if qerr == nil {
			item, ok := tr.EvoQueueItem(existing.ID)
			if !ok || item.ProjectID != p.ProjectID || item.AssetKind != p.AssetKind || item.AssetID != p.AssetID {
				return ErrEvoStale
			}
			if startID != "" {
				if item.ID != startID || item.Status != "queued" {
					return ErrEvoStale
				}
			} else if item.Status == "queued" {
				queued = &item
				return nil
			} else {
				if item.Status != "started" && item.Status != "rejected" {
					return ErrEvoStale
				}
				result := tr.DB.Where("id = ? AND status = ?", item.ID, item.Status).Delete(&evoQueueRow{})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrEvoStale
				}
			}
		} else if startID != "" {
			return ErrEvoStale
		}
		if busy {
			if startID != "" {
				return ErrEvoBusy
			}
			f, ok := tr.EvoFinding(p.FindingID)
			if !ok || f.ProjectID != p.ProjectID {
				return ErrEvoStale
			}
			item := domain.EvoQueueItem{ID: queueID, ProjectID: p.ProjectID, FindingID: p.FindingID, AssetKind: p.AssetKind, AssetID: p.AssetID,
				AssetName: p.AssetName, Title: f.Title, Model: p.Model, Status: "queued", CreatedBy: p.CreatedBy, CreatedAt: time.Now().UTC()}
			if err := tr.saveQueue(item, true); err != nil {
				return err
			}
			queued = &item
			return nil
		}
		if tr.EvoLevel(p.AssetKind, p.AssetID) < domain.EvoLevelPropose {
			return ErrEvoStale
		}
		content, raw, err := tr.catalog.evolutionAsset(tr.DB, p.AssetKind, p.AssetID)
		if err != nil {
			return err
		}
		p.BaseContent, p.BaseHash, p.BaseCatalogHash = content, domain.ContentHash(content), domain.ContentHash(string(raw))
		baseline, err := tr.versionLive(p.AssetKind, p.AssetID, content, p.CreatedBy)
		if err != nil {
			return err
		}
		p.BaseVersion = baseline.Version
		if err := tr.writeProposal(p); err != nil {
			return err
		}
		if startID != "" {
			item, _ := tr.EvoQueueItem(startID)
			item.Status, item.ProposalID = "started", p.ID
			if err := tr.saveQueue(item, false); err != nil {
				return err
			}
		}
		p, _ = tr.EvoProposal(p.ID)
		created = true
		return nil
	})
	return p, queued, created, err
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
	Revision  int64      `gorm:"column:revision;not null;default:0"`
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
	return r.assetTx(f.AssetKind, f.AssetID, func(tr *EvolutionRepo) error {
		if old, ok := tr.EvoFinding(f.ID); ok {
			if old.ProjectID != f.ProjectID || old.AssetKind != f.AssetKind || old.AssetID != f.AssetID {
				return ErrEvoStale
			}
			proposalID := old.ProposalID
			if proposalID == "" {
				proposalID = f.ProposalID
			}
			if p, exists := tr.EvoProposal(proposalID); exists && p.FindingID == f.ID {
				f.Status, f.ProposalID = old.Status, old.ProposalID
				if p.Open() {
					f.Status, f.ProposalID = domain.EvoFindingProposed, p.ID
				} else if p.Status == domain.EvoProposalReleased {
					f.Status, f.ProposalID = domain.EvoFindingResolved, p.ID
				}
			}
			var count int64
			if err := tr.DB.Model(&evoQueueRow{}).Where("finding_id = ? AND status = ?", f.ID, "queued").Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				f.Status, f.CloseNote = old.Status, old.CloseNote
			}
		}
		return tr.writeFinding(f)
	})
}

func (r *EvolutionRepo) writeFinding(f domain.EvoFinding) error {
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
	return r.evoFindingForAsset(projectID, kind, id, domain.EvoFindingOpen)
}

// ActiveEvoFindingForAsset returns the open or proposed finding tracking an
// asset, so re-diagnosis updates it instead of opening a duplicate.
func (r *EvolutionRepo) ActiveEvoFindingForAsset(projectID, kind, id string) (domain.EvoFinding, bool) {
	return r.evoFindingForAsset(projectID, kind, id, domain.EvoFindingOpen, domain.EvoFindingProposed)
}

func (r *EvolutionRepo) evoFindingForAsset(projectID, kind, id string, statuses ...string) (domain.EvoFinding, bool) {
	var row evoFindingRow
	err := r.DB.Where("project_id = ? AND asset_kind = ? AND asset_id = ? AND status IN ?", projectID, kind, id, statuses).
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
	return r.assetTx(p.AssetKind, p.AssetID, func(tr *EvolutionRepo) error {
		current, busy, err := tr.currentAssetProposal(p.AssetKind, p.AssetID)
		if errors.Is(err, ErrEvoParallel) && p.Status == domain.EvoProposalRejected {
			return tr.writeProposal(p)
		}
		if err != nil {
			return err
		}
		if p.OccupiesAsset() && busy && current.ID != p.ID {
			return ErrEvoBusy
		}
		if busy && current.ID == p.ID && current.Status != p.Status && (p.Status == domain.EvoProposalValidating || p.Status == domain.EvoProposalCanary) {
			content, raw, err := tr.catalog.evolutionAsset(tr.DB, p.AssetKind, p.AssetID)
			if err != nil {
				return err
			}
			if domain.ContentHash(content) != p.BaseHash || (p.BaseCatalogHash != "" && domain.ContentHash(string(raw)) != p.BaseCatalogHash) {
				return ErrEvoStale
			}
			if p.BaseVersion > 0 {
				latest, ok := tr.LatestAssetVersion(p.AssetKind, p.AssetID)
				if !ok || latest.Version != p.BaseVersion {
					return ErrEvoStale
				}
			}
		}
		return tr.writeProposal(p)
	})
}

func (r *EvolutionRepo) writeProposal(p domain.EvoProposal) error {
	if strings.TrimSpace(p.ID) == "" || !domain.ValidEvoAssetKind(p.AssetKind) || strings.TrimSpace(p.AssetID) == "" {
		return errors.New("proposal needs id, asset kind and asset id")
	}
	var old evoProposalRow
	err := r.DB.Where("id = ?", p.ID).Take(&old).Error
	create := errors.Is(err, gorm.ErrRecordNotFound)
	if err != nil && !create {
		return err
	}
	if !create {
		if old.Revision != p.Revision || old.ProjectID != p.ProjectID || old.AssetKind != p.AssetKind || old.AssetID != p.AssetID {
			return ErrEvoStale
		}
		previous, ok := r.evoProposalFromRow(old, false)
		if !ok || (!previous.OccupiesAsset() && p.OccupiesAsset() && p.Status != domain.EvoProposalReleased) {
			return ErrEvoStale
		}
		if previous.Status != domain.EvoProposalDrafting && (p.CandidateContent != previous.CandidateContent || p.BaseHash != previous.BaseHash) {
			return ErrEvoStale
		}
	} else if p.Revision != 0 {
		return ErrEvoStale
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt, p.Revision = now, p.Revision+1
	p.Replays = nil
	row := evoProposalRow{ID: p.ID, ProjectID: p.ProjectID, Status: p.Status, AssetKind: p.AssetKind, AssetID: p.AssetID,
		Revision: p.Revision, Payload: mustJSON(p), Created: timePtr(p.CreatedAt), Updated: timePtr(now)}
	if create {
		if err := r.DB.Create(&row).Error; err != nil {
			return err
		}
	} else {
		result := r.DB.Model(&evoProposalRow{}).Where("id = ? AND revision = ?", p.ID, old.Revision).
			Updates(map[string]any{"status": p.Status, "revision": p.Revision, "payload": row.Payload, "updated_at": row.Updated})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrEvoStale
		}
	}
	if p.FindingID != "" {
		if f, ok := r.EvoFinding(p.FindingID); ok {
			if p.OccupiesAsset() && p.Status != domain.EvoProposalReleased {
				f.Status, f.ProposalID = domain.EvoFindingProposed, p.ID
			} else if f.ProposalID == p.ID {
				if p.Status == domain.EvoProposalReleased {
					f.Status = domain.EvoFindingResolved
				} else if p.Status == domain.EvoProposalFailed || p.Status == domain.EvoProposalRejected || p.Status == domain.EvoProposalRolledBack {
					f.Status, f.ProposalID = domain.EvoFindingOpen, ""
				}
			}
			if err := r.writeFinding(f); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *EvolutionRepo) evoProposalFromRow(row evoProposalRow, withReplays bool) (domain.EvoProposal, bool) {
	var p domain.EvoProposal
	if json.Unmarshal(row.Payload, &p) != nil {
		return domain.EvoProposal{}, false
	}
	p.Status, p.Revision = row.Status, row.Revision
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

func (r *EvolutionRepo) appendVersion(v domain.AssetVersion) (domain.AssetVersion, error) {
	var maxVersion sql.NullInt64
	if err := r.DB.Model(&assetVersionRow{}).Where("asset_kind = ? AND asset_id = ?", v.AssetKind, v.AssetID).
		Select("MAX(version)").Row().Scan(&maxVersion); err != nil {
		return v, err
	}
	v.Version, v.Hash, v.CreatedAt = int(maxVersion.Int64)+1, domain.ContentHash(v.Content), time.Now().UTC()
	err := r.DB.Create(&assetVersionRow{AssetKind: v.AssetKind, AssetID: v.AssetID, Version: v.Version, Hash: v.Hash,
		Source: v.Source, ProposalID: v.ProposalID, Payload: mustJSON(v), Created: timePtr(v.CreatedAt)}).Error
	return v, err
}

func (r *EvolutionRepo) versionLive(kind, id, content string, userID int64) (domain.AssetVersion, error) {
	latest, ok := r.LatestAssetVersion(kind, id)
	if ok && latest.Hash == domain.ContentHash(content) {
		return latest, nil
	}
	source := domain.EvoVersionBaseline
	if ok {
		source = domain.EvoVersionManual
	}
	return r.appendVersion(domain.AssetVersion{AssetKind: kind, AssetID: id, Content: content, Source: source, CreatedBy: userID})
}

// RecordAssetEdit snapshots an external (chatbot or manual) body edit into
// asset_versions immediately, seeding the previous body as baseline v1 first
// when no prior version exists so RSI can always render a before/after diff.
func (r *EvolutionRepo) RecordAssetEdit(kind, id, prevContent, nextContent, source, note string, userID int64) (domain.AssetVersion, bool, error) {
	if !domain.ValidEvoAssetKind(kind) || strings.TrimSpace(id) == "" {
		return domain.AssetVersion{}, false, nil
	}
	if domain.ContentHash(prevContent) == domain.ContentHash(nextContent) {
		return domain.AssetVersion{}, false, nil
	}
	if source != domain.EvoVersionChatbot {
		source = domain.EvoVersionManual
	}
	var saved domain.AssetVersion
	var recorded bool
	err := r.assetTx(kind, id, func(tr *EvolutionRepo) error {
		latest, ok := tr.LatestAssetVersion(kind, id)
		if !ok {
			if _, err := tr.appendVersion(domain.AssetVersion{
				AssetKind: kind, AssetID: id, Content: prevContent,
				Source: domain.EvoVersionBaseline, CreatedBy: userID,
			}); err != nil {
				return err
			}
		} else if latest.Hash == domain.ContentHash(nextContent) {
			saved = latest
			return nil
		}
		v, err := tr.appendVersion(domain.AssetVersion{
			AssetKind: kind, AssetID: id, Content: nextContent,
			Source: source, Note: domain.TrimEvoText(note, 200), CreatedBy: userID,
		})
		if err != nil {
			return err
		}
		saved = v
		recorded = true
		return nil
	})
	return saved, recorded, err
}

func (r *EvolutionRepo) ReleaseEvoProposal(expected domain.EvoProposal, userID int64, note string, force bool, minRuns int) (domain.EvoProposal, error) {
	out := expected
	err := r.assetTx(expected.AssetKind, expected.AssetID, func(tr *EvolutionRepo) error {
		current, busy, err := tr.currentAssetProposal(expected.AssetKind, expected.AssetID)
		if err != nil {
			return err
		}
		if busy && current.ID != expected.ID {
			return ErrEvoBusy
		}
		p, ok := tr.EvoProposal(expected.ID)
		if !ok || p.Revision != expected.Revision {
			return ErrEvoStale
		}
		allowed := p.Status == domain.EvoProposalPending || (p.Status == domain.EvoProposalCanary && p.Canary != nil && p.Canary.Status == domain.EvoCanaryPassed)
		if force && p.Status == domain.EvoProposalFailed {
			allowed = true
		}
		if !allowed || (!force && !p.Summary.Passed) || strings.TrimSpace(p.CandidateContent) == "" {
			return ErrEvoStale
		}
		content, raw, err := tr.catalog.evolutionAsset(tr.DB, p.AssetKind, p.AssetID)
		if err != nil {
			return err
		}
		if domain.ContentHash(content) != p.BaseHash || (p.BaseCatalogHash != "" && domain.ContentHash(string(raw)) != p.BaseCatalogHash) {
			return ErrEvoStale
		}
		previous, err := tr.versionLive(p.AssetKind, p.AssetID, content, userID)
		if err != nil {
			return err
		}
		if p.BaseVersion > 0 && previous.Version != p.BaseVersion {
			return ErrEvoStale
		}
		if err := tr.catalog.writeEvolutionAsset(tr.DB, p.AssetKind, p.AssetID, p.CandidateContent, raw); err != nil {
			return err
		}
		version, err := tr.appendVersion(domain.AssetVersion{AssetKind: p.AssetKind, AssetID: p.AssetID, Content: p.CandidateContent,
			Source: domain.EvoVersionEvolution, ProposalID: p.ID, Note: p.Title, CreatedBy: userID})
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if p.Canary != nil {
			p.Canary.Status, p.Canary.EndedAt = domain.EvoCanaryPromoted, now
		}
		p.Status, p.ReleasedVersion, p.DecidedBy, p.DecidedAt, p.DecisionNote = domain.EvoProposalReleased, version.Version, userID, now, note
		if force && !p.Summary.Passed {
			p.DecisionNote = strings.TrimSpace("平台管理员强制发布。" + note)
		}
		p.Watch = &domain.EvoWatch{Version: version.Version, Hash: version.Hash, PrevVersion: previous.Version, PrevHash: previous.Hash,
			MinRuns: minRuns, Status: domain.EvoWatchWatching, StartedAt: now, Verdict: "发布后观察中"}
		if err := tr.writeProposal(p); err != nil {
			return err
		}
		out, _ = tr.EvoProposal(p.ID)
		return nil
	})
	return out, err
}

func (r *EvolutionRepo) RollbackEvoAsset(kind, id, expectedHash string, targetVersion int, userID int64, note string) (domain.AssetVersion, error) {
	var out domain.AssetVersion
	err := r.assetTx(kind, id, func(tr *EvolutionRepo) error {
		current, busy, err := tr.currentAssetProposal(kind, id)
		if err != nil {
			return err
		}
		if busy && current.Open() {
			return ErrEvoBusy
		}
		target, ok := tr.AssetVersion(kind, id, targetVersion)
		if !ok {
			return ErrEvoStale
		}
		content, raw, err := tr.catalog.evolutionAsset(tr.DB, kind, id)
		if err != nil {
			return err
		}
		if domain.ContentHash(content) != expectedHash || domain.ContentHash(content) == target.Hash {
			return ErrEvoStale
		}
		if _, err := tr.versionLive(kind, id, content, userID); err != nil {
			return err
		}
		if err := tr.catalog.writeEvolutionAsset(tr.DB, kind, id, target.Content, raw); err != nil {
			return err
		}
		out, err = tr.appendVersion(domain.AssetVersion{AssetKind: kind, AssetID: id, Content: target.Content, Source: domain.EvoVersionRollback, Note: note, CreatedBy: userID})
		if err != nil {
			return err
		}
		var rows []evoProposalRow
		if err := tr.DB.Where("asset_kind = ? AND asset_id = ? AND status = ?", kind, id, domain.EvoProposalReleased).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			p, ok := tr.evoProposalFromRow(row, false)
			if !ok {
				return ErrEvoStale
			}
			if p.ReleasedVersion <= target.Version && !p.Watch.Active() {
				continue
			}
			p.Status, p.DecisionNote = domain.EvoProposalRolledBack, strings.TrimSpace(p.DecisionNote+"\n已回滚："+note)
			if p.Watch.Active() {
				p.Watch.Status, p.Watch.EndedAt = domain.EvoWatchRolledBack, time.Now().UTC()
			}
			if err := tr.writeProposal(p); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
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

// ---------- per-SOP evolution policies ----------

type evoSOPPolicyRow struct {
	ProjectID  string     `gorm:"column:project_id;primaryKey;size:64;index:idx_evo_sop_policies_project"`
	WorkflowID string     `gorm:"column:workflow_id;primaryKey;size:128"`
	Payload    []byte     `gorm:"column:payload;type:json"`
	Updated    *time.Time `gorm:"column:updated_at"`
}

func (evoSOPPolicyRow) TableName() string { return "evo_sop_policies" }

func (r *EvolutionRepo) EvoSOPPolicyStored(projectID, workflowID string) (domain.EvoSOPPolicy, bool) {
	projectID = strings.TrimSpace(projectID)
	workflowID = strings.TrimSpace(workflowID)
	if projectID == "" || workflowID == "" {
		return domain.EvoSOPPolicy{}, false
	}
	var row evoSOPPolicyRow
	if r.DB.Where("project_id = ? AND workflow_id = ?", projectID, workflowID).Take(&row).Error != nil {
		return domain.EvoSOPPolicy{}, false
	}
	var p domain.EvoSOPPolicy
	if json.Unmarshal(row.Payload, &p) != nil {
		return domain.EvoSOPPolicy{}, false
	}
	p.ProjectID = row.ProjectID
	p.WorkflowID = row.WorkflowID
	if p.BatchSize <= 0 {
		p.BatchSize = 10
	}
	return p, true
}

func (r *EvolutionRepo) EvoSOPPolicy(projectID, workflowID string) domain.EvoSOPPolicy {
	if p, ok := r.EvoSOPPolicyStored(projectID, workflowID); ok {
		return p
	}
	return domain.DefaultEvoSOPPolicy(projectID, workflowID)
}

func (r *EvolutionRepo) PutEvoSOPPolicy(p domain.EvoSOPPolicy) error {
	p.WorkflowID = strings.TrimSpace(p.WorkflowID)
	p.ProjectID = strings.TrimSpace(p.ProjectID)
	if p.WorkflowID == "" || p.ProjectID == "" {
		return errors.New("sop policy needs project id and workflow id")
	}
	if p.BatchSize <= 0 {
		p.BatchSize = 10
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	return upsert(r.DB, "evo_sop_policies", map[string]any{
		"workflow_id": p.WorkflowID,
		"project_id":  p.ProjectID,
		"payload":     mustJSON(p),
		"updated_at":  timePtr(p.UpdatedAt),
	})
}

func (r *EvolutionRepo) EvoSOPPolicies(projectID string) map[string]domain.EvoSOPPolicy {
	var rows []evoSOPPolicyRow
	out := map[string]domain.EvoSOPPolicy{}
	if r.DB.Where("project_id = ?", strings.TrimSpace(projectID)).Find(&rows).Error != nil {
		return out
	}
	for _, row := range rows {
		var p domain.EvoSOPPolicy
		if json.Unmarshal(row.Payload, &p) == nil {
			p.WorkflowID = row.WorkflowID
			p.ProjectID = row.ProjectID
			if p.BatchSize <= 0 {
				p.BatchSize = 10
			}
			out[row.WorkflowID] = p
		}
	}
	return out
}

func (r *EvolutionRepo) AllEnabledEvoSOPPolicies() []domain.EvoSOPPolicy {
	var rows []evoSOPPolicyRow
	if r.DB.Find(&rows).Error != nil {
		return nil
	}
	out := make([]domain.EvoSOPPolicy, 0, len(rows))
	for _, row := range rows {
		var p domain.EvoSOPPolicy
		if json.Unmarshal(row.Payload, &p) == nil && p.Enabled && strings.TrimSpace(row.WorkflowID) != "" && strings.TrimSpace(row.ProjectID) != "" {
			p.WorkflowID = row.WorkflowID
			p.ProjectID = row.ProjectID
			if p.BatchSize <= 0 {
				p.BatchSize = 10
			}
			out = append(out, p)
		}
	}
	return out
}
