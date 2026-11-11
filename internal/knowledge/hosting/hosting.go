// Package hosting 负责托管平台（GitHub、GitLab）的合并请求接口。
// 它只接收来源上的写入凭据，不读取用户凭据，也不把令牌写进请求之外的任何地方。
package hosting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/knowledge/gitremote"
)

// Provider 是受支持的托管平台。
type Provider string

const (
	GitHub Provider = "github"
	GitLab Provider = "gitlab"
)

// Repo 是 Git 地址对应的托管仓库。Path 是 owner/repo 或 GitLab 的 group/sub/repo。
type Repo struct {
	Provider Provider
	Host     string
	Path     string
}

// Detect 从来源地址识别受支持的托管平台。不支持的平台返回 false，调用方
// 只推送分支并给出中文提示。只支持 GitHub.com 与 GitLab（含自建实例）。
func Detect(raw string) (Repo, bool) {
	u, err := gitremote.Parse(raw)
	if err != nil || u == nil {
		return Repo{}, false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, "/")
	if host == "" || path == "" {
		return Repo{}, false
	}
	segments := strings.Split(path, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, "\r\n\x00") {
			return Repo{}, false
		}
	}
	switch {
	case host == "github.com":
		if len(segments) != 2 {
			return Repo{}, false
		}
		return Repo{Provider: GitHub, Host: host, Path: path}, true
	case host == "gitlab.com" || strings.Contains(host, "gitlab"):
		if len(segments) < 2 {
			return Repo{}, false
		}
		return Repo{Provider: GitLab, Host: host, Path: path}, true
	default:
		return Repo{}, false
	}
}

// APIBase 返回该仓库的合并请求 API 基地址。只使用 HTTPS。
func (r Repo) APIBase() string {
	if r.Provider == GitHub {
		return "https://api.github.com"
	}
	return "https://" + strings.TrimSpace(r.Host) + "/api/v4"
}

// MergeRequest 是创建合并请求需要的字段。
type MergeRequest struct {
	Title        string
	Body         string
	SourceBranch string
	TargetBranch string
}

// MergeRequestRef 是创建合并请求的结果。
type MergeRequestRef struct {
	IID      int
	URL      string
	Provider Provider
	State    string
}

// Client 调用托管平台 API。BaseURL 只在测试里替换为本地地址；生产路径由
// Repo.APIBase 生成 HTTPS 地址。
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// Error 保留平台与状态码，便于分类；不携带响应正文，避免回显凭据。
type Error struct {
	Provider Provider
	Status   int
	Reason   string
}

func (e *Error) Error() string {
	label := "合并请求"
	switch e.Provider {
	case GitHub:
		label = "GitHub 合并请求"
	case GitLab:
		label = "GitLab 合并请求"
	}
	if e.Reason != "" {
		return fmt.Sprintf("%s接口失败：%s", label, e.Reason)
	}
	return fmt.Sprintf("%s接口返回 HTTP %d", label, e.Status)
}

func (c *Client) httpClient() *http.Client {
	var client http.Client
	if c.HTTP != nil {
		client = *c.HTTP
	} else {
		client.Timeout = 30 * time.Second
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := gitremote.PublicAddresses(ctx, host, net.DefaultResolver.LookupIPAddr)
			if err != nil {
				return nil, err
			}
			var dialer net.Dialer
			var last error
			for _, a := range addresses {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		}
		client.Transport = transport
	}
	// A redirect could move the request to another host while keeping the token.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout == 0 {
		client.Timeout = 30 * time.Second
	}
	return &client
}

func (c *Client) baseURL(repo Repo) (string, error) {
	base := strings.TrimSpace(c.BaseURL)
	if base == "" {
		base = repo.APIBase()
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("合并请求接口地址无效")
	}
	return strings.TrimSuffix(base, "/"), nil
}

// CreateMergeRequest 在仓库里创建 source→target 的合并请求。调用前必须
// 已经推送来源分支。
func (c *Client) CreateMergeRequest(ctx context.Context, repo Repo, req MergeRequest) (MergeRequestRef, error) {
	title := strings.TrimSpace(req.Title)
	source := strings.TrimSpace(req.SourceBranch)
	target := strings.TrimSpace(req.TargetBranch)
	if title == "" || source == "" || target == "" {
		return MergeRequestRef{}, errors.New("合并请求需要标题、源分支和目标分支")
	}
	if !validRepo(repo) {
		return MergeRequestRef{}, errors.New("托管平台仓库路径无效")
	}
	if strings.TrimSpace(c.Token) == "" {
		return MergeRequestRef{}, errors.New("来源凭据缺少可创建合并请求的令牌")
	}
	base, err := c.baseURL(repo)
	if err != nil {
		return MergeRequestRef{}, err
	}
	var path string
	var payload any
	switch repo.Provider {
	case GitHub:
		path = "/repos/" + repo.Path + "/pulls"
		payload = map[string]string{"title": title, "body": req.Body, "head": source, "base": target}
	case GitLab:
		path = "/projects/" + url.PathEscape(repo.Path) + "/merge_requests"
		payload = map[string]string{"title": title, "description": req.Body, "source_branch": source, "target_branch": target}
	default:
		return MergeRequestRef{}, errors.New("不支持的托管平台")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return MergeRequestRef{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return MergeRequestRef{}, errors.New("无法创建合并请求")
	}
	c.setHeaders(request, repo)
	response, err := c.httpClient().Do(request)
	if err != nil {
		return MergeRequestRef{}, fmt.Errorf("无法连接托管平台：%s", providerLabel(repo.Provider))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return MergeRequestRef{}, &Error{Provider: repo.Provider, Status: response.StatusCode}
	}
	var decoded struct {
		Number  int    `json:"number"`
		IID     int    `json:"iid"`
		HTMLURL string `json:"html_url"`
		WebURL  string `json:"web_url"`
		State   string `json:"state"`
		Merged  bool   `json:"merged"`
	}
	if err := decodeLimited(response.Body, &decoded); err != nil {
		return MergeRequestRef{}, &Error{Provider: repo.Provider, Reason: "响应格式无法识别"}
	}
	iid := decoded.Number
	if repo.Provider == GitLab {
		iid = decoded.IID
	}
	webURL := strings.TrimSpace(decoded.HTMLURL)
	if webURL == "" {
		webURL = strings.TrimSpace(decoded.WebURL)
	}
	if iid <= 0 || webURL == "" {
		return MergeRequestRef{}, &Error{Provider: repo.Provider, Reason: "响应缺少合并请求编号或链接"}
	}
	return MergeRequestRef{IID: iid, URL: webURL, Provider: repo.Provider, State: normalizeState(decoded.State, decoded.Merged)}, nil
}

// MergeRequestState 查询合并请求状态，返回 opened / merged / closed。
func (c *Client) MergeRequestState(ctx context.Context, repo Repo, iid int) (string, error) {
	if !validRepo(repo) || iid <= 0 {
		return "", errors.New("合并请求参数无效")
	}
	base, err := c.baseURL(repo)
	if err != nil {
		return "", err
	}
	var path string
	switch repo.Provider {
	case GitHub:
		path = "/repos/" + repo.Path + "/pulls/" + strconv.Itoa(iid)
	case GitLab:
		path = "/projects/" + url.PathEscape(repo.Path) + "/merge_requests/" + strconv.Itoa(iid)
	default:
		return "", errors.New("不支持的托管平台")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return "", errors.New("无法查询合并请求状态")
	}
	c.setHeaders(request, repo)
	response, err := c.httpClient().Do(request)
	if err != nil {
		return "", fmt.Errorf("无法连接托管平台：%s", providerLabel(repo.Provider))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", &Error{Provider: repo.Provider, Status: response.StatusCode}
	}
	var decoded struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	if err := decodeLimited(response.Body, &decoded); err != nil {
		return "", &Error{Provider: repo.Provider, Reason: "响应格式无法识别"}
	}
	return normalizeState(decoded.State, decoded.Merged), nil
}

func (c *Client) setHeaders(request *http.Request, repo Repo) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	token := strings.TrimSpace(c.Token)
	switch repo.Provider {
	case GitHub:
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		request.Header.Set("Authorization", "Bearer "+token)
	case GitLab:
		request.Header.Set("PRIVATE-TOKEN", token)
	}
}

func decodeLimited(body io.Reader, out any) error {
	return json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(out)
}

func normalizeState(state string, merged bool) string {
	if merged {
		return "merged"
	}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "merged":
		return "merged"
	case "closed":
		return "closed"
	default:
		return "opened"
	}
}

func providerLabel(provider Provider) string {
	if provider == GitHub {
		return "GitHub"
	}
	if provider == GitLab {
		return "GitLab"
	}
	return "托管平台"
}

func validRepo(repo Repo) bool {
	if repo.Provider != GitHub && repo.Provider != GitLab {
		return false
	}
	path := strings.TrimSpace(repo.Path)
	if path == "" || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return false
	}
	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		return false
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, "\r\n\x00 ") {
			return false
		}
	}
	return true
}
