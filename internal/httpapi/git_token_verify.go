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

	"agentflow/internal/gitwork"

	"github.com/gin-gonic/gin"
)

// 工蜂（TGit）当前提供 GitLab v3 兼容 API（v4 路径不存在）。
// 校验目标是用户在工蜂「个人访问令牌」页申请的 PAT，而不是私有令牌；
// 仓库克隆 / 推送要求 read_repository 与 write_repository 两个权限范围。
// 不带令牌访问 /personal_access_tokens/self 返回 401，说明端点存在。
// 工蜂对能用的个人访问令牌也会在这个端点返回 404，不能据此判定令牌类型。
// 只有 /user 里的 private_token 与所填令牌相同，才是不能克隆仓库的私人令牌。
var gongfengAPIBase = "https://git.example.com/api/v3"

var requiredGitTokenScopes = []string{"read_repository", "write_repository"}

type gitTokenVerifyResult struct {
	Valid   bool     `json:"valid"`
	Login   string   `json:"login,omitempty"`
	Scopes  []string `json:"scopes,omitempty"`
	Missing []string `json:"missing,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// verifyGitToken 校验当前登录用户的 GitHub Git Token 是否生效。
// GET 校验已保存的令牌；POST 可在 body.token 里带上尚未保存的令牌，
// 让用户填完后直接点校验。令牌只发往固定的 GitHub API 地址，不落库。
// 始终返回 200 + 结构化结果，前端据此展示校验结论。
func (h *usersHandlers) verifyGitToken(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gitTokenVerifyResult{Error: "valid user id is required"})
		return
	}
	current, ok := h.requestUser(c.Request, false)
	if !ok || current.ID != id {
		c.JSON(http.StatusForbidden, gitTokenVerifyResult{Error: "只能校验当前登录用户的凭据"})
		return
	}
	token := ""
	if c.Request.Method == http.MethodPost {
		var req struct {
			Token string `json:"token"`
		}
		if !decode(c, &req) {
			return
		}
		token = strings.TrimSpace(req.Token)
	}
	if token == "" {
		if h.repo == nil {
			c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "存储不可用"})
			return
		}
		user, ok := h.repo.User(id)
		if !ok {
			c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "用户不存在"})
			return
		}
		_, token = gitwork.SplitToken(h.repo.GitCredential(c.Request.Context(), user.ID))
	}
	_, token = gitwork.SplitToken(token)
	if token == "" {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "尚未配置 Git Token，请先在个人配置中填写"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	c.JSON(http.StatusOK, verifyGitHubToken(ctx, token))
}

func verifyGitHubToken(ctx context.Context, token string) gitTokenVerifyResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return gitTokenVerifyResult{Error: "Invalid verification request"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return gitTokenVerifyResult{Error: "Could not reach GitHub"}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return gitTokenVerifyResult{Error: fmt.Sprintf("GitHub returned HTTP %d; check your token", res.StatusCode)}
	}
	var user struct {
		Login string `json:"login"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&user) != nil || user.Login == "" {
		return gitTokenVerifyResult{Error: "Invalid GitHub response"}
	}
	// Fine-grained token permissions are repository-specific; /user validates
	// identity only. Repository access is verified when cloning or importing.
	return gitTokenVerifyResult{Valid: true, Login: user.Login}
}

func verifyGongfengToken(ctx context.Context, token string) gitTokenVerifyResult {
	scopes, login, err := gongfengTokenSelf(ctx, token)
	if err != nil {
		return gitTokenVerifyResult{Valid: false, Error: err.Error()}
	}
	missing := missingGitTokenScopes(scopes, requiredGitTokenScopes)
	return gitTokenVerifyResult{Valid: len(missing) == 0, Login: login, Scopes: scopes, Missing: missing}
}

// gongfengTokenSelf 优先用 /personal_access_tokens/self 读取权限范围。
// 该端点对有效个人访问令牌也会返回 404。这时退回 /user：私人令牌会原样出现在
// private_token 字段里，应拒绝；其余能通过 /user 的令牌按 master 一样视为有效。
func gongfengTokenSelf(ctx context.Context, token string) ([]string, string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := gongfengDo(ctx, client, gongfengAPIBase+"/personal_access_tokens/self", token)
	if err != nil {
		return nil, "", fmt.Errorf("无法连接工蜂（git.example.com）：%v", err)
	}
	defer resp.Body.Close()
	var info struct {
		Scopes  []string `json:"scopes"`
		Active  bool     `json:"active"`
		Revoked bool     `json:"revoked"`
		User    struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if resp.StatusCode == http.StatusNotFound {
		login, err := gongfengTokenUser(ctx, client, token)
		if err != nil {
			return nil, "", err
		}
		return nil, login, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, "", errors.New("令牌无效或已过期：请确认填写的是工蜂「个人访问令牌」")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("工蜂返回 HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&info); err != nil {
		return nil, "", fmt.Errorf("工蜂返回内容无法解析（HTTP %d）", resp.StatusCode)
	}
	if info.Revoked || !info.Active {
		return nil, "", errors.New("令牌已被撤销或未激活")
	}
	return info.Scopes, info.User.Username, nil
}

func gongfengTokenUser(ctx context.Context, client *http.Client, token string) (string, error) {
	resp, err := gongfengDo(ctx, client, gongfengAPIBase+"/user", token)
	if err != nil {
		return "", fmt.Errorf("无法连接工蜂（git.example.com）：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", errors.New("令牌无效或已过期：请确认填写的是工蜂「个人访问令牌」")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("工蜂返回 HTTP %d", resp.StatusCode)
	}
	var user struct {
		Username     string `json:"username"`
		PrivateToken string `json:"private_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&user); err != nil {
		return "", errors.New("工蜂返回的用户信息无法解析，请稍后重试校验")
	}
	if user.PrivateToken != "" && user.PrivateToken == token {
		return "", errors.New("当前配置的是工蜂「私人令牌」，只能用于 API，不能克隆或推送仓库。请在工蜂「个人配置 → 帐号」中创建「个人访问令牌」，勾选 read_repository、write_repository 权限，然后替换并保存")
	}
	return user.Username, nil
}

// gongfengDo sends the token as a private token, then retries with a bearer
// header. Personal access tokens use the first form. OAuth access tokens are
// rejected as PRIVATE-TOKEN and accepted as Authorization: Bearer.
func gongfengDo(ctx context.Context, client *http.Client, rawURL, token string) (*http.Response, error) {
	resp, err := gongfengDoOnce(ctx, client, rawURL, token, false)
	if err != nil || (resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden) {
		return resp, err
	}
	resp.Body.Close()
	return gongfengDoOnce(ctx, client, rawURL, token, true)
}

func gongfengDoOnce(ctx context.Context, client *http.Client, rawURL, token string, bearer bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if bearer {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	return client.Do(req)
}

func missingGitTokenScopes(scopes, required []string) []string {
	if len(scopes) == 0 {
		// self 端点没有返回范围时，不能把「读不到」报成「缺少权限」。
		return nil
	}
	set := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		set[s] = true
	}
	var missing []string
	for _, s := range required {
		if !set[s] {
			missing = append(missing, s)
		}
	}
	return missing
}

// verifyGitLabToken validates the configured user's identity on their instance.
func (h *usersHandlers) verifyGitLabToken(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gitTokenVerifyResult{Error: "valid user id is required"})
		return
	}
	current, ok := h.requestUser(c.Request, false)
	if !ok || current.ID != id {
		c.JSON(http.StatusForbidden, gitTokenVerifyResult{Error: "只能校验当前登录用户的凭据"})
		return
	}
	if h.repo == nil {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "存储不可用"})
		return
	}
	user, ok := h.repo.User(id)
	if !ok || user.GitLabToken == "" || user.GitLabBaseURL == "" {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "请先保存 GitLab 地址和 Token"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(user.GitLabBaseURL, "/")+"/api/v4/user", nil)
	if err != nil {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "GitLab 地址无效"})
		return
	}
	req.Header.Set("PRIVATE-TOKEN", user.GitLabToken)
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "无法连接 GitLab"})
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: fmt.Sprintf("GitLab 返回 HTTP %d，请检查 Token", res.StatusCode)})
		return
	}
	var identity struct {
		Username string `json:"username"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&identity) != nil || identity.Username == "" {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "GitLab 响应无效"})
		return
	}
	c.JSON(http.StatusOK, gitTokenVerifyResult{Valid: true, Login: identity.Username})
}
