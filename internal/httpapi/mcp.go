package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

type mcpHandlers struct {
	*deps
}

func (h *mcpHandlers) mcpServers(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		items := h.repo.MCPServers()
		if current, ok := h.requestUser(c.Request, true); ok {
			items = visibleMCPServersForUser(items, current.ID)
		} else {
			items = visibleMCPServersForUser(items, 0)
		}
		c.JSON(200, domain.PublicMCPServers(items))
		return
	}
	if c.Request.Method == http.MethodDelete {
		id := strings.TrimSpace(c.Query("id"))
		server, ok := h.repo.MCPServer(id)
		if !ok {
			c.JSON(http.StatusNotFound, map[string]string{"error": "MCP 不存在"})
			return
		}
		if server.Scope == "personal" {
			current, authenticated := h.requestUser(c.Request, true)
			if !authenticated || current.ID != server.OwnerUserID {
				c.JSON(http.StatusForbidden, map[string]string{"error": "不能移除其他用户的个人 MCP"})
				return
			}
		}
		respond(c, h.repo.DeleteMCPServer(id))
		return
	}
	if c.Request.Method == http.MethodPut {
		var v domain.MCPServerDefinition
		if decode(c, &v) && v.ID != "" {
			existing, ok := h.repo.MCPServer(v.ID)
			domain.RestoreMaskedMCPSecrets(&v, existing)
			if ok {
				if existing.MarketID != "" {
					c.JSON(http.StatusBadRequest, map[string]string{"error": "市场 MCP 不能直接修改，请移除后重新添加"})
					return
				}
				if existing.Scope == "personal" {
					current, authenticated := h.requestUser(c.Request, true)
					if !authenticated || current.ID != existing.OwnerUserID {
						c.JSON(http.StatusForbidden, map[string]string{"error": "不能修改其他用户的个人 MCP"})
						return
					}
				}
				v.Scope, v.ProjectID, v.OwnerUserID = existing.Scope, existing.ProjectID, existing.OwnerUserID
				v.MarketID, v.IconURL = "", ""
				// File contents are managed by the multipart endpoints. Ordinary
				// definition edits must not erase their manifest.
				if v.FileRefs == nil {
					v.FileRefs = existing.FileRefs
				}
			}
			if err := h.repo.PutMCPServer(v); err != nil {
				respond(c, err)
				return
			}
			h.configureSync(h.repo.SyncConfig())
			saved, _ := h.repo.MCPServer(v.ID)
			c.JSON(http.StatusOK, domain.PublicMCPServer(saved))
		}
	}
}
func (h *mcpHandlers) mcpImport(c *gin.Context) {
	var raw json.RawMessage
	if !decode(c, &raw) {
		return
	}
	imported, err := domain.ParseMCPImport(raw)
	if err != nil {
		c.JSON(400, map[string]string{"error": err.Error()})
		return
	}
	for _, item := range imported {
		target := strings.TrimSpace(item.ProjectID)
		if !h.allowStoredAsset(c, "mcp-servers", strings.TrimSpace(item.ID)) {
			return
		}
		if target == "" && item.Scope != "personal" {
			if !h.requirePlatformAdmin(c) {
				return
			}
		} else if !h.requireProjectWriter(c, target) {
			return
		}
	}
	saved := make([]domain.MCPServerDefinition, 0, len(imported))
	for i, item := range imported {
		if err := domain.ValidateMCPServer(item); err != nil {
			c.JSON(400, map[string]string{"error": fmt.Sprintf("第 %d 项无效: %v", i+1, err)})
			return
		}
		if err := h.repo.PutMCPServer(item); err != nil {
			respond(c, err)
			return
		}
		saved = append(saved, item)
	}
	h.configureSync(h.repo.SyncConfig())
	c.JSON(200, map[string]any{"imported": len(saved), "servers": domain.PublicMCPServers(saved)})
}
