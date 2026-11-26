package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type gitHTTPHostTrustHandlers struct {
	*deps
	hosts *repo.GitHTTPHostTrustRepo
}

func (h *gitHTTPHostTrustHandlers) list(c *gin.Context) {
	if !h.isPlatformAdmin(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以管理 Git HTTP 主机授权"})
		return
	}
	hosts, err := h.hosts.ListPlatformGitHTTPHosts(c.Request.Context())
	if err != nil {
		respond(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, hosts)
}

func (h *gitHTTPHostTrustHandlers) choices(c *gin.Context) {
	if user, ok := h.requestUser(c.Request, true); !ok || user.ID <= 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "请先登录"})
		return
	}
	hosts, err := h.hosts.ListPlatformGitHTTPHosts(c.Request.Context())
	if err != nil {
		respond(c, err)
		return
	}
	choices := make([]gin.H, 0, len(hosts))
	for _, host := range hosts {
		choices = append(choices, gin.H{"host": "http://" + host.Host, "platform": domain.GitPlatformOther})
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, choices)
}

func (h *gitHTTPHostTrustHandlers) save(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	var input struct {
		Host string `json:"host"`
	}
	if !decodeLimit(c, &input, 4<<10) {
		return
	}
	host, err := plainGitHTTPAuthority(input.Host)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	entry := repo.PlatformGitHTTPHost{Host: host}
	if err := h.hosts.CreatePlatformGitHTTPHost(c.Request.Context(), &entry); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			c.JSON(http.StatusConflict, gin.H{"error": "该 HTTP Git 主机已授权"})
		} else {
			respond(c, err)
		}
		return
	}
	c.Header("ETag", strconv.Quote(entry.Revision))
	c.JSON(http.StatusCreated, entry)
}

func (h *gitHTTPHostTrustHandlers) remove(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	host, err := plainGitHTTPAuthority(c.Param("host"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	revision, ok := readPlatformGitHostRevision(c)
	if !ok {
		return
	}
	if err := h.hosts.DeletePlatformGitHTTPHost(c.Request.Context(), host, revision); err != nil {
		if errors.Is(err, repo.ErrGitHTTPHostChanged) {
			c.JSON(http.StatusPreconditionFailed, gin.H{"code": "http_host_trust_changed", "error": "主机授权已变更，请刷新后重试"})
		} else {
			respond(c, err)
		}
		return
	}
	c.Status(http.StatusNoContent)
}

func plainGitHTTPAuthority(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		if strings.Count(raw, ":") > 1 && !strings.HasPrefix(raw, "[") {
			raw = "[" + raw + "]"
		}
		raw = "http://" + raw
	}
	endpoint, err := domain.ParseGitHTTPEndpoint(raw)
	if err != nil {
		return "", err
	}
	if endpoint.Scheme != "http" {
		return "", errors.New("请填写 HTTP Git 主机")
	}
	return endpoint.Authority, nil
}
