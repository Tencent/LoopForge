package opensandbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCreateAllowsColdImageStartup(t *testing.T) {
	transport := &sandboxCreateTransport{}
	client, err := New(Config{
		Endpoint: "https://sandbox.example", RunnerURL: "https://loopforge.example",
		APIKey: "fixture", RunnerImage: "runner:test", Scope: "test",
		RunnerSigningKey: strings.Repeat("x", 32),
	}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Create(context.Background(), "run-test"); err != nil {
		t.Fatal(err)
	}
	if transport.budget < time.Minute || transport.budget > 2*time.Minute {
		t.Fatalf("sandbox creation budget = %s, want between one and two minutes", transport.budget)
	}
}

type sandboxCreateTransport struct{ budget time.Duration }

func (t *sandboxCreateTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	deadline, _ := request.Context().Deadline()
	t.budget = time.Until(deadline)
	return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"id":"sandbox-test"}`))}, nil
}

// A definitive refusal must be distinguishable from an unknown outcome: only
// 408 and 429 (retryable, outcome unknown) stay plain errors.
func TestRequestStatusClassification(t *testing.T) {
	cases := []struct {
		name, method, path string
		status             int
		rejected           bool
		notFound           bool
		plain              bool
	}{
		{"bad request", http.MethodPost, "/sandboxes", http.StatusBadRequest, true, false, false},
		{"forbidden", http.MethodPost, "/sandboxes", http.StatusForbidden, true, false, false},
		{"conflict", http.MethodPost, "/sandboxes", http.StatusConflict, true, false, false},
		{"unprocessable", http.MethodPost, "/sandboxes", http.StatusUnprocessableEntity, true, false, false},
		{"client timeout", http.MethodPost, "/sandboxes", http.StatusRequestTimeout, false, false, true},
		{"rate limited", http.MethodPost, "/sandboxes", http.StatusTooManyRequests, false, false, true},
		{"server error", http.MethodPost, "/sandboxes", http.StatusBadGateway, false, false, true},
		{"sandbox gone", http.MethodDelete, "/sandboxes/sbx-1", http.StatusNotFound, false, true, false},
		{"collection gone", http.MethodGet, "/sandboxes", http.StatusNotFound, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := &statusTransport{method: tc.method, path: "/v1" + tc.path}
			client, err := New(Config{
				Endpoint: "https://sandbox.example", RunnerURL: "https://loopforge.example",
				APIKey: "fixture", RunnerImage: "runner:test", Scope: "test",
				RunnerSigningKey: strings.Repeat("x", 32),
			}, &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			transport.response = &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(""))}
			err = client.do(context.Background(), tc.method, tc.path, nil, nil)
			var rejected *RejectedError
			if got := errors.As(err, &rejected); got != tc.rejected {
				t.Fatalf("error %v classified as rejected=%v, want %v", err, got, tc.rejected)
			}
			if got := errors.Is(err, ErrNotFound); got != tc.notFound {
				t.Fatalf("error %v classified as not-found=%v, want %v", err, got, tc.notFound)
			}
			if tc.plain && (err == nil || rejected != nil || errors.Is(err, ErrNotFound)) {
				t.Fatalf("error %v should stay a plain retryable error", err)
			}
			if tc.rejected && (!strings.Contains(err.Error(), "HTTP") || !strings.Contains(err.Error(), tc.method)) {
				t.Fatalf("rejected error %q lost the method/status text", err)
			}
		})
	}
}

type statusTransport struct {
	method, path string
	response     *http.Response
}

func (t *statusTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != t.method || request.URL.Path != t.path {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	return t.response, nil
}
