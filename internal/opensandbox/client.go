package opensandbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

const (
	bindingMetadata = "loopforge-binding"
	BootstrapPrefix = "lfos_"
)

var (
	ErrNotFound = errors.New("OpenSandbox sandbox not found")
	safeID      = regexp.MustCompile("^[a-zA-Z0-9_-]{1,128}$")
)

type Config struct {
	Endpoint         string `json:"endpoint"`
	APIKey           string `json:"apiKey"`
	RunnerURL        string `json:"runnerUrl"`
	RunnerImage      string `json:"runnerImage"`
	Scope            string `json:"scope"`
	RunnerSigningKey string `json:"runnerSigningKey"`
}

type Client struct {
	config Config
	base   string
	http   http.Client
}

type Sandbox struct {
	ID       string            `json:"id"`
	Metadata map[string]string `json:"metadata"`
	Status   struct {
		State string `json:"state"`
	} `json:"status"`
}

func New(config Config, client *http.Client) (*Client, error) {
	endpoint, err := cleanURL(config.Endpoint)
	if err != nil || (endpoint.Path != "" && endpoint.Path != "/" && endpoint.Path != "/v1") {
		return nil, errors.New("OpenSandbox endpoint must be a service origin or its /v1 base")
	}
	if _, err := cleanURL(config.RunnerURL); err != nil {
		return nil, errors.New("OpenSandbox Runner callback URL is required")
	}
	if config.APIKey == "" || config.RunnerImage == "" || !safeID.MatchString(config.Scope) || len(config.RunnerSigningKey) < 32 {
		return nil, errors.New("OpenSandbox requires an API key, Runner image, scope and Runner signing key of at least 32 bytes")
	}
	if client == nil {
		client = http.DefaultClient
	}
	transport := *client
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{config: config, base: endpoint.Scheme + "://" + endpoint.Host + "/v1", http: transport}, nil
}

func cleanURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("invalid service URL")
	}
	return u, nil
}

// WorkspaceFromBootstrap decodes the workspace locator. Verify the signature
// with its pinned backend before authorizing registration.
func WorkspaceFromBootstrap(token string) (string, bool) {
	encoded, ok := strings.CutPrefix(token, BootstrapPrefix)
	if !ok || len(encoded) > 220 {
		return "", false
	}
	id, signature, ok := strings.Cut(encoded, ".")
	if !ok || len(signature) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return "", false
	}
	workspace, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || !safeID.Match(workspace) {
		return "", false
	}
	return string(workspace), true
}

func (c *Client) ValidBootstrap(token, workspace string) bool {
	return hmac.Equal([]byte(token), []byte(c.bootstrap(workspace)))
}

func (c *Client) bootstrap(workspace string) string {
	mac := hmac.New(sha256.New, []byte(c.config.RunnerSigningKey))
	mac.Write([]byte(c.config.Scope + "/" + workspace))
	return BootstrapPrefix + base64.RawURLEncoding.EncodeToString([]byte(workspace)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c *Client) binding(workspace string) string {
	hash := sha256.Sum256([]byte(c.config.Scope + "/" + workspace))
	// OpenSandbox stores metadata as Kubernetes labels, limited to 63 characters.
	return hex.EncodeToString(hash[:24])
}

// Find recovers a create whose response was lost. The provider may return
// multiple matches if an operator duplicated a resource; never select one.
func (c *Client) Find(ctx context.Context, workspace string) (Sandbox, error) {
	binding := c.binding(workspace)
	filter := url.Values{"metadata": {bindingMetadata + "=" + binding}, "pageSize": {"2"}}
	var page struct {
		Items      []Sandbox `json:"items"`
		Pagination struct {
			HasNextPage bool `json:"hasNextPage"`
			TotalItems  int  `json:"totalItems"`
		} `json:"pagination"`
	}
	if err := c.do(ctx, http.MethodGet, "/sandboxes?"+filter.Encode(), nil, &page); err != nil {
		return Sandbox{}, err
	}
	if len(page.Items) > 1 || page.Pagination.HasNextPage || page.Pagination.TotalItems > 1 {
		return Sandbox{}, errors.New("multiple OpenSandbox sandboxes found for the workspace")
	}
	if len(page.Items) == 0 {
		return Sandbox{}, ErrNotFound
	}
	sandbox := page.Items[0]
	if !safeID.MatchString(sandbox.ID) || sandbox.Metadata[bindingMetadata] != binding {
		return Sandbox{}, errors.New("OpenSandbox list returned an unexpected sandbox")
	}
	return sandbox, nil
}

func (c *Client) Create(ctx context.Context, workspace string) (Sandbox, error) {
	input := map[string]any{
		"image":          map[string]string{"uri": c.config.RunnerImage},
		"entrypoint":     []string{"/usr/local/bin/agentflow-runner", "-auto-update=false", "-concurrency=1"},
		"timeout":        int((2 * time.Hour) / time.Second),
		"resourceLimits": map[string]string{"cpu": "1", "memory": "2Gi"},
		"metadata":       map[string]string{bindingMetadata: c.binding(workspace)},
		"env": map[string]string{
			"AGENTFLOW_SERVER_URL":             c.config.RunnerURL,
			"AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN": c.bootstrap(workspace),
			"AGENTFLOW_RUNNER_NAME":            "OpenSandbox " + workspace,
			"AGENTFLOW_RUNNER_STATE_PATH":      "/home/runner/.agentflow-runner/state.json",
			"AGENTFLOW_RUNNER_WORKSPACE":       "/home/runner/workspace",
		},
	}
	var sandbox Sandbox
	if err := c.do(ctx, http.MethodPost, "/sandboxes", input, &sandbox); err != nil {
		return Sandbox{}, err
	}
	if !safeID.MatchString(sandbox.ID) {
		return Sandbox{}, errors.New("OpenSandbox create did not return a valid sandbox ID")
	}
	return sandbox, nil
}

func (c *Client) Inspect(ctx context.Context, workspace, id string) (Sandbox, error) {
	if !safeID.MatchString(id) {
		return Sandbox{}, errors.New("invalid OpenSandbox sandbox ID")
	}
	var sandbox Sandbox
	if err := c.do(ctx, http.MethodGet, "/sandboxes/"+id, nil, &sandbox); err != nil {
		return Sandbox{}, err
	}
	if sandbox.ID != id || sandbox.Metadata[bindingMetadata] != c.binding(workspace) {
		return Sandbox{}, errors.New("OpenSandbox sandbox does not belong to the workspace")
	}
	return sandbox, nil
}

func (c *Client) Release(ctx context.Context, workspace, id string) error {
	if _, err := c.Inspect(ctx, workspace, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	err := c.do(ctx, http.MethodDelete, "/sandboxes/"+id, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (c *Client) do(ctx context.Context, method, path string, input, output any) error {
	requestTimeout := 20 * time.Second
	if method == http.MethodPost {
		// Docker can pull an image and prepare the container before responding.
		requestTimeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("OPEN-SANDBOX-API-KEY", c.config.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("OpenSandbox %s: %w", method, err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound && strings.HasPrefix(path, "/sandboxes/") {
		return ErrNotFound
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("OpenSandbox %s: HTTP %d", method, res.StatusCode)
	}
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(output); err != nil {
		return fmt.Errorf("OpenSandbox %s: invalid response", method)
	}
	return nil
}
