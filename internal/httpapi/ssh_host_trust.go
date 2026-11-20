package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"agentflow/internal/gitwork"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type sshHostTrustHandlers struct {
	*deps
	hosts *repo.SSHHostTrustRepo
}

type platformSSHHostView struct {
	repo.PlatformGitSSHHost
	Fingerprints []string `json:"fingerprints"`
}

func (h *sshHostTrustHandlers) list(c *gin.Context) {
	if !h.isPlatformAdmin(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以管理 Git SSH 主机信任"})
		return
	}
	hosts, err := h.hosts.ListPlatformGitSSHHosts(c.Request.Context())
	if err != nil {
		respond(c, err)
		return
	}
	views := make([]platformSSHHostView, 0, len(hosts))
	for _, host := range hosts {
		_, fingerprints, err := gitwork.NormalizeSSHHostPublicKeys(host.PublicKeys)
		if err != nil {
			respond(c, err)
			return
		}
		views = append(views, platformSSHHostView{PlatformGitSSHHost: host, Fingerprints: fingerprints})
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, views)
}

func readPlatformSSHHost(c *gin.Context) (platformSSHHostView, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*gitwork.MaxSSHHostPublicKeyBytes)
	var input repo.PlatformGitSSHHost
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 Git SSH 主机信任配置"})
		return platformSSHHostView{}, false
	}
	if pathHost := c.Param("host"); pathHost != "" {
		input.Host = pathHost
	}
	host, err := gitwork.SSHHostScope(input.Host)
	if err != nil || len(host) > 260 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入 SSH 主机名，可附带端口"})
		return platformSSHHostView{}, false
	}
	keys, fingerprints, err := gitwork.NormalizeSSHHostPublicKeys(input.PublicKeys)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return platformSSHHostView{}, false
	}
	entry := repo.PlatformGitSSHHost{Host: host, PublicKeys: keys}
	return platformSSHHostView{PlatformGitSSHHost: entry, Fingerprints: fingerprints}, true
}

func (h *sshHostTrustHandlers) preview(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	if entry, ok := readPlatformSSHHost(c); ok {
		c.JSON(http.StatusOK, entry)
	}
}

func readPlatformSSHHostRevision(c *gin.Context) (string, bool) {
	value := strings.TrimSpace(c.GetHeader("If-Match"))
	if value == "" {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "请重新打开主机信任记录后再操作"})
		return "", false
	}
	revision, err := strconv.Unquote(value)
	if err != nil || revision == "" || len(revision) > 64 || value != strconv.Quote(revision) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的主机信任记录版本"})
		return "", false
	}
	return revision, true
}

func respondPlatformSSHHostWriteError(c *gin.Context, err error) {
	if errors.Is(err, repo.ErrPlatformGitSSHHostChanged) {
		c.JSON(http.StatusPreconditionFailed, gin.H{
			"code":  "ssh_host_trust_changed",
			"error": "主机信任记录已变更，请重新打开记录并核对公钥",
		})
		return
	}
	respond(c, err)
}

func (h *sshHostTrustHandlers) save(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	entry, ok := readPlatformSSHHost(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if c.Request.Method == http.MethodPost {
		err := h.hosts.CreatePlatformGitSSHHost(ctx, &entry.PlatformGitSSHHost)
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			c.JSON(http.StatusConflict, gin.H{"error": "该主机已登记，请更新现有记录"})
			return
		}
		if err != nil {
			respond(c, err)
			return
		}
		c.Header("ETag", strconv.Quote(entry.Revision))
		c.JSON(http.StatusCreated, entry)
		return
	}
	revision, ok := readPlatformSSHHostRevision(c)
	if !ok {
		return
	}
	if err := h.hosts.UpdatePlatformGitSSHHost(ctx, &entry.PlatformGitSSHHost, revision); err != nil {
		respondPlatformSSHHostWriteError(c, err)
		return
	}
	c.Header("ETag", strconv.Quote(entry.Revision))
	c.JSON(http.StatusOK, entry)
}

func (h *sshHostTrustHandlers) remove(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	host, err := gitwork.SSHHostScope(c.Param("host"))
	if err != nil || len(host) > 260 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入 SSH 主机名，可附带端口"})
		return
	}
	revision, ok := readPlatformSSHHostRevision(c)
	if !ok {
		return
	}
	if err := h.hosts.DeletePlatformGitSSHHost(c.Request.Context(), host, revision); err != nil {
		respondPlatformSSHHostWriteError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
