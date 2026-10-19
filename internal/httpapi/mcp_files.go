package httpapi

import (
	"fmt"
	"mime/multipart"
	"net/http"
	"path"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func (h *mcpHandlers) editableMCP(c *gin.Context, id string) (domain.MCPServerDefinition, bool) {
	server, ok := h.repo.MCPServer(id)
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "MCP 不存在"})
		return server, false
	}
	if server.MarketID != "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "市场 MCP 不能上传自定义程序"})
		return server, false
	}
	if server.Scope == "personal" {
		current, authenticated := h.requestUser(c.Request, true)
		if !authenticated || current.ID != server.OwnerUserID {
			c.JSON(http.StatusForbidden, map[string]string{"error": "不能修改其他用户的个人 MCP"})
			return server, false
		}
	}
	return server, true
}

// mcpFiles accepts a folder upload or ZIP and replaces the MCP source tree.
func (h *mcpHandlers) mcpFiles(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if _, ok := h.editableMCP(c, id); !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, skillUploadBodyLimit)
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		c.JSON(400, map[string]string{"error": "上传内容无效或超过 65MB 请求上限"})
		return
	}
	form, _ := c.MultipartForm()
	headers := form.File["files"]
	if len(headers) == 0 {
		c.JSON(400, map[string]string{"error": "缺少名为 files 的上传文件"})
		return
	}
	logical := form.Value["paths"]
	if len(logical) > 0 && len(logical) != len(headers) {
		c.JSON(400, map[string]string{"error": "paths 与 files 数量不一致"})
		return
	}
	type plainPart struct {
		path   string
		header *multipart.FileHeader
	}
	var plain []plainPart
	var inputs []repo.SkillFileInput
	seen := map[string]bool{}
	for i, header := range headers {
		name := header.Filename
		if i < len(logical) && strings.TrimSpace(logical[i]) != "" {
			name = logical[i]
		}
		if strings.EqualFold(path.Ext(name), ".zip") {
			items, err := readSkillZip(header, seen)
			if err != nil {
				c.JSON(400, map[string]string{"error": err.Error()})
				return
			}
			inputs = append(inputs, items...)
			continue
		}
		clean, err := cleanUploadPath(name)
		if err != nil {
			c.JSON(400, map[string]string{"error": err.Error()})
			return
		}
		plain = append(plain, plainPart{clean, header})
	}
	names := make([]string, len(plain))
	for i, p := range plain {
		names[i] = p.path
	}
	if _, nested := sharedTopDir(names); nested {
		for i := range plain {
			clean, err := cleanUploadPath(strings.Join(strings.Split(plain[i].path, "/")[1:], "/"))
			if err != nil {
				c.JSON(400, map[string]string{"error": err.Error()})
				return
			}
			plain[i].path = clean
		}
	}
	for _, part := range plain {
		if seen[part.path] {
			c.JSON(409, map[string]string{"error": fmt.Sprintf("上传中存在重复路径 %s", part.path)})
			return
		}
		seen[part.path] = true
		content, err := readBoundedPart(part.header)
		if err != nil {
			c.JSON(400, map[string]string{"error": err.Error()})
			return
		}
		inputs = append(inputs, repo.SkillFileInput{Path: part.path, Content: content})
	}
	var total int64
	for _, item := range inputs {
		total += int64(len(item.Content))
	}
	if len(inputs) > domain.MCPMaxFileCount || total > domain.MCPTotalMaxBytes {
		c.JSON(400, map[string]string{"error": "MCP 文件数量或总量超过限制"})
		return
	}
	if err := h.repo.PutMCPFiles(id, inputs); err != nil {
		respond(c, err)
		return
	}
	saved, _ := h.repo.MCPServer(id)
	c.JSON(200, domain.PublicMCPServer(saved))
}

func (h *mcpHandlers) mcpFile(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	filePath := strings.TrimPrefix(c.Param("path"), "/")
	if _, ok := h.editableMCP(c, id); !ok {
		return
	}
	if err := domain.ValidateRelativeFilePath(filePath); err != nil {
		c.JSON(400, map[string]string{"error": err.Error()})
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		content, ok := h.repo.MCPFileContent(id, filePath)
		if !ok {
			c.JSON(404, map[string]string{"error": "file not found"})
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", pathEscapeRFC5987(path.Base(filePath))))
		c.Data(200, "application/octet-stream", content)
	case http.MethodDelete:
		if err := h.repo.DeleteMCPFile(id, filePath); err != nil {
			respond(c, err)
			return
		}
		saved, _ := h.repo.MCPServer(id)
		c.JSON(200, domain.PublicMCPServer(saved))
	default:
		c.JSON(405, map[string]string{"error": "method not allowed"})
	}
}
