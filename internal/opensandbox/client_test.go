package opensandbox

import (
	"context"
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
