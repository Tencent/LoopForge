package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"agentflow/internal/gitwork"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (h *usersHandlers) gitSSHKeys(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid user id is required"})
		return
	}
	if user, ok := h.requestUser(c.Request, false); !ok || user.ID != id {
		c.JSON(http.StatusForbidden, gin.H{"error": "只能管理当前登录用户的 Git SSH 密钥"})
		return
	}
	switch c.Request.Method {
	case http.MethodPut:
		if !h.saveGitSSHKey(c, id) {
			return
		}
	case http.MethodDelete:
		if err := h.repo.DeleteGitSSHKey(id, c.Param("keyID")); err != nil {
			respond(c, err)
			return
		}
	}
	keys, err := h.repo.GitSSHKeyDescriptions(id)
	if err != nil {
		respond(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, keys)
}

// saveGitSSHKey validates the PUT body and stores the key. It returns false
// after writing an error response.
func (h *usersHandlers) saveGitSSHKey(c *gin.Context, userID int64) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var input struct {
		ID         string `json:"id"`
		Host       string `json:"host"`
		Name       string `json:"name"`
		PrivateKey string `json:"privateKey"`
	}
	if !decode(c, &input) {
		return false
	}
	host, err := gitwork.SSHHostScope(input.Host)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return false
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入 1 到 80 个字符的密钥名称"})
		return false
	}
	privateKey := strings.TrimSpace(input.PrivateKey)
	if privateKey != "" {
		privateKey, err = gitwork.NormalizeSSHPrivateKey(privateKey)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return false
		}
	}
	if err := h.repo.SaveGitSSHKey(userID, input.ID, host, name, privateKey); err != nil {
		if errors.Is(err, gitwork.ErrSSHKeyRequired) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "添加密钥时请粘贴私钥"})
			return false
		}
		if errors.Is(err, repo.ErrGitSSHKeyExists) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return false
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "此主机的密钥已不存在"})
			return false
		}
		respond(c, err)
		return false
	}
	return true
}
