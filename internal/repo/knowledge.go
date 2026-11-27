package repo

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// KnowledgeRepo owns user-isolated knowledge connector configurations.
type KnowledgeRepo struct {
	*deps
}

// ListKnowledgeConnectors returns all connectors owned by ownerUserID,
// ordered from oldest to newest.
func (r *KnowledgeRepo) ListKnowledgeConnectors(ownerUserID int64) []domain.KnowledgeConnector {
	var rows []knowledgeConnectorRow
	if err := r.DB.Where("owner_user_id = ?", ownerUserID).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.KnowledgeConnector, 0, len(rows))
	for _, row := range rows {
		out = append(out, knowledgeConnectorFromRow(row))
	}
	return out
}

// GetKnowledgeConnector returns the connector identified by id only if it belongs
// to ownerUserID.
func (r *KnowledgeRepo) GetKnowledgeConnector(ownerUserID int64, id string) (domain.KnowledgeConnector, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.KnowledgeConnector{}, false
	}
	var row knowledgeConnectorRow
	if err := r.DB.Where("id = ? AND owner_user_id = ?", id, ownerUserID).Take(&row).Error; err != nil {
		return domain.KnowledgeConnector{}, false
	}
	return knowledgeConnectorFromRow(row), true
}

// PutKnowledgeConnector creates or updates a connector under conn.OwnerUserID.
// If another user already owns a connector with conn.ID, PutKnowledgeConnector
// refuses the write to prevent cross-user hijacking.
func (r *KnowledgeRepo) PutKnowledgeConnector(conn domain.KnowledgeConnector) error {
	conn.ID = strings.TrimSpace(conn.ID)
	if conn.ID == "" {
		return errors.New("knowledge connector id is required")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		query := tx
		if query.Dialector.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var existing knowledgeConnectorRow
		err := query.Where("id = ?", conn.ID).Take(&existing).Error
		switch {
		case err == nil:
			if existing.OwnerUserID != conn.OwnerUserID {
				return errors.New("knowledge connector id belongs to another user")
			}
			now := time.Now().UTC()
			createdAt := now
			if existing.CreatedAt != nil && !existing.CreatedAt.IsZero() {
				createdAt = *existing.CreatedAt
			}
			cfgBytes, err := marshalConnectorConfig(conn.Config)
			if err != nil {
				return err
			}
			secret := strings.TrimSpace(conn.SecretEnc)
			var secretPtr *string
			if secret != "" {
				secretPtr = &secret
			}
			updates := map[string]any{
				"name":        strings.TrimSpace(conn.Name),
				"type":        strings.TrimSpace(conn.Type),
				"endpoint":    strings.TrimSpace(conn.Endpoint),
				"config_json": cfgBytes,
				"secret_enc":  secretPtr,
				"enabled":     conn.Enabled,
				"updated_at":  &now,
				"created_at":  &createdAt,
			}
			return tx.Model(&knowledgeConnectorRow{}).Where("id = ? AND owner_user_id = ?", conn.ID, conn.OwnerUserID).Updates(updates).Error

		case errors.Is(err, gorm.ErrRecordNotFound):
			now := time.Now().UTC()
			createdAt := now
			if !conn.CreatedAt.IsZero() {
				createdAt = conn.CreatedAt.UTC()
			}
			cfgBytes, err := marshalConnectorConfig(conn.Config)
			if err != nil {
				return err
			}
			secret := strings.TrimSpace(conn.SecretEnc)
			var secretPtr *string
			if secret != "" {
				secretPtr = &secret
			}
			row := knowledgeConnectorRow{
				ID:          conn.ID,
				OwnerUserID: conn.OwnerUserID,
				Name:        strings.TrimSpace(conn.Name),
				Type:        strings.TrimSpace(conn.Type),
				Endpoint:    strings.TrimSpace(conn.Endpoint),
				ConfigJSON:  cfgBytes,
				SecretEnc:   secretPtr,
				Enabled:     conn.Enabled,
				CreatedAt:   &createdAt,
				UpdatedAt:   &now,
			}
			return tx.Create(&row).Error

		default:
			return err
		}
	})
}

// UpdateKnowledgeConnectorSecret compare-and-swaps the sealed credential of one
// connector. The caller passes the ciphertext it based its change on, so a
// concurrent refresh/authorization is never silently overwritten, and only
// secret_enc/updated_at are touched (name, endpoint, config stay untouched).
func (r *KnowledgeRepo) UpdateKnowledgeConnectorSecret(ctx context.Context, ownerUserID int64, id, expectedSecretEnc, secretEnc string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errors.New("knowledge connector id is required")
	}
	now := time.Now().UTC()
	secret := strings.TrimSpace(secretEnc)
	var secretPtr *string
	if secret != "" {
		secretPtr = &secret
	}
	res := r.DB.WithContext(ctx).
		Model(&knowledgeConnectorRow{}).
		Where("id = ? AND owner_user_id = ? AND COALESCE(secret_enc, '') = ?", id, ownerUserID, strings.TrimSpace(expectedSecretEnc)).
		Updates(map[string]any{"secret_enc": secretPtr, "updated_at": &now})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// UpdateKnowledgeConnectorConfig compare-and-swaps only config_json. The
// comparison is structural (decoded config), so JSON storage normalization does
// not produce false mismatches; name/endpoint/secret are never written.
func (r *KnowledgeRepo) UpdateKnowledgeConnectorConfig(ctx context.Context, ownerUserID int64, id, expectedConfigJSON, configJSON string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errors.New("knowledge connector id is required")
	}
	var updated bool
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.WithContext(ctx)
		if query.Dialector.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var row knowledgeConnectorRow
		err := query.Where("id = ? AND owner_user_id = ?", id, ownerUserID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !connectorConfigEqual(row.ConfigJSON, expectedConfigJSON) {
			return nil
		}
		if strings.TrimSpace(configJSON) == "" {
			configJSON = "{}"
		}
		now := time.Now().UTC()
		res := tx.Model(&knowledgeConnectorRow{}).
			Where("id = ? AND owner_user_id = ?", id, ownerUserID).
			Updates(map[string]any{"config_json": []byte(configJSON), "updated_at": &now})
		if res.Error != nil {
			return res.Error
		}
		updated = true
		return nil
	})
	return updated, err
}

func connectorConfigEqual(stored []byte, expectedJSON string) bool {
	var storedCfg, expectedCfg domain.KnowledgeConnectorConfig
	if len(stored) > 0 && string(stored) != "null" {
		if json.Unmarshal(stored, &storedCfg) != nil {
			return false
		}
	}
	if strings.TrimSpace(expectedJSON) == "" {
		expectedJSON = "{}"
	}
	if json.Unmarshal([]byte(expectedJSON), &expectedCfg) != nil {
		return false
	}
	normalizeConnectorConfig(&storedCfg)
	normalizeConnectorConfig(&expectedCfg)
	return reflect.DeepEqual(storedCfg, expectedCfg)
}

// normalizeConnectorConfig makes nil and empty collections compare equal; they
// are the same configuration semantically and JSON round-trips differ.
func normalizeConnectorConfig(cfg *domain.KnowledgeConnectorConfig) {
	if len(cfg.Headers) == 0 {
		cfg.Headers = nil
	}
	if len(cfg.DefaultSources) == 0 {
		cfg.DefaultSources = nil
	}
	if len(cfg.AllowedTools) == 0 {
		cfg.AllowedTools = nil
	}
	if len(cfg.Extra) == 0 {
		cfg.Extra = nil
	}
}

// DeleteKnowledgeConnector deletes id only when owned by ownerUserID.
func (r *KnowledgeRepo) DeleteKnowledgeConnector(ownerUserID int64, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	res := r.DB.Where("id = ? AND owner_user_id = ?", id, ownerUserID).Delete(&knowledgeConnectorRow{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func marshalConnectorConfig(cfg domain.KnowledgeConnectorConfig) ([]byte, error) {
	return json.Marshal(cfg)
}

func knowledgeConnectorFromRow(row knowledgeConnectorRow) domain.KnowledgeConnector {
	var cfg domain.KnowledgeConnectorConfig
	if len(row.ConfigJSON) > 0 && string(row.ConfigJSON) != "null" {
		_ = json.Unmarshal(row.ConfigJSON, &cfg)
	}
	secretEnc := ""
	if row.SecretEnc != nil {
		secretEnc = strings.TrimSpace(*row.SecretEnc)
	}
	out := domain.KnowledgeConnector{
		ID:               row.ID,
		OwnerUserID:      row.OwnerUserID,
		Name:             row.Name,
		Type:             row.Type,
		Endpoint:         row.Endpoint,
		Config:           cfg,
		SecretEnc:        secretEnc,
		SecretConfigured: secretEnc != "",
		Enabled:          row.Enabled,
	}
	if row.CreatedAt != nil {
		out.CreatedAt = *row.CreatedAt
	}
	if row.UpdatedAt != nil {
		out.UpdatedAt = *row.UpdatedAt
	}
	return out
}
