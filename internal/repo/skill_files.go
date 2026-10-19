package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

// SkillFileInput is one binary file to persist for a skill.
type SkillFileInput struct {
	Path    string
	Content []byte
	Mode    uint32
}

// SkillFileRefs returns the binary manifest of a skill, sorted by path.
func (r *CatalogRepo) SkillFileRefs(skillID string) []domain.SkillFileRef {
	var rows []skillFileRow
	if err := r.DB.Where("skill_id = ?", skillID).Order("path").Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.SkillFileRef, 0, len(rows))
	for _, row := range rows {
		ref := domain.SkillFileRef{Path: row.Path, Size: row.Size, SHA256: row.SHA256, Mode: row.UnixMode}
		if row.Updated != nil {
			ref.UpdatedAt = *row.Updated
		}
		out = append(out, ref)
	}
	return out
}

// SkillFileContent loads one binary file of a skill.
func (r *CatalogRepo) SkillFileContent(skillID, path string) ([]byte, bool) {
	var row skillFileRow
	if err := r.DB.Where("skill_id = ? AND path = ?", skillID, path).Take(&row).Error; err != nil {
		return nil, false
	}
	return row.Content, true
}

// SkillFileContents loads every binary file of a skill (path -> bytes).
func (r *CatalogRepo) SkillFileContents(skillID string) map[string][]byte {
	var rows []skillFileRow
	if err := r.DB.Where("skill_id = ?", skillID).Find(&rows).Error; err != nil {
		return nil
	}
	out := make(map[string][]byte, len(rows))
	for _, row := range rows {
		out[row.Path] = row.Content
	}
	return out
}

// SkillFileTotalBytes returns the combined size of the binary files.
func (r *CatalogRepo) SkillFileTotalBytes(skillID string) int64 {
	var total int64
	if err := r.DB.Table("skill_files").Where("skill_id = ?", skillID).
		Select("COALESCE(SUM(size),0)").Scan(&total).Error; err != nil {
		return 0
	}
	return total
}

// PutSkillFiles stores the given binary files as the complete file set of
// the skill (replace semantics): existing rows for the skill are removed
// before the new ones are inserted, all inside one transaction. The skill
// payload manifest is refreshed from the resulting rows.
func (r *CatalogRepo) PutSkillFiles(skillID string, files []SkillFileInput) error {
	if skillID == "" {
		return errors.New("skill id is required")
	}
	if len(files) == 0 {
		return errors.New("no skill files to store")
	}
	now := time.Now().UTC()
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("skill_id = ?", skillID).Delete(&skillFileRow{}).Error; err != nil {
			return err
		}
		for _, file := range files {
			if err := writeSkillFileRow(tx, skillID, file, now); err != nil {
				return err
			}
		}
		return refreshSkillFileRefs(tx, skillID, now)
	})
}

func writeSkillFileRow(tx *gorm.DB, skillID string, file SkillFileInput, now time.Time) error {
	if err := domain.ValidateRelativeFilePath(file.Path); err != nil {
		return err
	}
	if int64(len(file.Content)) > domain.SkillFileMaxBytes {
		return fmt.Errorf("skill 文件 %s 超过 1MB 限制", file.Path)
	}
	sum := sha256.Sum256(file.Content)
	return upsert(tx, "skill_files", map[string]any{
		"skill_id":   skillID,
		"path":       file.Path,
		"content":    file.Content,
		"size":       int64(len(file.Content)),
		"sha256":     hex.EncodeToString(sum[:]),
		"unix_mode":  domain.NormalizeSkillFileMode(file.Mode),
		"updated_at": &now,
	})
}

// DeleteSkillFile removes one binary file and refreshes the manifest.
func (r *CatalogRepo) DeleteSkillFile(skillID, path string) error {
	if skillID == "" || path == "" {
		return errors.New("skill id and path are required")
	}
	now := time.Now().UTC()
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("skill_id = ? AND path = ?", skillID, path).Delete(&skillFileRow{}).Error; err != nil {
			return err
		}
		return refreshSkillFileRefs(tx, skillID, now)
	})
}

// DeleteSkillFiles removes every binary file of a skill without touching the
// payload manifest (used when the whole skill row goes away).
func (r *CatalogRepo) DeleteSkillFiles(skillID string) error {
	return r.DB.Where("skill_id = ?", skillID).Delete(&skillFileRow{}).Error
}

// refreshSkillFileRefs rewrites the fileRefs manifest on the skill payload
// from the skill_files table, which is the source of truth.
func refreshSkillFileRefs(tx *gorm.DB, skillID string, now time.Time) error {
	skill, ok := payloadOne[domain.SkillDefinition](tx, "skills", skillID)
	if !ok {
		return fmt.Errorf("skill %s not found", skillID)
	}
	var rows []skillFileRow
	if err := tx.Where("skill_id = ?", skillID).Order("path").Find(&rows).Error; err != nil {
		return err
	}
	refs := make([]domain.SkillFileRef, 0, len(rows))
	for _, row := range rows {
		ref := domain.SkillFileRef{Path: row.Path, Size: row.Size, SHA256: row.SHA256, Mode: row.UnixMode}
		if row.Updated != nil {
			ref.UpdatedAt = *row.Updated
		}
		refs = append(refs, ref)
	}
	skill.FileRefs = refs
	skill.UpdatedAt = now
	return upsert(tx, "skills", map[string]any{
		"id": skill.ID, "name": skill.Name, "payload": mustJSON(skill), "updated_at": timePtr(now),
	})
}
