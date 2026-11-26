package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

type mcpHandlers struct {
	*deps
}

func (h *mcpHandlers) mcpServers(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet:
		items := h.repo.MCPServers()
		if current, ok := h.requestUser(c.Request, true); ok {
			items = visibleMCPServersForUser(items, current.ID)
		} else {
			items = visibleMCPServersForUser(items, 0)
		}
		c.JSON(200, domain.PublicMCPServers(items))
	case http.MethodDelete:
		id := strings.TrimSpace(c.Query("id"))
		server, ok := h.repo.MCPServer(id)
		if !ok {
			c.JSON(http.StatusNotFound, map[string]string{"error": "MCP 不存在"})
			return
		}
		if !h.allowMCPServerWrite(c, server) {
			return
		}
		respond(c, h.repo.DeleteMCPServer(id))
	case http.MethodPut:
		var v domain.MCPServerDefinition
		if !decode(c, &v) {
			return
		}
		existing, found := h.repo.MCPServer(v.ID)
		if found {
			if !h.allowMCPServerWrite(c, existing) {
				return
			}
			if existing.MarketID != "" {
				c.JSON(http.StatusBadRequest, map[string]string{"error": "市场 MCP 不能直接修改，请移除后重新添加"})
				return
			}
			v.Scope, v.ProjectID, v.OwnerUserID = existing.Scope, existing.ProjectID, existing.OwnerUserID
			v.MarketID, v.IconURL = "", ""
		} else if !h.authorizeMCPDestination(c, &v) {
			return
		}
		saved, err := h.repo.SaveMCPServers(repo.MCPServerChange{Definition: v, Previous: existing})
		if err != nil {
			respondMCPWriteError(c, err)
			return
		}
		h.configureSync(h.repo.SyncConfig())
		c.JSON(http.StatusOK, domain.PublicMCPServer(saved[0]))
	}
}

// allowMCPServerWrite checks the scope of a stored definition.
func (d deps) allowMCPServerWrite(c *gin.Context, server domain.MCPServerDefinition) bool {
	if server.Scope == "personal" {
		user, ok := d.requestUser(c.Request, true)
		if ok && user.ID > 0 && user.ID == server.OwnerUserID {
			return true
		}
		c.JSON(http.StatusForbidden, gin.H{"error": "不能修改其他用户的个人 MCP"})
		return false
	}
	if project := strings.TrimSpace(server.ProjectID); project != "" {
		return d.requireProjectWriter(c, project)
	}
	return d.requirePlatformAdmin(c)
}

func (h *mcpHandlers) authorizeMCPDestination(c *gin.Context, server *domain.MCPServerDefinition) bool {
	server.Scope = strings.TrimSpace(server.Scope)
	if server.Scope != "personal" {
		return h.allowMCPServerWrite(c, *server)
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok || user.ID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "添加个人 MCP 前请先登录"})
		return false
	}
	server.OwnerUserID = user.ID
	return true
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
	changes := make([]repo.MCPServerChange, 0, len(imported))
	for i, item := range imported {
		item.Scope = strings.TrimSpace(item.Scope)
		existing, found := h.repo.MCPServer(item.ID)
		if found {
			if !h.allowMCPServerWrite(c, existing) {
				return
			}
			if (existing.Scope == "personal") != (item.Scope == "personal") {
				c.JSON(http.StatusConflict, gin.H{"error": "不能通过导入更改个人 MCP 的范围"})
				return
			}
		}
		if !h.authorizeMCPDestination(c, &item) {
			return
		}
		if err := domain.ValidateMCPServer(item); err != nil {
			c.JSON(400, map[string]string{"error": fmt.Sprintf("第 %d 项无效: %v", i+1, err)})
			return
		}
		changes = append(changes, repo.MCPServerChange{Definition: item, Previous: existing})
	}
	saved, err := h.repo.SaveMCPServers(changes...)
	if err != nil {
		respondMCPWriteError(c, err)
		return
	}
	h.configureSync(h.repo.SyncConfig())
	c.JSON(200, map[string]any{"imported": len(saved), "servers": domain.PublicMCPServers(saved)})
}

func respondMCPWriteError(c *gin.Context, err error) {
	if errors.Is(err, repo.ErrMCPServerChanged) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	respond(c, err)
}
