package repo

import (
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------- Projects ----------

func (r *ProjectsRepo) lockProjectInTx(tx *gorm.DB, id string) error {
	var row projectRow
	return r.locking(tx).Select("id").Where("id = ?", id).First(&row).Error
}

// Projects lists live projects; deleted (archived) ones are left out. Project
// still returns an archived project so its history and restore keep working.
func (r *ProjectsRepo) Projects() []domain.Project {
	var rows []projectRow
	if err := r.DB.Find(&rows).Error; err != nil {
		return []domain.Project{}
	}
	var memberRows []projectMemberRow
	members := map[string][]domain.ProjectMember{}
	if len(rows) > 0 {
		_ = r.DB.Order("user_id").Find(&memberRows).Error
		for _, member := range memberRows {
			members[member.ProjectID] = append(members[member.ProjectID], domain.ProjectMember{UserID: member.UserID, Role: member.Role})
		}
	}
	out := make([]domain.Project, 0, len(rows))
	for _, row := range rows {
		var project domain.Project
		if jsonUnmarshal(row.Payload, &project) != nil || project.ArchivedAt != nil {
			continue
		}
		if row.OwnerUserID != nil {
			project.OwnerID = *row.OwnerUserID
		} else {
			project.OwnerID = 0
		}
		project.BusinessDomainID = row.BusinessDomainID
		if row.Updated != nil {
			project.UpdatedAt = *row.Updated
		}
		project.Members = members[project.ID]
		if project.Members == nil {
			project.Members = []domain.ProjectMember{}
		}
		out = append(out, project)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *ProjectsRepo) Project(id string) (domain.Project, bool) {
	var row projectRow
	if err := r.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.Project{}, false
	}
	var project domain.Project
	if jsonUnmarshal(row.Payload, &project) != nil {
		return domain.Project{}, false
	}
	if row.OwnerUserID != nil {
		project.OwnerID = *row.OwnerUserID
	} else {
		project.OwnerID = 0
	}
	project.BusinessDomainID = row.BusinessDomainID
	if row.Updated != nil {
		project.UpdatedAt = *row.Updated
	}
	project.Members = r.projectMembers(project.ID)
	return project, true
}

// ProjectMemberships returns only project identity, ownership and membership
// for batched authorization. Missing projects are absent from the map.
func (r *ProjectsRepo) ProjectMemberships(ids []string) (map[string]domain.Project, error) {
	out := make(map[string]domain.Project, len(ids))
	for start := 0; start < len(ids); start += 500 {
		batch := ids[start:min(start+500, len(ids))]
		var rows []projectRow
		if err := r.DB.Select("id", "owner_user_id").Where("id IN ?", batch).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			project := domain.Project{ID: row.ID}
			if row.OwnerUserID != nil {
				project.OwnerID = *row.OwnerUserID
			}
			out[row.ID] = project
		}
		var members []projectMemberRow
		if err := r.DB.Select("project_id", "user_id").Where("project_id IN ?", batch).Find(&members).Error; err != nil {
			return nil, err
		}
		for _, member := range members {
			if project, ok := out[member.ProjectID]; ok {
				project.Members = append(project.Members, domain.ProjectMember{UserID: member.UserID})
				out[member.ProjectID] = project
			}
		}
	}
	return out, nil
}

func (r *ProjectsRepo) projectMembers(projectID string) []domain.ProjectMember {
	var rows []projectMemberRow
	if err := r.DB.Where("project_id = ?", projectID).Order("user_id").Find(&rows).Error; err != nil {
		return nil
	}
	members := make([]domain.ProjectMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, domain.ProjectMember{UserID: row.UserID, Role: row.Role})
	}
	return members
}

// SetProjectArchived deletes (at != nil) or restores (at == nil) a project
// without touching its settings, members or requirements.
func (r *ProjectsRepo) SetProjectArchived(id string, at *time.Time, by int64) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row projectRow
		if err := r.locking(tx).Where("id = ?", id).Take(&row).Error; err != nil {
			return err
		}
		payload := map[string]any{}
		if err := jsonUnmarshal(row.Payload, &payload); err != nil {
			return err
		}
		if at != nil {
			payload["archivedAt"] = at.UTC()
			payload["archivedBy"] = by
		} else {
			delete(payload, "archivedAt")
			delete(payload, "archivedBy")
		}
		return tx.Model(&projectRow{}).Where("id = ?", id).Updates(map[string]any{
			"payload": mustJSON(payload), "updated_at": timePtr(time.Now().UTC()),
		}).Error
	})
}

func (r *ProjectsRepo) PutProject(v domain.Project) error {
	now := time.Now().UTC()
	if v.Clarification.Threshold <= 0 {
		v.Clarification = domain.DefaultClarificationPolicy()
	}
	if old, ok := r.Project(v.ID); ok {
		if v.OwnerID == 0 {
			v.OwnerID = old.OwnerID
		}
		if v.Members == nil {
			v.Members = old.Members
		}
		v.CreatedAt = old.CreatedAt
	} else {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	return r.writeProject(v)
}

func (r *ProjectsRepo) writeProject(v domain.Project) error {
	payload := v
	payload.Members = nil
	payload.GitToken, payload.CodeBuddyAPIKey = "", ""
	var owner any
	if v.OwnerID > 0 {
		owner = v.OwnerID
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var current projectRow
		err := r.locking(tx).Where("id = ?", v.ID).Take(&current).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		// Only SetProjectBusinessDomain owns the assignment, even for stale project payloads.
		v.BusinessDomainID = current.BusinessDomainID
		payload.BusinessDomainID = current.BusinessDomainID
		if err := upsert(tx, "projects", map[string]any{
			"id": v.ID, "business_domain_id": v.BusinessDomainID, "project_key": v.Key, "name": v.Name, "owner_user_id": owner,
			"payload": mustJSON(payload), "created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
		}); err != nil {
			return err
		}
		if err := tx.Where("project_id = ?", v.ID).Delete(&projectMemberRow{}).Error; err != nil {
			return err
		}
		for _, member := range v.Members {
			if member.UserID <= 0 {
				continue
			}
			role := strings.TrimSpace(member.Role)
			if role == "" {
				role = "member"
			}
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&projectMemberRow{
				ProjectID: v.ID, UserID: member.UserID, Role: role, Created: timePtr(time.Now().UTC()),
			}).Error; err != nil {
				return err
			}
		}
		if v.OwnerID > 0 {
			// Backfill issue responsibility for issues that have no assignee.
			var issueRows []issueRow
			if err := tx.Where("project_id = ? AND assignee_user_id IS NULL", v.ID).Find(&issueRows).Error; err != nil {
				return err
			}
			for _, row := range issueRows {
				var issue domain.Issue
				if jsonUnmarshal(row.Payload, &issue) != nil {
					continue
				}
				issue.AssigneeUserID = v.OwnerID
				if err := tx.Model(&issueRow{}).Where("id = ?", row.ID).Updates(map[string]any{
					"assignee_user_id": v.OwnerID, "payload": mustJSON(issue),
				}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}
