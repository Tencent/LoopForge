package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// A Forge request never goes through an HTTP proxy or follows a redirect.
// TLS verifies the explicitly trusted instance before any PAT is sent.
var apiTransport = &http.Transport{
	DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ForceAttemptHTTP2:   true,
	MaxIdleConns:        20,
	IdleConnTimeout:     60 * time.Second,
	TLSHandshakeTimeout: 5 * time.Second,
}

var apiClient = newAPIClient(apiTransport)

func newAPIClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Timeout:       15 * time.Second,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

var ErrTokenRequired = errors.New("请在「个人配置 → 新增 Git 配置 → Forgejo」配置该实例的访问令牌")

// ValidToken treats a PAT as opaque, but rejects whitespace and control
// characters before it can enter storage or an HTTP header.
func ValidToken(token string) bool {
	if token == "" || len(token) > 512 {
		return false
	}
	for _, ch := range token {
		if ch < '!' || ch > '~' {
			return false
		}
	}
	return true
}

type statusError int

func (s statusError) Error() string {
	return fmt.Sprintf("Forgejo 返回 HTTP %d，请检查令牌权限和仓库状态", int(s))
}

func request(ctx context.Context, instance, token, method, route string, input, output any) (http.Header, error) {
	if !Allows(instance) {
		return nil, ErrInstanceNotAllowed
	}
	if !ValidToken(token) {
		return nil, ErrTokenRequired
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return nil, errors.New("无法编码 Forgejo 请求")
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, instance+"/api/v1"+route, body)
	if err != nil {
		return nil, errors.New("无效的 Forgejo 请求")
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+token)
	res, err := apiClient.Do(req)
	if err != nil {
		// Do not expose server-controlled response text, URLs or headers.
		return nil, errors.New("无法连接 Forgejo 实例")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, statusError(res.StatusCode)
	}
	if output != nil && json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(output) != nil {
		return nil, errors.New("无法解析 Forgejo 响应")
	}
	return res.Header, nil
}

// VerifyToken checks authentication only. Repository access is verified when
// delivery looks up the bound repository.
func VerifyToken(ctx context.Context, instance, token string) error {
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	_, err := request(ctx, instance, token, http.MethodGet, "/user", nil, &user)
	if err == nil && (user.ID <= 0 || user.Login == "") {
		return errors.New("Forgejo 未确认令牌身份")
	}
	return err
}

type RepositoryClient struct {
	ref           domain.ForgeRepository
	token         string
	id            int64
	defaultBranch string
}

func OpenRepository(ctx context.Context, ref domain.ForgeRepository, token string) (*RepositoryClient, error) {
	if parsed, ok := domain.ParseForgeProject(ref.Project()); !ok || parsed != ref {
		return nil, errors.New("无效的 Forgejo 仓库地址")
	}
	client := &RepositoryClient{ref: ref, token: token}
	var info struct {
		ID            int64  `json:"id"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := client.request(ctx, http.MethodGet, "", nil, &info); err != nil {
		return nil, err
	}
	if info.ID <= 0 || !strings.EqualFold(info.FullName, ref.Path()) {
		return nil, errors.New("Forgejo 未确认仓库身份")
	}
	client.id = info.ID
	client.defaultBranch = info.DefaultBranch
	return client, nil
}

func (c *RepositoryClient) DefaultBranch() string { return c.defaultBranch }

func (c *RepositoryClient) request(ctx context.Context, method, suffix string, input, output any) (http.Header, error) {
	return request(ctx, c.ref.Instance, c.token, method, "/repos/"+c.ref.Path()+suffix, input, output)
}
