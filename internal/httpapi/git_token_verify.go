package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// 工蜂（TGit）当前提供 GitLab v3 兼容 API（v4 路径不存在，实测返回 404）。
// 校验目标是用户在工蜂「个人访问令牌」页申请的 PAT，而不是私有令牌；
// 仓库克隆 / 推送要求 read_repository 与 write_repository 两个权限范围。
const gongfengAPIBase = "https://git.example.com/api/v3"

var requiredGitTokenScopes = []string{"read_repository", "write_repository"}

type gitTokenVerifyResult struct {
	Valid   bool     `json:"valid"`
	Login   string   `json:"login,omitempty"`
	Scopes  []string `json:"scopes,omitempty"`
	Missing []string `json:"missing,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// verifyGitToken 校验当前登录用户已保存的工蜂 Git Token 是否生效。
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
	if h.repo == nil {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "存储不可用"})
		return
	}
	user, ok := h.repo.User(id)
	if !ok {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "用户不存在"})
		return
	}
	if user.GitToken == "" {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "尚未配置 Git Token，请先在个人设置中填写"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	c.JSON(http.StatusOK, verifyGitHubToken(ctx, user.GitToken))
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

// gongfengTokenSelf 通过 /personal_access_tokens/self 反查令牌状态与权限范围。
// 工蜂旧版本不支持该端点时退回 /user，仅确认令牌有效、无法读取权限范围。
func gongfengTokenSelf(ctx context.Context, token string) ([]string, string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gongfengAPIBase+"/personal_access_tokens/self", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := client.Do(req)
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
		// 旧版工蜂没有 self 端点：退回 /user 只验证令牌有效性。
		if err := gongfengTokenUser(ctx, client, token); err != nil {
			return nil, "", err
		}
		return nil, "", nil
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

func gongfengTokenUser(ctx context.Context, client *http.Client, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gongfengAPIBase+"/user", nil)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接工蜂（git.example.com）：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errors.New("令牌无效或已过期：请确认填写的是工蜂「个人访问令牌」")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("工蜂返回 HTTP %d", resp.StatusCode)
	}
	return nil
}

func missingGitTokenScopes(scopes, required []string) []string {
	if len(scopes) == 0 {
		// 无法读取权限范围（旧版工蜂 fallback）：不误报，交给前端提示无法确认范围。
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
