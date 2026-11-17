package executionenv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is the worker-side adapter for an independently deployed sandbox
// manager. It only carries lease metadata; Docker and filesystem operations
// remain in the worker's runtime.Manager.
type Client struct {
	base  string
	token string
	http  *http.Client
}

func NewClient(base, token string) *Client {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil
	}
	return &Client{base: base, token: strings.TrimSpace(token), http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) request(ctx context.Context, path string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		var detail struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &detail)
		if detail.Error == "" {
			detail.Error = strings.TrimSpace(string(payload))
		}
		if resp.StatusCode == http.StatusConflict {
			if detail.Error == ErrLeaseLost.Error() {
				return ErrLeaseLost
			}
			if detail.Error == ErrRequestConflict.Error() {
				return ErrRequestConflict
			}
		}
		return fmt.Errorf("sandbox manager %s: %s", path, detail.Error)
	}
	if out != nil && len(payload) > 0 {
		return json.Unmarshal(payload, out)
	}
	return nil
}

func (c *Client) Acquire(ctx context.Context, req LeaseRequest) (Lease, error) {
	var out Lease
	err := c.request(ctx, "/v1/environments/acquire", req, &out)
	return out, err
}
func (c *Client) Renew(ctx context.Context, leaseID, workerID string, generation int, ttl time.Duration) (Lease, error) {
	var out Lease
	err := c.request(ctx, "/v1/environments/renew", map[string]any{"leaseId": leaseID, "workerId": workerID, "generation": generation, "leaseSeconds": int(ttl / time.Second)}, &out)
	return out, err
}
func (c *Client) Release(ctx context.Context, leaseID, workerID string, generation int) error {
	return c.request(ctx, "/v1/environments/release", map[string]any{"leaseId": leaseID, "workerId": workerID, "generation": generation}, nil)
}
