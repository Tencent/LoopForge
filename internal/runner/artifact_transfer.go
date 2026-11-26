package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"agentflow/internal/domain"
)

func (c *Client) uploadTaskOutput(ctx context.Context, taskID, path string, body io.Reader, size int64) (domain.ArtifactBlob, error) {
	var blob domain.ArtifactBlob
	response, err := c.taskArtifactRequest(ctx, http.MethodPut, taskID, "output-file", path, body, size)
	if err != nil {
		return blob, err
	}
	defer response.Body.Close()
	err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&blob)
	return blob, err
}

func (c *Client) openTaskInput(ctx context.Context, taskID, path string) (io.ReadCloser, error) {
	response, err := c.taskArtifactRequest(ctx, http.MethodGet, taskID, "input-file", path, nil, 0)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func (c *Client) taskArtifactRequest(ctx context.Context, method, taskID, action, path string, body io.Reader, size int64) (*http.Response, error) {
	requestPath := "/runners/tasks/" + url.PathEscape(taskID) + "/" + action + "?path=" + url.QueryEscape(path)
	if method == http.MethodPut && size == 0 {
		body = http.NoBody
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+requestPath, body)
	if err != nil {
		return nil, err
	}
	if method == http.MethodPut {
		req.ContentLength = size
		req.Header.Set("Content-Type", "application/octet-stream")
		if size != 0 {
			req.Header.Set("Expect", "100-continue")
		}
	}
	if session := c.sessionToken(); session != "" {
		req.Header.Set("Authorization", "Bearer "+session)
	}
	client := *c.http
	client.Timeout = 5 * time.Minute
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusOK {
		return response, nil
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
	return nil, runnerResponseError(method, requestPath, response.StatusCode, payload)
}

// Retrying the transfer leaves the already executed workload untouched.
func retryTaskTransfer(ctx context.Context, transfer func() error) error {
	for attempt := 0; ; attempt++ {
		err := transfer()
		if err == nil || attempt == 2 || ctx.Err() != nil || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrTaskLeaseLost) {
			return err
		}
		var response *runnerHTTPError
		if errors.As(err, &response) && response.status < 500 && response.status != http.StatusTooManyRequests && response.status != http.StatusRequestTimeout {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
