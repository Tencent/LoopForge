package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// codeupHandlers serves Aliyun Yunxiao Codeup repository discovery. Unlike
// GitHub there is no OAuth device flow: the user saves a personal access token
// (plus the HTTPS clone account) as a per-host Git HTTP credential for
// codeup.aliyun.com, and these endpoints read with that stored credential.
type codeupHandlers struct {
	*deps
	client *http.Client
}

func newCodeupHandlers(shared *deps) *codeupHandlers {
	return &codeupHandlers{deps: shared, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

var codeupOrganizationID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (h *codeupHandlers) current(c *gin.Context) (int64, bool) {
	c.Header("Cache-Control", "no-store")
	user, ok := h.requestUser(c.Request, false)
	if !ok || user == nil || user.IsGuest || user.ID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录 LoopForge，再连接云效 Codeup"})
		return 0, false
	}
	if c.Request.Method != http.MethodGet {
		origin, err := url.Parse(c.GetHeader("Origin"))
		if err != nil || origin.Host != c.Request.Host || (origin.Scheme != "http" && origin.Scheme != "https") {
			c.JSON(http.StatusForbidden, gin.H{"error": "请求来源不允许"})
			return 0, false
		}
	}
	return user.ID, true
}

func (h *codeupHandlers) credential(c *gin.Context, userID int64) (domain.GitHTTPCredentialSecret, bool) {
	cred, ok := h.repo.GitHTTPCredentialForHost(c.Request.Context(), userID, domain.CodeupRepoHost)
	if !ok || strings.TrimSpace(cred.Token) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先在个人配置中连接云效 Codeup（填写 HTTPS 克隆账号与个人访问令牌）"})
		return domain.GitHTTPCredentialSecret{}, false
	}
	return cred, true
}

// request calls the Codeup central-station OpenAPI with the stored PAT.
func (h *codeupHandlers) request(c *gin.Context, method, path, token string, output any) error {
	req, err := http.NewRequestWithContext(c.Request.Context(), method, domain.CodeupAPIBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "LoopForge")
	req.Header.Set("x-yunxiao-token", token)
	res, err := h.client.Do(req)
	if err != nil {
		return errors.New("无法连接云效 Codeup，请稍后重试")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New("云效 Codeup 请求失败（HTTP " + strconv.Itoa(res.StatusCode) + "），请检查个人访问令牌与组织/仓库权限")
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(output); err != nil {
		return errors.New("云效 Codeup 返回了无效响应")
	}
	return nil
}

// organizations lists the Codeup organizations the credential can access; the
// returned id is the organizationId required by the repository list endpoint.
func (h *codeupHandlers) organizations(c *gin.Context) {
	userID, ok := h.current(c)
	if !ok {
		return
	}
	cred, ok := h.credential(c, userID)
	if !ok {
		return
	}
	var organizations []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := h.request(c, http.MethodGet, "/oapi/v1/platform/organizations", cred.Token, &organizations); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"organizations": organizations})
}

// repositories lists one organization's repositories. httpUrlToRepo is the
// HTTPS clone URL ParseRepositoryURL turns into the codeup: identity.
func (h *codeupHandlers) repositories(c *gin.Context) {
	userID, ok := h.current(c)
	if !ok {
		return
	}
	cred, ok := h.credential(c, userID)
	if !ok {
		return
	}
	org := strings.TrimSpace(c.Query("organizationId"))
	if org == "" || !codeupOrganizationID.MatchString(org) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供有效的 organizationId"})
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 || page > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效页码"})
		return
	}
	q := url.Values{"page": {strconv.Itoa(page)}, "perPage": {"100"}}
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		q.Set("search", search)
	}
	var repositories []struct {
		ID                int    `json:"id"`
		Name              string `json:"name"`
		Path              string `json:"path"`
		PathWithNamespace string `json:"pathWithNamespace"`
		WebURL            string `json:"webUrl"`
		SSHURLToRepo      string `json:"sshUrlToRepo"`
		HTTPURLToRepo     string `json:"httpUrlToRepo"`
		DefaultBranch     string `json:"defaultBranch"`
	}
	path := "/oapi/v1/codeup/organizations/" + url.PathEscape(org) + "/repositories?" + q.Encode()
	if err := h.request(c, http.MethodGet, path, cred.Token, &repositories); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"repositories": repositories, "hasMore": len(repositories) == 100, "page": page})
}

// verifyCodeupToken checks a saved Codeup credential against the current-user
// endpoint. It is the Codeup branch of verifyGitHTTPToken and always answers
// with a structured result.
func verifyCodeupToken(ctx context.Context, cred domain.GitHTTPCredentialSecret, client *http.Client) gitTokenVerifyResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cred.APIBase()+"/oapi/v1/platform/user", nil)
	if err != nil {
		return gitTokenVerifyResult{Error: "无效的校验请求"}
	}
	req.Header.Set(cred.APIAuthHeader())
	res, err := client.Do(req)
	if err != nil {
		return gitTokenVerifyResult{Error: fmt.Sprintf("无法连接 %s：%v", cred.Host, err)}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return gitTokenVerifyResult{Error: fmt.Sprintf("%s 返回 HTTP %d，请检查个人访问令牌", cred.Host, res.StatusCode)}
	}
	var user struct {
		Username string `json:"username"`
		Login    string `json:"login"`
		Name     string `json:"name"`
		NickName string `json:"nickName"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&user) != nil {
		return gitTokenVerifyResult{Error: fmt.Sprintf("%s 返回了无法识别的响应", cred.Host)}
	}
	login := user.Username
	if login == "" {
		login = user.Login
	}
	if login == "" {
		login = user.Name
	}
	if login == "" {
		login = user.NickName
	}
	if login == "" {
		return gitTokenVerifyResult{Error: fmt.Sprintf("%s 返回了无法识别的响应", cred.Host)}
	}
	return gitTokenVerifyResult{Valid: true, Login: login}
}