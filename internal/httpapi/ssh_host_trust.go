package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"agentflow/internal/domain"
	"agentflow/internal/gitegress"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	platformSSHHostRevisionMaxChars     = 64
	platformSSHHostLegacyRevisionPrefix = "~"
)

type sshHostTrustHandlers struct {
	*deps
	hosts          *repo.SSHHostTrustRepo
	runnerControls *runnerControlHub
}

type platformSSHHostInput struct {
	Host       string                `json:"host"`
	PublicKeys string                `json:"publicKeys"`
	Probe      *platformSSHHostProbe `json:"probe"`
}

type platformSSHHostProbe struct {
	RunnerID string `json:"runnerId"`
}

type platformSSHHostView struct {
	repo.PlatformGitSSHHost
	Fingerprints []string `json:"fingerprints"`
	Invalid      bool     `json:"invalid,omitempty"`
	InvalidHost  bool     `json:"invalidHost,omitempty"`
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
		host.Revision = platformSSHHostRevisionToken(host.Revision)
		scope, scopeErr := gitwork.SSHHostScope(host.Host)
		invalidHost := scopeErr != nil || scope != host.Host
		view, err := platformSSHHostWithFingerprints(host)
		if err != nil || invalidHost {
			host.PublicKeys = ""
			view = platformSSHHostView{PlatformGitSSHHost: host, Fingerprints: []string{}, Invalid: true, InvalidHost: invalidHost}
		}
		views = append(views, view)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, views)
}

func readPlatformSSHHostInput(c *gin.Context) (platformSSHHostInput, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*gitwork.MaxSSHHostPublicKeyBytes)
	var input platformSSHHostInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 Git SSH 主机信任配置"})
		return platformSSHHostInput{}, false
	}
	if pathHost := c.Param("host"); pathHost != "" {
		input.Host = pathHost
	}
	host, err := gitwork.SSHHostScope(input.Host)
	if err != nil || len(host) > 260 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入 SSH 主机名，可附带端口"})
		return platformSSHHostInput{}, false
	}
	input.Host = host
	return input, true
}

func platformSSHHostWithFingerprints(host repo.PlatformGitSSHHost) (platformSSHHostView, error) {
	keys, fingerprints, err := gitwork.NormalizeSSHHostPublicKeys(host.PublicKeys)
	host.PublicKeys = keys
	return platformSSHHostView{PlatformGitSSHHost: host, Fingerprints: fingerprints}, err
}

func (h *sshHostTrustHandlers) preview(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	input, ok := readPlatformSSHHostInput(c)
	if !ok {
		return
	}
	if input.Probe != nil {
		if strings.TrimSpace(input.PublicKeys) != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请选择读取主机公钥或核对手动录入的公钥"})
			return
		}
		key, err := h.probeSSHHostKey(c.Request, input.Host, strings.TrimSpace(input.Probe.RunnerID))
		if err != nil {
			respond(c, err)
			return
		}
		input.PublicKeys = key
	}
	entry, err := platformSSHHostWithFingerprints(repo.PlatformGitSSHHost{Host: input.Host, PublicKeys: input.PublicKeys})
	if err != nil {
		status := http.StatusBadRequest
		if input.Probe != nil {
			status = http.StatusBadGateway
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, entry)
}

func (h *sshHostTrustHandlers) probeSSHHostKey(r *http.Request, host, runnerID string) (string, error) {
	if runnerID == "" {
		key, err := gitwork.ProbeSSHHostPublicKey(r.Context(), host, gitegress.DialContext)
		if err != nil {
			return "", HTTPError{Status: http.StatusBadGateway, Message: err.Error()}
		}
		return key, nil
	}
	user, authenticated := h.requestUser(r, true)
	if !authenticated || user.ID <= 0 {
		return "", HTTPError{Status: http.StatusForbidden, Message: "请选择自己绑定的执行机器"}
	}
	runner, found := h.repo.Runner(runnerID)
	if !found {
		return "", HTTPError{Status: http.StatusNotFound, Message: "执行机器不存在"}
	}
	if runner.OwnerUserID != user.ID {
		return "", HTTPError{Status: http.StatusForbidden, Message: "请选择自己绑定的执行机器"}
	}
	if runner.Status != domain.RunnerStatusOnline || h.runnerControls == nil {
		return "", HTTPError{Status: http.StatusServiceUnavailable, Message: "执行机器暂未连接，请稍后重试"}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	response, err := h.runnerControls.requestWorkspace(ctx, runner.ID, domain.RunnerWorkspaceRequest{
		Operation:         domain.RunnerWorkspaceOperationSSHHostKeyProbe,
		SSHHostKeyAddress: host, SSHHostKeyOwnerID: user.ID,
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return "", HTTPError{Status: http.StatusGatewayTimeout, Message: "执行机器读取 SSH 主机公钥超时"}
	}
	if err != nil {
		return "", HTTPError{Status: http.StatusServiceUnavailable, Message: "暂时无法连接执行机器，请稍后重试"}
	}
	if response.Error != "" && response.Status >= 400 && response.Status < 600 {
		return "", HTTPError{Status: response.Status, Message: response.Error}
	}
	var key string
	if response.Status != http.StatusOK || len(response.Payload) > 2*gitwork.MaxSSHHostPublicKeyBytes ||
		json.Unmarshal(response.Payload, &key) != nil {
		return "", HTTPError{Status: http.StatusBadGateway, Message: "执行机器返回了无效的 SSH 主机公钥"}
	}
	return key, nil
}

// Generated revisions already fit in an HTTP entity-tag. Encode legacy values
// with characters that browsers cannot put in an If-Match header.
func platformSSHHostRevisionToken(revision string) string {
	if !strings.HasPrefix(revision, platformSSHHostLegacyRevisionPrefix) &&
		strings.IndexFunc(revision, func(c rune) bool {
			return c <= ' ' || c > '~' || c == '"' || c == '\\'
		}) == -1 {
		return revision
	}
	return platformSSHHostLegacyRevisionPrefix + base64.RawURLEncoding.EncodeToString([]byte(revision))
}

func readPlatformSSHHostRevision(c *gin.Context) (string, bool) {
	value := strings.TrimSpace(c.GetHeader("If-Match"))
	if value == "" {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "请重新打开主机信任记录后再操作"})
		return "", false
	}
	token, unquoteErr := strconv.Unquote(value)
	revision := token
	var decodeErr error
	if encoded, ok := strings.CutPrefix(token, platformSSHHostLegacyRevisionPrefix); ok {
		var raw []byte
		raw, decodeErr = base64.RawURLEncoding.Strict().DecodeString(encoded)
		revision = string(raw)
	}
	if unquoteErr != nil || decodeErr != nil || utf8.RuneCountInString(revision) > platformSSHHostRevisionMaxChars ||
		value != strconv.Quote(token) || token != platformSSHHostRevisionToken(revision) {
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
	input, ok := readPlatformSSHHostInput(c)
	if !ok {
		return
	}
	if input.Probe != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先核对指纹，再提交公钥"})
		return
	}
	entry, err := platformSSHHostWithFingerprints(repo.PlatformGitSSHHost{Host: input.Host, PublicKeys: input.PublicKeys})
	if err != nil {
		respond(c, err)
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
	var host string
	if pathHost := c.Param("host"); pathHost != "" {
		var err error
		host, err = gitwork.SSHHostScope(pathHost)
		if err != nil || len(host) > 260 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请输入 SSH 主机名，可附带端口"})
			return
		}
	} else {
		// Use the stored identifier even when it cannot be normalized to an SSH endpoint.
		hosts := c.QueryArray("host")
		if len(hosts) != 1 || utf8.RuneCountInString(hosts[0]) > 260 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请提供一条主机信任记录的主机名"})
			return
		}
		host = hosts[0]
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
