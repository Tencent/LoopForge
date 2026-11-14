package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/forge"

	"github.com/gin-gonic/gin"
)

func (h *usersHandlers) forgeTokens(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid user id is required"})
		return
	}
	if user, ok := h.requestUser(c.Request, false); !ok || user.ID != id {
		c.JSON(http.StatusForbidden, gin.H{"error": "只能管理当前登录用户的 Forgejo 令牌"})
		return
	}
	c.Header("Cache-Control", "no-store")
	switch c.Request.Method {
	case http.MethodPut:
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
		var input struct {
			Instance string `json:"instance"`
			Token    string `json:"token"`
		}
		if !decode(c, &input) {
			return
		}
		instance, ok := domain.NormalizeForgeInstance(input.Instance)
		if !ok || !forge.Allows(instance) {
			c.JSON(http.StatusBadRequest, gin.H{"error": forge.ErrInstanceNotAllowed.Error()})
			return
		}
		token := strings.TrimSpace(input.Token)
		if !forge.ValidToken(token) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请输入有效的 Forgejo 访问令牌"})
			return
		}
		if err := forge.VerifyToken(c.Request.Context(), instance, token); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := h.repo.SaveForgeToken(id, instance, token); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "无法保存访问令牌"})
			return
		}
	case http.MethodDelete:
		if err := h.repo.DeleteForgeToken(id, c.Param("tokenID")); err != nil {
			respond(c, err)
			return
		}
	}
	tokens, err := h.repo.ForgeTokenDescriptions(id)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"instances": forge.Instances(), "tokens": tokens})
}
