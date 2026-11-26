package managedagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TextAgent is a remotely managed text turn. Callers own scheduling and
// publish the completed text through the normal workflow artifact path.
type TextAgent interface {
	Run(context.Context, string, func(string)) (string, error)
}

type A2AConfig struct {
	Endpoint string
	Token    string
	// Loopback permits a personal Runner to use an Agent on the same host.
	Loopback bool
}

type A2A struct {
	endpoint string
	token    string
	client   http.Client
}

func NewA2A(config A2AConfig, client *http.Client) (*A2A, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" || endpoint.RawQuery != "" || endpoint.ForceQuery {
		return nil, errors.New("invalid A2A endpoint")
	}
	if config.Loopback {
		ip := net.ParseIP(endpoint.Hostname())
		if ip == nil || !ip.IsLoopback() || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return nil, errors.New("personal Runner A2A endpoint must use a literal loopback address")
		}
	} else if endpoint.Scheme != "https" {
		return nil, errors.New("remotely configured A2A endpoint requires HTTPS")
	}
	if client == nil {
		client = http.DefaultClient
	}
	transport := *client
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &A2A{endpoint: endpoint.String(), token: config.Token, client: transport}, nil
}

type a2aPart struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
}

type a2aMessage struct {
	Kind      string    `json:"kind"`
	MessageID string    `json:"messageId,omitempty"`
	Role      string    `json:"role"`
	Parts     []a2aPart `json:"parts"`
}

type a2aResult struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	a2aMessage
	Status struct {
		State   string      `json:"state"`
		Message *a2aMessage `json:"message"`
	} `json:"status"`
	Artifacts []struct {
		Parts []a2aPart `json:"parts"`
	} `json:"artifacts"`
	History []a2aMessage `json:"history"`
}

func (a *A2A) Run(ctx context.Context, prompt string, onText func(string)) (string, error) {
	if len(prompt) > maxACPText {
		return "", errors.New("A2A prompt exceeds 1 MiB")
	}
	ctx, stop := context.WithTimeout(ctx, 5*time.Minute)
	defer stop()
	var result a2aResult
	err := a.rpc(ctx, "message/send", map[string]any{
		"message": a2aMessage{
			Kind: "message", MessageID: rand.Text(), Role: "user",
			Parts: []a2aPart{{Kind: "text", Text: prompt}},
		},
		"configuration": map[string]any{
			"acceptedOutputModes": []string{"text/plain"},
			"blocking":            false,
			"historyLength":       1,
		},
	}, &result)
	if err != nil {
		return "", err
	}

	switch result.Kind {
	case "message":
		if result.Role != "agent" {
			return "", errors.New("A2A response is not an Agent message")
		}
	case "task":
		var err error
		result, err = a.waitTask(ctx, result)
		if err != nil {
			return "", err
		}
	default:
		return "", errors.New("A2A response must be a Message or Task")
	}
	output := a2aText(result)
	if strings.TrimSpace(output) == "" {
		return "", errors.New("A2A Agent returned no text")
	}
	if len(output) > maxACPText {
		return "", errors.New("A2A text result exceeds 1 MiB")
	}
	if onText != nil {
		onText(output)
	}
	return output, nil
}

func (a *A2A) waitTask(ctx context.Context, result a2aResult) (a2aResult, error) {
	id := result.ID
	if id == "" || len(id) > 256 {
		return a2aResult{}, errors.New("A2A response lacks a valid task ID")
	}
	defer func() {
		if ctx.Err() != nil && (result.Status.State == "submitted" || result.Status.State == "working") {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = a.rpc(cleanup, "tasks/cancel", map[string]string{"id": id}, nil)
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		switch result.Status.State {
		case "completed":
			return result, nil
		case "submitted", "working":
			select {
			case <-ctx.Done():
				return a2aResult{}, ctx.Err()
			case <-ticker.C:
			}
		case "failed", "rejected", "canceled", "input-required", "auth-required":
			return a2aResult{}, fmt.Errorf("A2A task ended in state %s", result.Status.State)
		default:
			return a2aResult{}, errors.New("A2A task returned an unsupported state")
		}
		var next a2aResult
		if err := a.rpc(ctx, "tasks/get", map[string]any{"id": id, "historyLength": 1}, &next); err != nil {
			return a2aResult{}, err
		}
		if next.Kind != "task" || next.ID != id {
			return a2aResult{}, errors.New("A2A returned a different task during polling")
		}
		result = next
	}
}

func a2aText(result a2aResult) string {
	var output strings.Builder
	writeParts := func(parts []a2aPart) {
		for _, part := range parts {
			if part.Kind == "text" {
				output.WriteString(part.Text)
			}
		}
	}
	if result.Kind == "message" {
		writeParts(result.Parts)
		return output.String()
	}
	for _, artifact := range result.Artifacts {
		writeParts(artifact.Parts)
	}
	if output.Len() > 0 {
		return output.String()
	}
	if result.Status.Message != nil && result.Status.Message.Role == "agent" {
		writeParts(result.Status.Message.Parts)
	}
	for i := len(result.History) - 1; output.Len() == 0 && i >= 0; i-- {
		if result.History[i].Role == "agent" {
			writeParts(result.History[i].Parts)
		}
	}
	return output.String()
}

func (a *A2A) rpc(ctx context.Context, method string, params any, output any) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	res, err := a.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("A2A %s: connection failed", method)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("A2A %s: HTTP %d", method, res.StatusCode)
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&envelope); err != nil || envelope.JSONRPC != "2.0" || envelope.ID != 1 {
		return fmt.Errorf("A2A %s: invalid JSON-RPC response", method)
	}
	if envelope.Error != nil {
		return fmt.Errorf("A2A %s: JSON-RPC error %d", method, envelope.Error.Code)
	}
	if output != nil && json.Unmarshal(envelope.Result, output) != nil {
		return fmt.Errorf("A2A %s: invalid result", method)
	}
	return nil
}
