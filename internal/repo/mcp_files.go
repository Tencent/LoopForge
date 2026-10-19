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

func (r *CatalogRepo) MCPFileRefs(mcpID string) []domain.MCPFileRef {
	var rows []mcpFileRow
	if err := r.DB.Where("mcp_id = ?", mcpID).Order("path").Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.MCPFileRef, 0, len(rows))
	for _, row := range rows {
		ref := domain.MCPFileRef{Path: row.Path, Size: row.Size, SHA256: row.SHA256, Mode: row.UnixMode}
		if row.Updated != nil {
			ref.UpdatedAt = *row.Updated
		}
		out = append(out, ref)
	}
	return out
}

func (r *CatalogRepo) MCPFileContent(mcpID, filePath string) ([]byte, bool) {
	var row mcpFileRow
	if err := r.DB.Where("mcp_id = ? AND path = ?", mcpID, filePath).Take(&row).Error; err != nil {
		return nil, false
	}
	return row.Content, true
}

func (r *CatalogRepo) MCPFileContents(mcpID string) map[string][]byte {
	var rows []mcpFileRow
	if err := r.DB.Where("mcp_id = ?", mcpID).Find(&rows).Error; err != nil {
		return nil
	}
	out := make(map[string][]byte, len(rows))
	for _, row := range rows {
		out[row.Path] = row.Content
	}
	return out
}

// PutMCPFiles stores one complete uploaded stdio MCP source tree.
func (r *CatalogRepo) PutMCPFiles(mcpID string, files []SkillFileInput) error {
	if mcpID == "" {
		return errors.New("MCP id is required")
	}
	if len(files) == 0 {
		return errors.New("no MCP files to store")
	}
	now := time.Now().UTC()
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if _, ok := payloadOne[domain.MCPServerDefinition](tx, "mcp_servers", mcpID); !ok {
			return fmt.Errorf("MCP %s not found", mcpID)
		}
		if err := tx.Where("mcp_id = ?", mcpID).Delete(&mcpFileRow{}).Error; err != nil {
			return err
		}
		for _, file := range files {
			if err := domain.ValidateRelativeFilePath(file.Path); err != nil {
				return err
			}
			if int64(len(file.Content)) > domain.MCPFileMaxBytes {
				return fmt.Errorf("MCP 文件 %s 超过 1MB 限制", file.Path)
			}
			sum := sha256.Sum256(file.Content)
			if err := upsert(tx, "mcp_files", map[string]any{
				"mcp_id": mcpID, "path": file.Path, "content": file.Content,
				"size": int64(len(file.Content)), "sha256": hex.EncodeToString(sum[:]),
				"unix_mode": domain.NormalizeSkillFileMode(file.Mode), "updated_at": &now,
			}); err != nil {
				return err
			}
		}
		return refreshMCPFileRefs(tx, mcpID, now)
	})
}

func (r *CatalogRepo) DeleteMCPFile(mcpID, filePath string) error {
	if mcpID == "" || filePath == "" {
		return errors.New("MCP id and path are required")
	}
	now := time.Now().UTC()
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("mcp_id = ? AND path = ?", mcpID, filePath).Delete(&mcpFileRow{}).Error; err != nil {
			return err
		}
		return refreshMCPFileRefs(tx, mcpID, now)
	})
}

func refreshMCPFileRefs(tx *gorm.DB, mcpID string, now time.Time) error {
	server, ok := payloadOne[domain.MCPServerDefinition](tx, "mcp_servers", mcpID)
	if !ok {
		return fmt.Errorf("MCP %s not found", mcpID)
	}
	var rows []mcpFileRow
	if err := tx.Where("mcp_id = ?", mcpID).Order("path").Find(&rows).Error; err != nil {
		return err
	}
	refs := make([]domain.MCPFileRef, 0, len(rows))
	for _, row := range rows {
		ref := domain.MCPFileRef{Path: row.Path, Size: row.Size, SHA256: row.SHA256, Mode: row.UnixMode}
		if row.Updated != nil {
			ref.UpdatedAt = *row.Updated
		}
		refs = append(refs, ref)
	}
	server.FileRefs, server.UpdatedAt = refs, now
	return upsert(tx, "mcp_servers", map[string]any{"id": server.ID, "name": server.Name, "payload": mustJSON(server), "updated_at": timePtr(now)})
}
