// Package gitlab adapts the REST v4 API of a user-configured GitLab instance.
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/gitwork"
)

func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if len(raw) > 2048 || !(gitwork.HTTPSCredential{BaseURL: raw}).Matches(raw+"/") {
		return "", fmt.Errorf("请输入 GitLab HTTPS 实例地址，可包含部署子路径")
	}
	u, _ := url.Parse(raw)
	u.Host = strings.ToLower(u.Host)
	if u.Port() == "443" {
		u.Host = strings.TrimSuffix(u.Host, ":443")
	}
	return u.String(), nil
}

type Client struct {
	BaseURL string
	http    http.Client
}

func NewClient(baseURL string, transport http.RoundTripper) *Client {
	if transport == nil {
		transport = gitLabTransport
	}
	return &Client{BaseURL: baseURL, http: http.Client{
		Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type HTTPError int

func (e HTTPError) Error() string { return fmt.Sprintf("GitLab 返回 HTTP %d", e) }

func (c *Client) get(ctx context.Context, endpoint, token string, out any) (http.Header, error) {
	if c.BaseURL == "" || token == "" {
		return nil, fmt.Errorf("请先配置 GitLab 地址和个人 PAT")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v4/"+endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("无法构造 GitLab 请求")
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, gitwork.ErrPrivateHTTPSDestination) {
			return nil, gitwork.ErrPrivateHTTPSDestination
		}
		return nil, fmt.Errorf("无法连接 GitLab")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, HTTPError(resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return nil, fmt.Errorf("GitLab 返回了无法读取的数据")
	}
	return resp.Header, nil
}

func (c *Client) VerifyToken(ctx context.Context, token string) error {
	if len(token) > 1024 || strings.IndexFunc(token, func(r rune) bool { return r <= 0x20 || r > 0x7e }) >= 0 {
		return fmt.Errorf("请输入有效的 GitLab 个人访问令牌")
	}
	var result struct {
		Active bool     `json:"active"`
		Scopes []string `json:"scopes"`
	}
	if _, err := c.get(ctx, "personal_access_tokens/self", token, &result); err != nil {
		if err != HTTPError(http.StatusNotFound) && err != HTTPError(http.StatusForbidden) && err != HTTPError(http.StatusUnauthorized) {
			return err
		}
		// Some GitLab installations restrict or lack token introspection.
		// Verify the account and the API used by the repository picker.
		// Binding still checks the selected repository's branch access.
		var user struct {
			ID int64 `json:"id"`
		}
		if _, err := c.get(ctx, "user", token, &user); err != nil {
			return err
		}
		if user.ID <= 0 {
			return fmt.Errorf("GitLab 未返回有效的用户身份")
		}
		var projects []Project
		_, err := c.get(ctx, "projects?membership=true&per_page=1", token, &projects)
		return err
	}
	if !result.Active {
		return fmt.Errorf("GitLab PAT 已失效")
	}
	if !slices.Contains(result.Scopes, "api") && !(slices.Contains(result.Scopes, "read_api") &&
		(slices.Contains(result.Scopes, "read_repository") || slices.Contains(result.Scopes, "write_repository"))) {
		return fmt.Errorf("GitLab PAT 需要 read_api 和 read_repository（或 write_repository），也可使用 api")
	}
	return nil
}

type Project struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path_with_namespace"`
	WebURL        string `json:"web_url"`
	CloneURL      string `json:"http_url_to_repo"`
	DefaultBranch string `json:"default_branch"`
}

func (c *Client) usableProject(p Project) bool {
	scope := gitwork.HTTPSCredential{BaseURL: c.BaseURL}
	return p.ID > 0 && p.Path != "" && p.DefaultBranch != "" && scope.Matches(p.WebURL) && scope.Matches(p.CloneURL)
}

func (c *Client) Projects(ctx context.Context, token, search string, page int) ([]Project, bool, error) {
	query := url.Values{
		"membership": {"true"}, "order_by": {"last_activity_at"}, "sort": {"desc"},
		"per_page": {"50"}, "page": {strconv.Itoa(page)},
	}
	if search != "" {
		query.Set("search", search)
		query.Set("search_namespaces", "true")
	}
	var projects []Project
	headers, err := c.get(ctx, "projects?"+query.Encode(), token, &projects)
	if err != nil {
		return nil, false, err
	}
	next := headers.Get("X-Next-Page")
	more := next != "" || strings.Contains(headers.Get("Link"), `rel="next"`)
	return slices.DeleteFunc(projects, func(p Project) bool { return !c.usableProject(p) }), more, nil
}

func (c *Client) ReadableProject(ctx context.Context, token string, id int64) (Project, error) {
	endpoint := "projects/" + strconv.FormatInt(id, 10)
	var project Project
	if _, err := c.get(ctx, endpoint, token, &project); err != nil {
		return Project{}, err
	}
	if project.ID != id || !c.usableProject(project) {
		return Project{}, fmt.Errorf("GitLab 仓库缺少主分支，或地址不在配置的 GitLab 实例内")
	}
	// Project metadata can be visible to a user whose role cannot read Git.
	var branches []json.RawMessage
	if _, err := c.get(ctx, endpoint+"/repository/branches?per_page=1", token, &branches); err != nil {
		return Project{}, err
	}
	return project, nil
}
