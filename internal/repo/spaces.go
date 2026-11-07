package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SpacesRepo stores conversation state layered on chat rooms: the linked
// requirement, members, versioned artifacts and threads.
type SpacesRepo struct {
	*deps
}

// ErrArtifactConflict reports an edit based on a stale artifact version.
var ErrArtifactConflict = errors.New("产物已被其他人更新，请刷新后再保存")

func (r *SpacesRepo) Space(id string) (domain.RequirementSpace, bool) {
	return payloadOne[domain.RequirementSpace](r.DB, "requirement_spaces", strings.TrimSpace(id))
}

func (r *SpacesRepo) PutSpace(v domain.RequirementSpace) error {
	now := time.Now().UTC()
	if old, ok := r.Space(v.ID); ok {
		v.CreatedAt = old.CreatedAt
	} else if v.CreatedAt.IsZero() {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	return upsert(r.DB, "requirement_spaces", map[string]any{
		"id": v.ID, "project_id": v.ProjectID, "issue_id": v.IssueID,
		"payload": mustJSON(v), "created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
	})
}

// SpaceByIssue finds the conversation that carries issueID.
func (r *SpacesRepo) SpaceByIssue(issueID string) (domain.RequirementSpace, bool) {
	var row requirementSpaceRow
	if issueID = strings.TrimSpace(issueID); issueID == "" {
		return domain.RequirementSpace{}, false
	}
	if err := r.DB.Table("requirement_spaces").Where("issue_id = ?", issueID).Order("created_at").Take(&row).Error; err != nil {
		return domain.RequirementSpace{}, false
	}
	var item domain.RequirementSpace
	if json.Unmarshal(row.Payload, &item) != nil {
		return domain.RequirementSpace{}, false
	}
	return item, true
}

func (r *SpacesRepo) SpaceMembers(spaceID string) []domain.SpaceMember {
	var rows []spaceMemberRow
	if err := r.DB.Table("space_members").Where("space_id = ?", strings.TrimSpace(spaceID)).Order("created_at").Find(&rows).Error; err != nil {
		return []domain.SpaceMember{}
	}
	out := make([]domain.SpaceMember, 0, len(rows))
	for _, row := range rows {
		var item domain.SpaceMember
		if json.Unmarshal(row.Payload, &item) == nil {
			out = append(out, item)
		}
	}
	return out
}

func (r *SpacesRepo) PutSpaceMember(v domain.SpaceMember) error {
	if v.AddedAt.IsZero() {
		v.AddedAt = time.Now().UTC()
	}
	return upsert(r.DB, "space_members", map[string]any{
		"space_id": v.SpaceID, "user_id": v.UserID, "payload": mustJSON(v), "created_at": timePtr(v.AddedAt),
	})
}

func (r *SpacesRepo) DeleteSpaceMember(spaceID string, userID int64) error {
	return r.DB.Exec("DELETE FROM space_members WHERE space_id = ? AND user_id = ?", spaceID, userID).Error
}

func (r *SpacesRepo) SpaceArtifacts(spaceID string) []domain.SpaceArtifact {
	var rows []spaceArtifactRow
	if err := r.DB.Table("space_artifacts").Where("space_id = ?", strings.TrimSpace(spaceID)).Order("created_at").Find(&rows).Error; err != nil {
		return []domain.SpaceArtifact{}
	}
	out := make([]domain.SpaceArtifact, 0, len(rows))
	for _, row := range rows {
		var item domain.SpaceArtifact
		if json.Unmarshal(row.Payload, &item) == nil {
			out = append(out, item)
		}
	}
	return out
}

func (r *SpacesRepo) SpaceArtifact(spaceID, id string) (domain.SpaceArtifact, bool) {
	item, ok := payloadOne[domain.SpaceArtifact](r.DB, "space_artifacts", strings.TrimSpace(id))
	if !ok || item.SpaceID != strings.TrimSpace(spaceID) {
		return domain.SpaceArtifact{}, false
	}
	return item, true
}

// PutSpaceArtifact stores a new version of v. baseVersion is the version the
// editor started from; 0 creates the artifact. Every version is kept.
func (r *SpacesRepo) PutSpaceArtifact(v domain.SpaceArtifact, baseVersion int) (domain.SpaceArtifact, error) {
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		var row spaceArtifactRow
		err := tx.Table("space_artifacts").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", v.ID).Take(&row).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if baseVersion != 0 {
				return ErrArtifactConflict
			}
			v.Version, v.CreatedAt = 1, now
		case err != nil:
			return err
		default:
			var old domain.SpaceArtifact
			if json.Unmarshal(row.Payload, &old) != nil || old.SpaceID != v.SpaceID {
				return fmt.Errorf("artifact %s belongs to another space", v.ID)
			}
			if baseVersion != old.Version {
				return ErrArtifactConflict
			}
			v.Version, v.CreatedAt = old.Version+1, old.CreatedAt
		}
		v.UpdatedAt = now
		if err := upsert(tx, "space_artifacts", map[string]any{
			"id": v.ID, "space_id": v.SpaceID, "kind": v.Kind, "version": v.Version,
			"payload": mustJSON(v), "created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
		}); err != nil {
			return err
		}
		return upsert(tx, "space_artifact_revisions", map[string]any{
			"artifact_id": v.ID, "version": v.Version, "space_id": v.SpaceID, "payload": mustJSON(v), "created_at": timePtr(now),
		})
	})
	return v, err
}

// SpaceArtifactRevisions lists every stored version, newest first, without
// bodies.
func (r *SpacesRepo) SpaceArtifactRevisions(spaceID, id string) []domain.SpaceArtifact {
	var rows []spaceArtifactRevisionRow
	if err := r.DB.Table("space_artifact_revisions").Where("artifact_id = ? AND space_id = ?", id, spaceID).Order("version desc").Find(&rows).Error; err != nil {
		return []domain.SpaceArtifact{}
	}
	out := make([]domain.SpaceArtifact, 0, len(rows))
	for _, row := range rows {
		var item domain.SpaceArtifact
		if json.Unmarshal(row.Payload, &item) == nil {
			item.Body = ""
			out = append(out, item)
		}
	}
	return out
}

func (r *SpacesRepo) SpaceArtifactRevision(spaceID, id string, version int) (domain.SpaceArtifact, bool) {
	var row spaceArtifactRevisionRow
	if err := r.DB.Table("space_artifact_revisions").Where("artifact_id = ? AND space_id = ? AND version = ?", id, spaceID, version).Take(&row).Error; err != nil {
		return domain.SpaceArtifact{}, false
	}
	var item domain.SpaceArtifact
	if json.Unmarshal(row.Payload, &item) != nil {
		return domain.SpaceArtifact{}, false
	}
	return item, true
}

// DeleteSpaceArtifact removes the artifact and its history.
func (r *SpacesRepo) DeleteSpaceArtifact(spaceID, id string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM space_artifact_revisions WHERE artifact_id = ? AND space_id = ?", id, spaceID).Error; err != nil {
			return err
		}
		return tx.Exec("DELETE FROM space_artifacts WHERE id = ? AND space_id = ?", id, spaceID).Error
	})
}

func (r *SpacesRepo) SpaceThreads(spaceID string) []domain.SpaceThread {
	var rows []spaceThreadRow
	if err := r.DB.Table("space_threads").Where("space_id = ?", strings.TrimSpace(spaceID)).Order("created_at").Find(&rows).Error; err != nil {
		return []domain.SpaceThread{}
	}
	out := make([]domain.SpaceThread, 0, len(rows))
	for _, row := range rows {
		var item domain.SpaceThread
		if json.Unmarshal(row.Payload, &item) == nil {
			out = append(out, item)
		}
	}
	return out
}

func (r *SpacesRepo) SpaceThread(spaceID, id string) (domain.SpaceThread, bool) {
	item, ok := payloadOne[domain.SpaceThread](r.DB, "space_threads", strings.TrimSpace(id))
	if !ok || item.SpaceID != strings.TrimSpace(spaceID) {
		return domain.SpaceThread{}, false
	}
	return item, true
}

// SpaceRunThread returns the thread attached to one dispatched run.
func (r *SpacesRepo) SpaceRunThread(spaceID, runID string) (domain.SpaceThread, bool) {
	var row spaceThreadRow
	if err := r.DB.Table("space_threads").Where("space_id = ? AND run_id = ?", spaceID, runID).Take(&row).Error; err != nil {
		return domain.SpaceThread{}, false
	}
	var item domain.SpaceThread
	if json.Unmarshal(row.Payload, &item) != nil {
		return domain.SpaceThread{}, false
	}
	return item, true
}

func (r *SpacesRepo) PutSpaceThread(v domain.SpaceThread) error {
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	return upsert(r.DB, "space_threads", map[string]any{
		"id": v.ID, "space_id": v.SpaceID, "run_id": v.RunID, "payload": mustJSON(v), "created_at": timePtr(v.CreatedAt),
	})
}
