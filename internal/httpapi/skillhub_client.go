package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	skillhubDefaultBaseURL  = "https://api.skillhub.cn"
	skillhubSiteURL         = "https://skillhub.cn"
	skillhubRequestTimeout  = 15 * time.Second
	skillhubDownloadTimeout = 60 * time.Second
	skillhubRetryDelay      = 400 * time.Millisecond
	skillhubCategoryTTL     = 10 * time.Minute
	skillhubJSONLimit       = 4 << 20
	skillhubMaxRedirects    = 5
)

// Headers that identify the platform to SkillHub. They must never follow a
// redirect to another host (signed COS download links).
const (
	skillhubAPIKeyHeader = "X-API-Key"
	skillhubUserHeader   = "X-Client-User-Id"
)

// skillhubError is a failed SkillHub call: Status is what the platform API
// answers, Upstream the SkillHub status (0 for transport failures).
type skillhubError struct {
	Status   int
	Upstream int
	Message  string
}

func (e *skillhubError) Error() string { return e.Message }

func isSkillhubNotFound(err error) bool {
	var se *skillhubError
	return errors.As(err, &se) && se.Upstream == http.StatusNotFound
}

type skillhubCategory struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// skillhubClient talks to the SkillHub Open API. Base URL and API key come
// from server configuration only and never reach the browser.
type skillhubClient struct {
	baseURL         string
	apiKey          string
	http            *http.Client
	timeout         time.Duration
	downloadTimeout time.Duration
	retryDelay      time.Duration
	categoryTTL     time.Duration

	mu           sync.Mutex
	categories   []skillhubCategory
	categoriesAt time.Time
}

func newSkillhubClientFromEnv() *skillhubClient {
	return newSkillhubClient(os.Getenv("SKILLHUB_BASE_URL"), os.Getenv("SKILLHUB_API_KEY"))
}

func newSkillhubClient(baseURL, apiKey string) *skillhubClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = skillhubDefaultBaseURL
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	return &skillhubClient{
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(apiKey),
		http: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= skillhubMaxRedirects {
					return errors.New("too many redirects")
				}
				if req.URL.Host != via[0].URL.Host {
					req.Header.Del(skillhubAPIKeyHeader)
					req.Header.Del(skillhubUserHeader)
				}
				return nil
			},
		},
		timeout:         skillhubRequestTimeout,
		downloadTimeout: skillhubDownloadTimeout,
		retryDelay:      skillhubRetryDelay,
		categoryTTL:     skillhubCategoryTTL,
	}
}

// skillhubUserKey pseudonymises the caller for X-Client-User-Id.
func skillhubUserKey(id int64, login string) string {
	raw := strings.TrimSpace(login)
	if id > 0 {
		raw = strconv.FormatInt(id, 10)
	}
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:16]
}

// get performs one GET (following redirects) and returns at most limit
// bytes; 429 and 5xx answers are retried once.
func (c *skillhubClient) get(ctx context.Context, path string, query url.Values, userKey string, timeout time.Duration, limit int64) ([]byte, error) {
	body, err := c.getOnce(ctx, path, query, userKey, timeout, limit)
	var se *skillhubError
	if err != nil && errors.As(err, &se) && (se.Upstream == http.StatusTooManyRequests || se.Upstream >= 500) {
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(c.retryDelay):
		}
		return c.getOnce(ctx, path, query, userKey, timeout, limit)
	}
	return body, err
}

func (c *skillhubClient) getOnce(ctx context.Context, path string, query url.Values, userKey string, timeout time.Duration, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &skillhubError{Status: http.StatusBadGateway, Message: "SkillHub 地址配置无效"}
	}
	req.Header.Set("Accept", "application/json, */*")
	if c.apiKey != "" {
		req.Header.Set(skillhubAPIKeyHeader, c.apiKey)
	}
	if userKey != "" {
		req.Header.Set(skillhubUserHeader, userKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, skillhubTransportError(ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, skillhubTransportError(ctx, err)
	}
	if resp.StatusCode >= 400 {
		return nil, skillhubStatusError(resp.StatusCode, body)
	}
	if int64(len(body)) > limit {
		return nil, &skillhubError{Status: http.StatusUnprocessableEntity, Upstream: http.StatusRequestEntityTooLarge, Message: "SkillHub 返回内容超过平台限额"}
	}
	return body, nil
}

func skillhubTransportError(ctx context.Context, err error) error {
	var netErr net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &skillhubError{Status: http.StatusGatewayTimeout, Message: "SkillHub 响应超时，请稍后重试"}
	}
	return &skillhubError{Status: http.StatusBadGateway, Message: "无法连接 SkillHub，请检查网络后重试"}
}

func skillhubStatusError(status int, body []byte) error {
	var payload struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &payload)
	remote := strings.TrimSpace(payload.Error)
	if remote == "" {
		remote = strings.TrimSpace(payload.Message)
	}
	if r := []rune(remote); len(r) > 200 {
		remote = string(r[:200])
	}
	out := &skillhubError{Status: http.StatusBadGateway, Upstream: status}
	switch {
	case status == http.StatusNotFound:
		out.Status, out.Message = http.StatusNotFound, "SkillHub 上未找到该技能或文件（可能已下架或不可见）"
	case status == http.StatusRequestEntityTooLarge:
		out.Status, out.Message = http.StatusUnprocessableEntity, "文件超过 SkillHub 单文件上限"
	case status == http.StatusTooManyRequests:
		out.Status, out.Message = http.StatusTooManyRequests, "SkillHub 请求过于频繁，请稍后重试"
	case status >= 500:
		out.Message = fmt.Sprintf("SkillHub 服务暂时不可用（HTTP %d），请稍后重试", status)
	default:
		out.Message = fmt.Sprintf("SkillHub 请求失败（HTTP %d）", status)
		if remote != "" {
			out.Message += "：" + remote
		}
	}
	return out
}

func (c *skillhubClient) getJSON(ctx context.Context, path string, query url.Values, userKey string, v any) error {
	body, err := c.get(ctx, path, query, userKey, c.timeout, skillhubJSONLimit)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return &skillhubError{Status: http.StatusBadGateway, Message: "SkillHub 返回了无法解析的数据"}
	}
	return nil
}

// --- Open API calls ---

type skillhubNamespace struct {
	Handle      string `json:"handle"`
	DisplayName string `json:"displayName"`
}

type skillhubRemoteListSkill struct {
	Slug          string            `json:"slug"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	DescriptionZh string            `json:"description_zh"`
	OwnerName     string            `json:"ownerName"`
	Category      string            `json:"category"`
	Version       string            `json:"version"`
	IconURL       string            `json:"iconUrl"`
	Downloads     int64             `json:"downloads"`
	Stars         int64             `json:"stars"`
	Labels        map[string]any    `json:"labels"`
	UpdatedAt     int64             `json:"updated_at"`
	Verified      bool              `json:"verified"`
	Namespace     skillhubNamespace `json:"namespace"`
}

type skillhubRemoteList struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Skills []skillhubRemoteListSkill `json:"skills"`
		Total  int64                     `json:"total"`
	} `json:"data"`
}

func (c *skillhubClient) list(ctx context.Context, query url.Values, userKey string) (skillhubRemoteList, error) {
	var out skillhubRemoteList
	if err := c.getJSON(ctx, "/api/skills", query, userKey, &out); err != nil {
		return out, err
	}
	if out.Code != 0 {
		msg := "SkillHub 列表接口返回错误"
		if strings.TrimSpace(out.Message) != "" {
			msg += "：" + strings.TrimSpace(out.Message)
		}
		return out, &skillhubError{Status: http.StatusBadGateway, Message: msg}
	}
	return out, nil
}

func (c *skillhubClient) categoryList(ctx context.Context, userKey string) ([]skillhubCategory, error) {
	c.mu.Lock()
	if c.categories != nil && time.Since(c.categoriesAt) < c.categoryTTL {
		out := c.categories
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()
	var remote struct {
		Items []struct {
			Key       string `json:"key"`
			Name      string `json:"name"`
			Active    *bool  `json:"active"`
			SortOrder int    `json:"sortOrder"`
		} `json:"items"`
	}
	if err := c.getJSON(ctx, "/api/v1/categories", nil, userKey, &remote); err != nil {
		return nil, err
	}
	out := make([]skillhubCategory, 0, len(remote.Items))
	for _, item := range remote.Items {
		if item.Active != nil && !*item.Active {
			continue
		}
		if strings.TrimSpace(item.Key) == "" {
			continue
		}
		out = append(out, skillhubCategory{Key: item.Key, Name: strings.TrimSpace(firstNonEmpty(item.Name, item.Key))})
	}
	c.mu.Lock()
	c.categories, c.categoriesAt = out, time.Now()
	c.mu.Unlock()
	return out, nil
}

type skillhubRemoteVersion struct {
	Version         string          `json:"version"`
	Changelog       string          `json:"changelog"`
	CreatedAt       int64           `json:"createdAt"`
	SecurityReports json.RawMessage `json:"securityReports"`
}

type skillhubRemoteDetail struct {
	Namespace skillhubNamespace `json:"namespace"`
	Owner     struct {
		DisplayName string `json:"displayName"`
		Handle      string `json:"handle"`
	} `json:"owner"`
	LatestVersion   *skillhubRemoteVersion `json:"latestVersion"`
	SecurityReports json.RawMessage        `json:"securityReports"`
	Skill           struct {
		Slug          string             `json:"slug"`
		DisplayName   string             `json:"displayName"`
		Summary       string             `json:"summary"`
		SummaryZh     string             `json:"summary_zh"`
		Category      string             `json:"category"`
		IconURL       string             `json:"iconUrl"`
		CreatedAt     int64              `json:"createdAt"`
		Labels        map[string]any     `json:"labels"`
		SubCategories []skillhubCategory `json:"subCategories"`
		Stats         struct {
			Downloads int64 `json:"downloads"`
			Stars     int64 `json:"stars"`
			Installs  int64 `json:"installs"`
			Versions  int64 `json:"versions"`
		} `json:"stats"`
	} `json:"skill"`
}

func skillhubScope(namespace string) url.Values {
	q := url.Values{}
	if namespace != "" {
		q.Set("namespace", namespace)
	}
	return q
}

func skillhubSkillPath(slug, suffix string) string {
	return "/api/v1/skills/" + url.PathEscape(slug) + suffix
}

func (c *skillhubClient) detail(ctx context.Context, slug, namespace, userKey string) (skillhubRemoteDetail, error) {
	var out skillhubRemoteDetail
	err := c.getJSON(ctx, skillhubSkillPath(slug, ""), skillhubScope(namespace), userKey, &out)
	return out, err
}

func (c *skillhubClient) versions(ctx context.Context, slug, namespace, userKey string) ([]skillhubRemoteVersion, error) {
	var out struct {
		Versions []skillhubRemoteVersion `json:"versions"`
	}
	err := c.getJSON(ctx, skillhubSkillPath(slug, "/versions"), skillhubScope(namespace), userKey, &out)
	return out.Versions, err
}

type skillhubFileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

func (c *skillhubClient) files(ctx context.Context, slug, namespace, version, userKey string) ([]skillhubFileEntry, error) {
	q := skillhubScope(namespace)
	if version != "" {
		q.Set("version", version)
	}
	var out struct {
		Files []skillhubFileEntry `json:"files"`
	}
	err := c.getJSON(ctx, skillhubSkillPath(slug, "/files"), q, userKey, &out)
	return out.Files, err
}

// file follows the 302 to the signed COS address server-side and returns at
// most limit bytes of the file.
func (c *skillhubClient) file(ctx context.Context, slug, namespace, version, filePath, userKey string, limit int64) ([]byte, error) {
	q := skillhubScope(namespace)
	q.Set("path", filePath)
	if version != "" {
		q.Set("version", version)
	}
	return c.get(ctx, skillhubSkillPath(slug, "/file"), q, userKey, c.timeout, limit)
}

type skillhubRemoteEvaluation struct {
	Dimensions  json.RawMessage `json:"dimensions"`
	Summary     string          `json:"summary"`
	UserSummary string          `json:"userSummary"`
	UpdatedAt   int64           `json:"updatedAt"`
	CreatedAt   int64           `json:"createdAt"`
}

func (c *skillhubClient) evaluation(ctx context.Context, slug, namespace, userKey string) (skillhubRemoteEvaluation, error) {
	var out skillhubRemoteEvaluation
	err := c.getJSON(ctx, skillhubSkillPath(slug, "/evaluation"), skillhubScope(namespace), userKey, &out)
	return out, err
}

// download fetches the zip of one version. The signed COS address in the
// redirect is used once and never cached or exposed.
func (c *skillhubClient) download(ctx context.Context, slug, namespace, version, userKey string, limit int64) ([]byte, error) {
	q := skillhubScope(namespace)
	q.Set("slug", slug)
	if version != "" {
		q.Set("version", version)
	}
	body, err := c.get(ctx, "/api/v1/download", q, userKey, c.downloadTimeout, limit)
	var se *skillhubError
	if errors.As(err, &se) && se.Upstream == http.StatusRequestEntityTooLarge {
		return nil, &skillhubError{Status: http.StatusUnprocessableEntity, Upstream: se.Upstream, Message: "技能包超过平台 64MB 总量限额"}
	}
	return body, err
}
