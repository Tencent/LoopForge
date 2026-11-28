package workbuddy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"agentflow/internal/domain"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	cloudError "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	api "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/workbuddyenterprise/v20260709"
)

const loopbackEndpointEnv = "AGENTFLOW_WMA_LOOPBACK_ENDPOINT"

var httpClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type Client struct {
	api      *api.Client
	loopback *url.URL
}

type Session struct {
	ID       string
	Endpoint string
}

type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func New(config domain.WorkBuddyConfig, key domain.ProjectAKSK) (*Client, error) {
	if err := config.ValidateRegion(); err != nil {
		return nil, err
	}
	settings := profile.NewClientProfile()
	region := config.EffectiveRegion()
	settings.HttpProfile.Endpoint = "workbuddyenterprise." + region + ".tencentcloudapi.com"
	settings.HttpProfile.ReqTimeout = 20
	// CreateAgentSession has no idempotency token.
	settings.NetworkFailureMaxRetries = 0
	settings.RateLimitExceededMaxRetries = 0
	settings.UnsafeRetryOnConnectionFailure = false
	client := &Client{}
	if raw := strings.TrimSpace(os.Getenv(loopbackEndpointEnv)); raw != "" {
		target, err := url.Parse(raw)
		if err != nil || target.Scheme != "http" || !net.ParseIP(target.Hostname()).IsLoopback() ||
			target.User != nil || target.RawQuery != "" || target.Fragment != "" || (target.Path != "" && target.Path != "/") {
			return nil, fmt.Errorf("%s 必须是本机 HTTP 地址", loopbackEndpointEnv)
		}
		settings.HttpProfile.Endpoint = target.Host
		settings.HttpProfile.Scheme = target.Scheme
		client.loopback = target
	}
	creds := common.NewTokenCredential(key.SecretID, key.SecretKey, key.Token)
	var err error
	client.api, err = api.NewClient(creds, region, settings)
	if err != nil {
		return nil, err
	}
	client.api.WithHttpTransport(noRedirectTransport{})
	return client, nil
}

// Reject redirects before the SDK could forward a temporary security token.
type noRedirectTransport struct{}

func (noRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err == nil && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		resp.Body.Close()
		return nil, errors.New("腾讯云 API 返回了重定向")
	}
	return resp, err
}

func (c *Client) Models(ctx context.Context) ([]Model, error) {
	request := api.NewDescribeBuiltinModelListRequest()
	request.Limit = common.Int64Ptr(100)
	request.Filters = []*api.Filter{{Name: common.StringPtr("Status"), Values: common.StringPtrs([]string{"ENABLED"}), ExactMatch: common.BoolPtr(true)}}
	models := []Model{}
	for offset := int64(0); ; {
		request.Offset = common.Int64Ptr(offset)
		result, err := c.api.DescribeBuiltinModelListWithContext(ctx, request)
		if err != nil {
			return nil, apiError("DescribeBuiltinModelList", err)
		}
		if result.Response == nil {
			return nil, errors.New("腾讯云接口未返回 WorkBuddy 模型列表")
		}
		page := result.Response.BuiltinModelSet
		for _, item := range page {
			if item == nil || value(item.ModelId) == "" || (item.SupportsToolCall != nil && !*item.SupportsToolCall) {
				continue
			}
			name := value(item.Name)
			if name == "" {
				name = value(item.ModelId)
			}
			models = append(models, Model{ID: value(item.ModelId), Name: name})
		}
		offset += int64(len(page))
		if len(page) == 0 || offset >= value(result.Response.TotalCount) {
			return models, nil
		}
	}
}

func (c *Client) AccessToken(ctx context.Context) (string, error) {
	result, err := c.api.DescribeUserAccessTokenWithContext(ctx, api.NewDescribeUserAccessTokenRequest())
	if err != nil {
		return "", apiError("DescribeUserAccessToken", err)
	}
	if result.Response == nil || value(result.Response.AccessToken) == "" {
		return "", errors.New("腾讯云接口未返回 WorkBuddy 访问令牌")
	}
	return value(result.Response.AccessToken), nil
}

func (c *Client) CreateSession(ctx context.Context, config domain.WorkBuddyConfig) (Session, error) {
	request := api.NewCreateAgentSessionRequest()
	request.AgentId = common.StringPtr(strings.TrimSpace(config.AgentID))
	if version := strings.TrimSpace(config.VersionID); version != "" {
		request.VersionId = common.StringPtr(version)
	}
	result, err := c.api.CreateAgentSessionWithContext(ctx, request)
	if err != nil {
		return Session{}, apiError("CreateAgentSession", err)
	}
	if result.Response == nil || value(result.Response.SessionId) == "" {
		return Session{}, errors.New("腾讯云接口未返回 WorkBuddy 会话 ID")
	}
	session := Session{ID: value(result.Response.SessionId)}
	for _, endpoint := range result.Response.EndpointSet {
		if endpoint != nil && value(endpoint.EndpointType) == "PUBLIC" {
			session.Endpoint = value(endpoint.Url)
			break
		}
	}
	return session, nil
}

func (c *Client) serviceURL(raw string, forACP bool) (*url.URL, error) {
	target, err := url.Parse(raw)
	if err != nil || target.Host == "" || target.User != nil || target.Fragment != "" {
		return nil, errors.New("WorkBuddy 服务地址无效")
	}
	if c.loopback != nil && target.Scheme == c.loopback.Scheme && target.Host == c.loopback.Host {
		return target, nil
	}
	if target.Scheme != "https" || (forACP && !strings.HasSuffix(strings.ToLower(target.Hostname()), ".ma.workbuddy.cn")) {
		return nil, errors.New("WorkBuddy 返回了不受支持的服务地址")
	}
	return target, nil
}

func apiError(action string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("腾讯云 %s：%w", action, err)
	}
	var issue *cloudError.TencentCloudSDKError
	if errors.As(err, &issue) {
		return fmt.Errorf("腾讯云 %s 失败（%s，RequestId %s）", action, issue.GetCode(), issue.GetRequestId())
	}
	return fmt.Errorf("腾讯云 %s 请求失败", action)
}

func value[T ~string | ~int64](v *T) T {
	if v != nil {
		return *v
	}
	var zero T
	return zero
}
