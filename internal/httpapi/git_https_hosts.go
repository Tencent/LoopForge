package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type httpsHostTrustHandlers struct {
	*deps
	hosts    *repo.HTTPSHostTrustRepo
	lookupIP func(context.Context, string, string) ([]netip.Addr, error)
}

func (h *httpsHostTrustHandlers) list(c *gin.Context) {
	if !h.isPlatformAdmin(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以管理 Git HTTPS 主机授权"})
		return
	}
	hosts, err := h.hosts.ListPlatformGitHTTPSHosts(c.Request.Context())
	if err != nil {
		respond(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, hosts)
}

func (h *httpsHostTrustHandlers) choices(c *gin.Context) {
	if user, ok := h.requestUser(c.Request, true); !ok || user.ID <= 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "请先登录"})
		return
	}
	hosts, err := h.hosts.GitHTTPSHostChoices(c.Request.Context())
	if err != nil {
		respond(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, hosts)
}

func readGitHTTPSHost(c *gin.Context) (repo.PlatformGitHTTPSHost, bool) {
	var entry repo.PlatformGitHTTPSHost
	if !decodeLimit(c, &entry, 16<<10) {
		return entry, false
	}
	if host := c.Param("host"); host != "" {
		entry.Host = host
	}
	host, err := gitwork.HTTPSHostScope(entry.Host)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return entry, false
	}
	platform, ok := domain.NormalizeGitPlatform(entry.Platform)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择 Git 托管平台"})
		return entry, false
	}
	entry.Host, entry.Platform, entry.Revision = host, platform, ""
	return entry, true
}

func approvedGitHTTPSAddresses(host string, addresses []netip.Addr) ([]netip.Addr, error) {
	if len(addresses) == 0 {
		return nil, errors.New("请确认该 Git 主机允许连接的 IP 地址")
	}
	for i, address := range addresses {
		ip := address.Unmap()
		if !ip.IsGlobalUnicast() || ip.Zone() != "" {
			return nil, errors.New("Git HTTPS 主机授权需要可路由的单播 IP 地址")
		}
		addresses[i] = ip
	}
	slices.SortFunc(addresses, netip.Addr.Compare)
	addresses = slices.Compact(addresses)
	if literal, err := netip.ParseAddr((&url.URL{Host: host}).Hostname()); err == nil &&
		(len(addresses) != 1 || addresses[0] != literal) {
		return nil, errors.New("IPv6 主机只能授权其自身地址")
	}
	return addresses, nil
}

func (h *httpsHostTrustHandlers) preview(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	entry, ok := readGitHTTPSHost(c)
	if !ok {
		return
	}
	status := http.StatusBadRequest
	var err error
	if entry.Addresses == nil {
		hostname := (&url.URL{Host: entry.Host}).Hostname()
		if literal, parseErr := netip.ParseAddr(hostname); parseErr == nil {
			entry.Addresses = []netip.Addr{literal}
		} else {
			status = http.StatusBadGateway
			ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
			defer cancel()
			entry.Addresses, err = h.lookupIP(ctx, "ip", hostname)
		}
	}
	if err == nil {
		entry.Addresses, err = approvedGitHTTPSAddresses(entry.Host, entry.Addresses)
	}
	if err != nil {
		message := err.Error()
		if status == http.StatusBadGateway {
			message = "平台无法解析可授权的 Git 主机地址，请检查服务端 DNS"
		}
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, entry)
}

func (h *httpsHostTrustHandlers) save(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	entry, ok := readGitHTTPSHost(c)
	if !ok {
		return
	}
	addresses, err := approvedGitHTTPSAddresses(entry.Host, entry.Addresses)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	entry.Addresses = addresses
	ctx := c.Request.Context()
	status := http.StatusCreated
	if c.Request.Method == http.MethodPost {
		err = h.hosts.CreatePlatformGitHTTPSHost(ctx, &entry)
	} else {
		revision, ok := readPlatformGitHostRevision(c)
		if !ok {
			return
		}
		err = h.hosts.UpdatePlatformGitHTTPSHost(ctx, &entry, revision)
		status = http.StatusOK
	}
	if err != nil {
		respondGitHTTPSHostWriteError(c, err)
		return
	}
	c.Header("ETag", strconv.Quote(entry.Revision))
	c.JSON(status, entry)
}

func (h *httpsHostTrustHandlers) remove(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	host, err := gitwork.HTTPSHostScope(c.Param("host"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	revision, ok := readPlatformGitHostRevision(c)
	if !ok {
		return
	}
	if err := h.hosts.DeletePlatformGitHTTPSHost(c.Request.Context(), host, revision); err != nil {
		respondGitHTTPSHostWriteError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func respondGitHTTPSHostWriteError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, gorm.ErrDuplicatedKey):
		c.JSON(http.StatusConflict, gin.H{"error": "该 HTTPS 主机已登记，请更新现有授权"})
	case errors.Is(err, repo.ErrGitHTTPSHostChanged):
		c.JSON(http.StatusPreconditionFailed, gin.H{
			"code":  "https_host_trust_changed",
			"error": "主机授权已变更，请刷新后重试",
		})
	default:
		respond(c, err)
	}
}
