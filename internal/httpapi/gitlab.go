package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitlab"

	"github.com/gin-gonic/gin"
)

type gitlabHandlers struct {
	*deps
}

// Only tests replace the transport. Redirect policy still belongs to the
// GitLab client.
var gitLabTransport http.RoundTripper

func gitLabClient(baseURL string) *gitlab.Client { return gitlab.NewClient(baseURL, gitLabTransport) }

func (h *gitlabHandlers) credential(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	current, ok := h.requestUser(c.Request, false)
	if !ok || current.ID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录 LoopForge"})
		return
	}
	var base, token string
	if c.Request.Method == http.MethodPut {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		var input struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		}
		if !decode(c, &input) {
			return
		}
		var err error
		base, err = gitlab.NormalizeURL(input.URL)
		if err != nil {
			gitLabError(c, err)
			return
		}
		token = strings.TrimSpace(input.Token)
		if token == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 GitLab 个人访问令牌"})
			return
		}
		if err := gitLabClient(base).VerifyToken(c.Request.Context(), token); err != nil {
			gitLabError(c, err)
			return
		}
	}
	if err := h.repo.SaveGitLabToken(current.ID, base, token); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	user, _ := h.repo.User(current.ID)
	c.JSON(http.StatusOK, struct {
		domain.User
		GitLabURL string `json:"gitLabUrl,omitempty"`
	}{domain.PublicUser(user), base})
}

func (h *gitlabHandlers) repositories(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	current, ok := h.requestUser(c.Request, false)
	if !ok || current.ID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录 LoopForge"})
		return
	}
	credential := h.repo.GitLabCredential(current.ID)
	if credential.Token == "" {
		gitLabTokenRequired(c)
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	q := strings.TrimSpace(c.Query("q"))
	if err != nil || page < 1 || page > 10000 || len(q) > 250 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "GitLab 仓库搜索参数无效"})
		return
	}
	repositories, hasMore, err := gitLabClient(credential.BaseURL).Projects(c.Request.Context(), credential.Token, q, page)
	if err != nil {
		gitLabError(c, err)
		return
	}
	if repositories == nil {
		repositories = []gitlab.Project{}
	}
	c.JSON(http.StatusOK, gin.H{"repositories": repositories, "hasMore": hasMore, "gitLabUrl": credential.BaseURL})
}

func gitLabTokenRequired(c *gin.Context) {
	c.JSON(http.StatusPreconditionFailed, gin.H{"error": "请先在个人配置中保存 GitLab 地址和 PAT"})
}

func gitLabError(c *gin.Context, err error) {
	var status gitlab.HTTPError
	if errors.As(err, &status) {
		switch int(status) {
		case http.StatusUnauthorized:
			c.JSON(http.StatusUnauthorized, gin.H{"error": "GitLab PAT 无效或已过期，请更新个人凭证"})
			return
		case http.StatusForbidden, http.StatusNotFound:
			c.JSON(http.StatusForbidden, gin.H{"error": "当前 GitLab PAT 没有访问该资源所需的权限"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}
