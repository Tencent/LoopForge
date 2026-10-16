package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"

	"github.com/gorilla/websocket"
)

// Client talks to the AgentFlow control plane over outbound HTTPS. It never
// opens a listener; the runner is unreachable from the network by design.

var ErrUnauthorized = errors.New("runner session rejected by server")

type Client struct {
	base    string
	http    *http.Client
	mu      sync.RWMutex
	session string
}

func NewClient(serverURL string) *Client {
	return &Client{
		base: strings.TrimRight(strings.TrimSpace(serverURL), "/"),
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) SetSession(token string) {
	c.mu.Lock()
	c.session = token
	c.mu.Unlock()
}

func (c *Client) sessionToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.session
}

// ---------- request plumbing ----------

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if session := c.sessionToken(); session != "" {
		req.Header.Set("Authorization", "Bearer "+session)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s %s: %s", method, path, strings.TrimSpace(string(payload)))
	}
	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return nil
}

// ---------- endpoints ----------

type RegisterRequest struct {
	RunnerID       string                 `json:"runnerId,omitempty"`
	Name           string                 `json:"name"`
	OS             string                 `json:"os"`
	Arch           string                 `json:"arch"`
	Version        string                 `json:"version"`
	Labels         []string               `json:"labels,omitempty"`
	MaxConcurrency int                    `json:"maxConcurrency"`
	Runtimes       []domain.RunnerRuntime `json:"runtimes,omitempty"`
}

type RegisterResponse struct {
	RunnerID          string    `json:"runnerId"`
	SessionToken      string    `json:"sessionToken"`
	ExpiresAt         time.Time `json:"expiresAt"`
	HeartbeatInterval int       `json:"heartbeatInterval"`
	LeaseTTL          int       `json:"leaseTTL"`
}

func (c *Client) Register(ctx context.Context, bootstrapToken string, req RegisterRequest) (*RegisterResponse, error) {
	var out RegisterResponse
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/runners/register", nil)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(bytes.NewReader(raw))
	request.ContentLength = int64(len(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Bootstrap-Token", bootstrapToken)
	resp, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("register: %s", strings.TrimSpace(string(payload)))
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("decode register response: %w", err)
	}
	return &out, nil
}

type HeartbeatRequest struct {
	Status       string                 `json:"status"`
	RunningTasks int                    `json:"runningTasks"`
	Version      string                 `json:"version,omitempty"`
	Runtimes     []domain.RunnerRuntime `json:"runtimes,omitempty"`
}

type HeartbeatResponse struct {
	ExpiresAt time.Time `json:"expiresAt"`
}

func (c *Client) Heartbeat(ctx context.Context, req HeartbeatRequest) (*HeartbeatResponse, error) {
	var out HeartbeatResponse
	if err := c.do(ctx, http.MethodPost, "/runners/heartbeat", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Deregister permanently removes this machine from the platform. It is used
// by explicit uninstall, not by an ordinary daemon stop or service restart.
func (c *Client) Deregister(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/runners/deregister", map[string]any{}, nil)
}

// Disconnect marks the machine offline but preserves its registration. It is
// used for ordinary daemon shutdown; uninstall uses Deregister instead.
func (c *Client) Disconnect(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/runners/disconnect", map[string]any{}, nil)
}

type ClaimRequest struct {
	AvailableRuntimes []string `json:"availableRuntimes"`
	Capacity          int      `json:"capacity"`
	Preflight         bool     `json:"preflight,omitempty"`
	NoWait            bool     `json:"noWait,omitempty"`
}

// WaitForTask long-polls without leasing work. Older servers ignore preflight
// and return a leased task, which the daemon can still execute.
func (c *Client) WaitForTask(ctx context.Context, req ClaimRequest) (bool, *domain.RunnerTask, error) {
	req.Preflight = true
	var out struct {
		TaskAvailable bool              `json:"taskAvailable"`
		Task          domain.RunnerTask `json:"task"`
	}
	if err := c.do(ctx, http.MethodPost, "/runners/tasks/claim", req, &out); err != nil {
		return false, nil, err
	}
	if out.Task.ID != "" {
		return true, &out.Task, nil
	}
	return out.TaskAvailable, nil, nil
}

// Claim long-polls the server. A 204 response means "no task right now" and
// is reported as (nil, false, nil); the caller simply polls again. The
// context deadline must exceed the server long-poll timeout.
func (c *Client) Claim(ctx context.Context, req ClaimRequest) (*domain.RunnerTask, bool, error) {
	var out struct {
		Task domain.RunnerTask `json:"task"`
	}
	if err := c.do(ctx, http.MethodPost, "/runners/tasks/claim", req, &out); err != nil {
		return nil, false, err
	}
	if out.Task.ID == "" {
		return nil, false, nil
	}
	return &out.Task, true, nil
}

func (c *Client) StartTask(ctx context.Context, taskID string) error {
	return c.do(ctx, http.MethodPost, "/runners/tasks/"+taskID+"/start", map[string]any{}, nil)
}

type RenewResponse struct {
	LeaseUntil   time.Time `json:"leaseUntil"`
	DesiredState string    `json:"desiredState"`
}

func (c *Client) RenewLease(ctx context.Context, taskID string) (*RenewResponse, error) {
	var out RenewResponse
	if err := c.do(ctx, http.MethodPost, "/runners/tasks/"+taskID+"/lease/renew", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) TaskControl(ctx context.Context, taskID string) (*RenewResponse, error) {
	var out RenewResponse
	if err := c.do(ctx, http.MethodGet, "/runners/tasks/"+taskID+"/control", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// OpenControl opens the runner's single server-push control channel. All
// task/event data remains on the durable HTTP API; this socket carries only
// low-latency signals whose state is also persisted in the task table.
func (c *Client) OpenControl(ctx context.Context) (*websocket.Conn, error) {
	u, err := url.Parse(c.base)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return nil, fmt.Errorf("unsupported runner server scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/runners/connect"
	header := http.Header{}
	if session := c.sessionToken(); session != "" {
		header.Set("Authorization", "Bearer "+session)
	}
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, u.String(), header)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		if resp != nil {
			defer resp.Body.Close()
			payload, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			return nil, fmt.Errorf("control websocket handshake (%s): %s", resp.Status, strings.TrimSpace(string(payload)))
		}
		return nil, err
	}
	return conn, nil
}

func (c *Client) SendEvents(ctx context.Context, taskID string, events []domain.RunnerTaskEvent) error {
	return c.do(ctx, http.MethodPost, "/runners/tasks/"+taskID+"/events", map[string]any{"events": events}, nil)
}

type CompleteRequest struct {
	Status    string                    `json:"status"`
	ExitCode  *int                      `json:"exitCode"`
	Result    string                    `json:"result"`
	Error     string                    `json:"error"`
	Artifacts map[string]string         `json:"artifacts,omitempty"`
	Outputs   *domain.RunnerTaskOutputs `json:"outputs,omitempty"`
}

func (c *Client) Complete(ctx context.Context, taskID string, req CompleteRequest) error {
	return c.do(ctx, http.MethodPost, "/runners/tasks/"+taskID+"/complete", req, nil)
}

func (c *Client) UploadPreview(ctx context.Context, previewID, token string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+"/runners/previews/"+previewID+"/content", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Preview-Upload-Token", token)
	if session := c.sessionToken(); session != "" {
		req.Header.Set("Authorization", "Bearer "+session)
	}
	httpClient := &http.Client{Timeout: 15 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("upload preview: %s", strings.TrimSpace(string(payload)))
	}
	return nil
}

func (c *Client) CompletePreview(ctx context.Context, previewID string) error {
	return c.do(ctx, http.MethodPost, "/runners/previews/"+previewID+"/complete", map[string]any{}, nil)
}
