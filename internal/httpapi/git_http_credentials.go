package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"agentflow/internal/domain"
	"agentflow/internal/gitegress"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// gitHTTPVerifyClient reaches user-configured hosts; tests replace it.
var gitHTTPVerifyClient = gitegress.Client

type gitHTTPCredentialInput struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

func (h *usersHandlers) gitHTTPCredentials(c *gin.Context) {
	id, ok := h.gitHTTPCredentialOwner(c)
	if !ok {
		return
	}
	switch c.Request.Method {
	case http.MethodPut:
		var input gitHTTPCredentialInput
		if !decodeLimit(c, &input, 16<<10) {
			return
		}
		cred, err := h.gitHTTPCredentialFromInput(c.Request.Context(), id, input)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := h.repo.SaveGitHTTPCredential(id, input.ID, cred); err != nil {
			respondGitHTTPCredentialError(c, err)
			return
		}
	case http.MethodDelete:
		if err := h.repo.DeleteGitHTTPCredential(id, c.Param("credID")); err != nil {
			respondGitHTTPCredentialError(c, err)
			return
		}
	}
	creds, err := h.repo.GitHTTPCredentials(id)
	if err != nil {
		respond(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, creds)
}

func (h *usersHandlers) gitHTTPCredentialOwner(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid user id is required"})
		return 0, false
	}
	if user, ok := h.requestUser(c.Request, false); !ok || user.ID != id {
		c.JSON(http.StatusForbidden, gin.H{"error": "只能管理当前登录用户的 Git 访问令牌"})
		return 0, false
	}
	return id, true
}

func respondGitHTTPCredentialError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, repo.ErrGitHTTPTokenRequired), errors.Is(err, repo.ErrGitHTTPCredentialManaged):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, repo.ErrGitHTTPCredentialExists):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "此主机的访问令牌已不存在"})
	default:
		respond(c, err)
	}
}

// gitHTTPCredentialFromInput validates the form. The platform defaults to
// GitHub for github.com and "other" elsewhere.
func (h *usersHandlers) gitHTTPCredentialFromInput(ctx context.Context, userID int64, input gitHTTPCredentialInput) (domain.GitHTTPCredentialSecret, error) {
	host, err := gitwork.HTTPSHostScope(input.Host)
	if err != nil {
		return domain.GitHTTPCredentialSecret{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return domain.GitHTTPCredentialSecret{}, errors.New("请输入 1 到 80 个字符的凭据名称")
	}
	platform := domain.GitPlatformOther
	if host == "github.com" {
		platform = domain.GitPlatformGitHub
	}
	if strings.TrimSpace(input.Platform) != "" {
		var ok bool
		if platform, ok = domain.NormalizeGitPlatform(input.Platform); !ok {
			return domain.GitHTTPCredentialSecret{}, errors.New("平台只能是 github、gitlab 或 other")
		}
	}
	username := strings.TrimSpace(input.Username)
	if len(username) > 128 || strings.ContainsRune(username, ':') || strings.IndexFunc(username, unicode.IsControl) >= 0 {
		return domain.GitHTTPCredentialSecret{}, errors.New("用户名不超过 128 个字符，且不能包含冒号或控制字符")
	}
	token := strings.TrimSpace(input.Token)
	if len(token) > 4096 || strings.IndexFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return domain.GitHTTPCredentialSecret{}, errors.New("访问令牌不超过 4096 个字符，且不能包含空白或控制字符")
	}
	return domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{
			Name: name, Platform: platform, Host: host, Username: username},
		Token: token,
	}, nil
}

// verifyGitHTTPCredential checks a saved credential against its platform's
// "current user" endpoint. It always answers 200 with a structured result
// once the credential exists.
func (h *usersHandlers) verifyGitHTTPCredential(c *gin.Context) {
	id, ok := h.gitHTTPCredentialOwner(c)
	if !ok {
		return
	}
	cred, ok := h.repo.GitHTTPCredentialByID(c.Request.Context(), id, c.Param("credID"))
	if !ok {
		c.JSON(http.StatusNotFound, gitTokenVerifyResult{Error: "此主机的访问令牌已不存在"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	c.JSON(http.StatusOK, verifyGitHTTPToken(ctx, cred))
}

func verifyGitHTTPToken(ctx context.Context, cred domain.GitHTTPCredentialSecret) gitTokenVerifyResult {
	if cred.Managed {
		return verifyGongfengToken(ctx, cred.Token)
	}
	base := cred.APIBase()
	if base == "" {
		return gitTokenVerifyResult{Error: "该平台不支持在线校验，令牌会在 Git 操作时使用"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return gitTokenVerifyResult{Error: "无效的校验请求"}
	}
	req.Header.Set(cred.APIAuthHeader())
	if cred.Platform == domain.GitPlatformGitHub {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	res, err := gitHTTPVerifyClient().Do(req)
	if err != nil {
		return gitTokenVerifyResult{Error: fmt.Sprintf("无法连接 %s：%v", cred.Host, err)}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return gitTokenVerifyResult{Error: fmt.Sprintf("%s 返回 HTTP %d，请检查令牌", cred.Host, res.StatusCode)}
	}
	var user struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&user) != nil {
		return gitTokenVerifyResult{Error: fmt.Sprintf("%s 返回了无法识别的响应", cred.Host)}
	}
	login := user.Login
	if cred.Platform == domain.GitPlatformGitLab {
		login = user.Username
	}
	if login == "" {
		return gitTokenVerifyResult{Error: fmt.Sprintf("%s 返回了无法识别的响应", cred.Host)}
	}
	// Repository permissions are per repository; /user proves identity only.
	return gitTokenVerifyResult{Valid: true, Login: login}
}
