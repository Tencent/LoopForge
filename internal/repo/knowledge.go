package repo

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	knowledgeSettingsID         = "default"
	knowledgeManagedBootstrapID = "kbbootstrap:managed"
	knowledgeBootstrapLockID    = "kbbootstrap:lock"
)

// KnowledgeRepo owns the upstream knowledge base connection and the review
// queue of distilled documents waiting to be written back.
type KnowledgeRepo struct {
	*deps
}

// KnowledgeSettings returns the stored settings including the upstream token.
// Callers that render API responses must use Public().
func (r *KnowledgeRepo) KnowledgeSettings() domain.KnowledgeSettings {
	var row knowledgeSettingsRow
	if err := r.DB.Where("id = ?", knowledgeSettingsID).Take(&row).Error; err != nil {
		return domain.KnowledgeSettings{}
	}
	var out domain.KnowledgeSettings
	_ = json.Unmarshal(row.Payload, &out)
	out.Token = ""
	if row.Token != nil {
		out.Token = *row.Token
	}
	out.TokenConfigured = strings.TrimSpace(out.Token) != ""
	if len(out.Connections) > 0 {
		for i := range out.Connections {
			var connRow knowledgeSettingsRow
			if err := r.DB.Where("id = ?", "kbconn:"+out.Connections[i].ID).Take(&connRow).Error; err == nil && connRow.Token != nil {
				out.Connections[i].Token = *connRow.Token
				out.Connections[i].TokenConfigured = strings.TrimSpace(*connRow.Token) != ""
			}
		}
	}
	if row.Updated != nil {
		out.UpdatedAt = *row.Updated
	}
	return out
}

func (r *KnowledgeRepo) PutKnowledgeSettings(settings domain.KnowledgeSettings) error {
	now := time.Now().UTC()
	token := strings.TrimSpace(settings.Token)
	settings.Token = ""
	settings.TokenConfigured = false
	settings.UpdatedAt = now

	connTokens := map[string]string{}
	if len(settings.Connections) > 0 {
		cleanConns := make([]domain.KnowledgeConnection, len(settings.Connections))
		for i, c := range settings.Connections {
			if tok := strings.TrimSpace(c.Token); tok != "" {
				connTokens[c.ID] = tok
			}
			c.Token = ""
			c.TokenConfigured = false
			cleanConns[i] = c
		}
		settings.Connections = cleanConns
	}

	payload, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	row := knowledgeSettingsRow{ID: knowledgeSettingsID, Payload: payload, Updated: &now}
	if token != "" {
		row.Token = &token
	}
	if err := r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"payload", "token", "updated_at"}),
	}).Create(&row).Error; err != nil {
		return err
	}
	for id, tok := range connTokens {
		cRow := knowledgeSettingsRow{ID: "kbconn:" + id, Token: &tok, Updated: &now}
		if err := r.DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"token", "updated_at"}),
		}).Create(&cRow).Error; err != nil {
			return err
		}
	}
	return nil
}

// MutateKnowledgeSettings reads the latest persisted settings inside a
// transaction, applies mutate, and writes the payload back when mutate reports
// a change. Allow-list updates therefore cannot overwrite a concurrent
// create/delete or an admin edit made from an older snapshot. Token columns are
// never touched here.
func (r *KnowledgeRepo) MutateKnowledgeSettings(mutate func(*domain.KnowledgeSettings) bool) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row knowledgeSettingsRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", knowledgeSettingsID).Take(&row).Error
		var settings domain.KnowledgeSettings
		switch {
		case err == nil:
			if err := json.Unmarshal(row.Payload, &settings); err != nil {
				return err
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			// No stored settings yet; mutate decides whether to create them.
		default:
			return err
		}
		legacyRowToken := ""
		if row.Token != nil {
			legacyRowToken = strings.TrimSpace(*row.Token)
		}
		hasLegacyToken := legacyRowToken != ""
		settings.Token = legacyRowToken
		settings.TokenConfigured = hasLegacyToken
		for i := range settings.Connections {
			settings.Connections[i].Token = ""
			settings.Connections[i].TokenConfigured = false
		}
		if !mutate(&settings) {
			return nil
		}
		now := time.Now().UTC()
		legacyToken := strings.TrimSpace(settings.Token)
		settings.Token = ""
		settings.TokenConfigured = false
		settings.UpdatedAt = now
		connTokens := map[string]string{}
		for i := range settings.Connections {
			if tok := strings.TrimSpace(settings.Connections[i].Token); tok != "" {
				connTokens[settings.Connections[i].ID] = tok
			}
			settings.Connections[i].Token = ""
			settings.Connections[i].TokenConfigured = false
		}
		payload, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		upRow := knowledgeSettingsRow{ID: knowledgeSettingsID, Payload: payload, Updated: &now}
		cols := []string{"payload", "updated_at"}
		if legacyToken != "" {
			upRow.Token = &legacyToken
			cols = append(cols, "token")
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns(cols),
		}).Create(&upRow).Error; err != nil {
			return err
		}
		for id, tok := range connTokens {
			t := tok
			cRow := knowledgeSettingsRow{ID: "kbconn:" + id, Token: &t, Updated: &now}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{"token", "updated_at"}),
			}).Create(&cRow).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// MutateKnowledgeSettingsIfFresh serializes first-install initialization across
// processes/replicas using a row lock and invokes mutate only when the
// knowledge_settings row has never been persisted or modified. Because mutate
// runs only for the single winning fresh install check, concurrent replicas do
// not mint duplicate orphan API keys on WeKnora, and an admin who later clears
// all connections (leaving an existing row with UpdatedAt set) is never
// overwritten.
func (r *KnowledgeRepo) MutateKnowledgeSettingsIfFresh(mutate func(*domain.KnowledgeSettings) (bool, error)) (bool, error) {
	var claimed bool
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		lockRow := knowledgeSettingsRow{ID: knowledgeBootstrapLockID, Payload: []byte("{}")}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoNothing: true,
		}).Create(&lockRow).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", knowledgeBootstrapLockID).Take(&lockRow).Error; err != nil {
			return err
		}

		var row knowledgeSettingsRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", knowledgeSettingsID).Take(&row).Error
		var settings domain.KnowledgeSettings
		rowExists := false
		switch {
		case err == nil:
			rowExists = true
			if err := json.Unmarshal(row.Payload, &settings); err != nil {
				return err
			}
			if row.Updated != nil && settings.UpdatedAt.IsZero() {
				settings.UpdatedAt = *row.Updated
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			// Truly fresh install: no settings row exists yet.
		default:
			return err
		}
		hasLegacyToken := row.Token != nil && strings.TrimSpace(*row.Token) != ""
		if rowExists || !settings.UpdatedAt.IsZero() || strings.TrimSpace(settings.UpdatedBy) != "" ||
			len(settings.Connections) > 0 || strings.TrimSpace(settings.URL) != "" || hasLegacyToken {
			return nil
		}

		ok, err := mutate(&settings)
		if err != nil || !ok {
			return err
		}
		now := time.Now().UTC()
		legacyToken := strings.TrimSpace(settings.Token)
		settings.Token = ""
		settings.TokenConfigured = false
		settings.UpdatedAt = now
		connTokens := map[string]string{}
		for i := range settings.Connections {
			if tok := strings.TrimSpace(settings.Connections[i].Token); tok != "" {
				connTokens[settings.Connections[i].ID] = tok
			}
			settings.Connections[i].Token = ""
			settings.Connections[i].TokenConfigured = false
		}
		payload, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		upRow := knowledgeSettingsRow{ID: knowledgeSettingsID, Payload: payload, Updated: &now}
		cols := []string{"payload", "updated_at"}
		if legacyToken != "" {
			upRow.Token = &legacyToken
			cols = append(cols, "token")
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns(cols),
		}).Create(&upRow).Error; err != nil {
			return err
		}
		for id, tok := range connTokens {
			t := tok
			cRow := knowledgeSettingsRow{ID: "kbconn:" + id, Token: &t, Updated: &now}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{"token", "updated_at"}),
			}).Create(&cRow).Error; err != nil {
				return err
			}
		}
		claimed = true
		return nil
	})
	return claimed, err
}

// DeleteKnowledgeConnectionToken removes the stored token row for a deleted connection.
func (r *KnowledgeRepo) DeleteKnowledgeConnectionToken(connID string) error {
	connID = strings.TrimSpace(connID)
	if connID == "" {
		return nil
	}
	return r.DB.Where("id = ?", "kbconn:"+connID).Delete(&knowledgeSettingsRow{}).Error
}

// ManagedWeKnoraAdminPassword returns the internally generated admin password
// for managed WeKnora instances, if one has been persisted.
func (r *KnowledgeRepo) ManagedWeKnoraAdminPassword() string {
	var row knowledgeSettingsRow
	if err := r.DB.Where("id = ?", knowledgeManagedBootstrapID).Take(&row).Error; err != nil || row.Token == nil {
		return ""
	}
	return *row.Token
}

// PutManagedWeKnoraAdminPassword persists the internally generated admin password
// for managed WeKnora instances without exposing it via KnowledgeSettings().
func (r *KnowledgeRepo) PutManagedWeKnoraAdminPassword(password string) error {
	now := time.Now().UTC()
	row := knowledgeSettingsRow{ID: knowledgeManagedBootstrapID, Token: &password, Updated: &now}
	return r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"token", "updated_at"}),
	}).Create(&row).Error
}

// KnowledgeSubmissions lists submissions newest first. Empty filters match
// everything; content is omitted to keep list payloads small.
func (r *KnowledgeRepo) KnowledgeSubmissions(projectID, status string, limit int) []domain.KnowledgeSubmission {
	out := make([]domain.KnowledgeSubmission, 0)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := r.DB.Model(&knowledgeSubmissionRow{})
	if projectID = strings.TrimSpace(projectID); projectID != "" {
		q = q.Where("project_id = ?", projectID)
	}
	if status = strings.TrimSpace(status); status != "" {
		q = q.Where("status = ?", status)
	}
	var rows []knowledgeSubmissionRow
	if err := q.Order("updated_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		item := knowledgeSubmissionFromRow(row)
		item.Content = ""
		out = append(out, item)
	}
	return out
}

func (r *KnowledgeRepo) KnowledgeSubmission(id string) (domain.KnowledgeSubmission, bool) {
	var row knowledgeSubmissionRow
	if err := r.DB.Where("id = ?", strings.TrimSpace(id)).Take(&row).Error; err != nil {
		return domain.KnowledgeSubmission{}, false
	}
	return knowledgeSubmissionFromRow(row), true
}

// KnowledgeSubmissionIDsByArtifact maps artifact ids to the submission that
// was created from them, so candidates can show "already queued".
func (r *KnowledgeRepo) KnowledgeSubmissionIDsByArtifact(artifactIDs []string) map[string]string {
	out := map[string]string{}
	if len(artifactIDs) == 0 {
		return out
	}
	var rows []knowledgeSubmissionRow
	if err := r.DB.Select("id", "artifact_id").Where("artifact_id IN ?", artifactIDs).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out[row.ArtifactID] = row.ID
	}
	return out
}

func (r *KnowledgeRepo) PutKnowledgeSubmission(item domain.KnowledgeSubmission) error {
	if strings.TrimSpace(item.ID) == "" {
		return errors.New("knowledge submission id is required")
	}
	now := time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	if item.Status == "" {
		item.Status = domain.KnowledgeSubmissionPending
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	created := item.CreatedAt
	row := knowledgeSubmissionRow{
		ID: item.ID, ProjectID: item.ProjectID, RunID: item.RunID, ArtifactID: item.ArtifactID,
		Status: item.Status, Payload: payload, Created: &created, Updated: &now,
	}
	return r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"project_id", "run_id", "artifact_id", "status", "payload", "updated_at"}),
	}).Create(&row).Error
}

func (r *KnowledgeRepo) DeleteKnowledgeSubmission(id string) error {
	res := r.DB.Where("id = ?", strings.TrimSpace(id)).Delete(&knowledgeSubmissionRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func knowledgeSubmissionFromRow(row knowledgeSubmissionRow) domain.KnowledgeSubmission {
	var out domain.KnowledgeSubmission
	_ = json.Unmarshal(row.Payload, &out)
	out.ID, out.ProjectID, out.RunID, out.ArtifactID, out.Status = row.ID, row.ProjectID, row.RunID, row.ArtifactID, row.Status
	if row.Created != nil {
		out.CreatedAt = *row.Created
	}
	if row.Updated != nil {
		out.UpdatedAt = *row.Updated
	}
	return out
}
