package managedagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const acpPath = "/v1/agents/acp"

type Config struct {
	Endpoint     string
	EnterpriseID string
	AgentID      string
	SessionID    string
	APIKey       string
	TencentCloud *TencentCloudConfig
	ACPOrigin    string // optional exact origin for a tenant's private ACP gateway
}

type WMA struct {
	config Config
	client http.Client
	base   string
	cloud  *tencentCloud
	gate   chan struct{}
}

var identifier = regexp.MustCompile("^[a-zA-Z0-9_-]{1,128}$")
var errorCode = regexp.MustCompile("^[a-zA-Z0-9_.-]{1,64}$")

func NewWMA(config Config, client *http.Client) (*WMA, error) {
	if config.ACPOrigin != "" {
		u, err := secureURL(config.ACPOrigin)
		if err != nil || (u.Path != "" && u.Path != "/") {
			return nil, errors.New("WMA ACP endpoint override must be an HTTPS origin")
		}
		config.ACPOrigin = "https://" + u.Host
	}
	if !identifier.MatchString(config.AgentID) || !identifier.MatchString(config.SessionID) {
		return nil, errors.New("WMA requires an agent and existing session ID")
	}
	if client == nil {
		client = http.DefaultClient
	}
	transport := *client
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	wma := &WMA{config: config, client: transport, gate: make(chan struct{}, 1)}
	if config.TencentCloud != nil {
		if config.APIKey != "" || config.Endpoint != "" {
			return nil, errors.New("choose one WMA control-plane authentication mode")
		}
		cloud, err := newTencentCloud(*config.TencentCloud, &wma.client)
		if err != nil {
			return nil, err
		}
		wma.cloud = cloud
		return wma, nil
	}
	if !identifier.MatchString(config.EnterpriseID) || (!strings.HasPrefix(config.APIKey, "ck_") && !strings.HasPrefix(config.APIKey, "pt_")) {
		return nil, errors.New("WMA OpenAPI requires an enterprise ID and a ck_ or pt_ credential")
	}
	if config.Endpoint == "" {
		config.Endpoint = "https://www.workbuddy.cn"
	}
	origin, err := secureURL(config.Endpoint)
	if err != nil || (origin.Path != "" && origin.Path != "/") {
		return nil, errors.New("WMA control endpoint must be an HTTPS origin")
	}
	wma.base = "https://" + origin.Host + "/v2/enterprises/" + config.EnterpriseID + "/managedagent"
	return wma, nil
}

// Run prompts a dedicated pre-provisioned WMA session. The same session accepts
// only one active prompt. A turn with an unknown outcome is never replayed here.
func (w *WMA) Run(parent context.Context, prompt string, onText func(string)) (string, error) {
	ctx, cancel := context.WithTimeout(parent, turnTimeout)
	defer cancel()
	output, err := w.run(ctx, prompt, onText)
	return output, turnTimeoutError("WMA", ctx, parent, err)
}

func (w *WMA) run(ctx context.Context, prompt string, onText func(string)) (string, error) {
	select {
	case w.gate <- struct{}{}:
		defer func() { <-w.gate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	endpoint, err := w.sessionEndpoint(ctx)
	if err != nil {
		return "", err
	}
	var token struct {
		AccessToken string `json:"accessToken"`
	}
	if err := w.accessToken(ctx, &token); err != nil {
		return "", err
	}
	if token.AccessToken == "" {
		return "", errors.New("WMA did not return a data-plane access token")
	}
	connection, err := w.connect(ctx, endpoint, token.AccessToken)
	if err != nil {
		return "", err
	}
	defer connection.disconnect()

	if _, err := connection.call("initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]bool{"readTextFile": false, "writeTextFile": false},
		},
	}, nil); err != nil {
		return "", err
	}
	if _, err := connection.call("session/load", map[string]any{
		"sessionId": w.config.SessionID, "cwd": "/workspace", "mcpServers": []any{},
	}, nil); err != nil {
		return "", err
	}
	return connection.prompt(prompt, onText)
}

func (w *WMA) sessionEndpoint(ctx context.Context) (string, error) {
	var session struct {
		SessionID string `json:"sessionId"`
		AgentID   string `json:"agentId"`
		Endpoints []struct {
			Type string `json:"endpointType"`
			URL  string `json:"url"`
		} `json:"endpointSet"`
	}
	path := "/agents/" + w.config.AgentID + "/sessions/" + w.config.SessionID
	var err error
	if w.cloud != nil {
		err = w.cloud.call(ctx, "DescribeAgentSession", map[string]string{"SessionId": w.config.SessionID}, &session)
	} else {
		err = w.control(ctx, http.MethodGet, path, &session)
	}
	if err != nil {
		return "", err
	}
	if session.SessionID != w.config.SessionID || session.AgentID != w.config.AgentID {
		return "", errors.New("WMA session does not match the configured agent and session")
	}
	for _, endpoint := range session.Endpoints {
		u, err := secureURL(endpoint.URL)
		if err != nil || (u.Path != "" && u.Path != "/" && u.Path != acpPath) {
			continue
		}
		origin := "https://" + u.Host
		defaultOrigin := "https://" + strings.ToLower(w.config.AgentID) + ".ma.workbuddy.cn"
		if w.config.ACPOrigin == origin || (w.config.ACPOrigin == "" && endpoint.Type == "PUBLIC" && strings.EqualFold(defaultOrigin, origin)) {
			return origin + acpPath, nil
		}
	}
	return "", errors.New("WMA session has no trusted ACP endpoint")
}

func (w *WMA) accessToken(ctx context.Context, output any) error {
	if w.cloud != nil {
		return w.cloud.call(ctx, "DescribeUserAccessToken", struct{}{}, output)
	}
	return w.control(ctx, http.MethodPost, "/user-access-token", output)
}

func secureURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return nil, errors.New("invalid HTTPS endpoint")
	}
	return u, nil
}

func (w *WMA) control(ctx context.Context, method, path string, output any) error {
	ctx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	var body io.Reader
	if method == http.MethodPost {
		body = bytes.NewBufferString("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, w.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.HasPrefix(w.config.APIKey, "ck_") {
		req.Header.Set("X-Api-Key", w.config.APIKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+w.config.APIKey)
	}
	res, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("WMA control request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("WMA control request: HTTP %d", res.StatusCode)
	}
	var envelope struct {
		Code json.RawMessage `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&envelope); err != nil || envelope.Code == nil {
		return errors.New("invalid WMA control response")
	}
	code := strings.Trim(string(envelope.Code), `"`)
	if code != "0" {
		if errorCode.MatchString(code) {
			return fmt.Errorf("WMA control request: API code %s", code)
		}
		return errors.New("WMA control request rejected")
	}
	if err := json.Unmarshal(envelope.Data, output); err != nil {
		return errors.New("invalid WMA control response data")
	}
	return nil
}
