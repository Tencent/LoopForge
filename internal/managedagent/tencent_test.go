package managedagent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type cloudTestTransport func(*http.Request) (*http.Response, error)

func (f cloudTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWMATencentControl(t *testing.T) {
	client := &http.Client{Transport: cloudTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Host != "workbuddyenterprise.ap-beijing.tencentcloudapi.com" ||
			r.Header.Get("X-TC-Version") != tencentVersion || r.Header.Get("X-TC-Token") != "temporary-test-token" ||
			!strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=example-id/") {
			t.Fatal("missing signed Tencent Cloud headers")
		}
		body := ""
		switch r.Header.Get("X-TC-Action") {
		case "DescribeAgentSession":
			var request struct{ SessionId string }
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.SessionId != "session-test" {
				t.Fatal("wrong session request")
			}
			body = `{"Response":{"AgentId":"agent-test","SessionId":"session-test","EndpointSet":[{"EndpointType":"PUBLIC","Url":"https://agent-test.ma.workbuddy.cn"}]}}`
		case "DescribeUserAccessToken":
			body = `{"Response":{"Error":{"Code":"UnauthorizedOperation.Test"}}}`
		default:
			t.Fatalf("unexpected action %q", r.Header.Get("X-TC-Action"))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	wma, err := NewWMA(Config{AgentID: "agent-test", SessionID: "session-test", TencentCloud: &TencentCloudConfig{
		SecretID: "example-id", SecretKey: "example-test-secret", Token: "temporary-test-token",
	}}, client)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint, err := wma.sessionEndpoint(context.Background()); err != nil || endpoint != "https://agent-test.ma.workbuddy.cn"+acpPath {
		t.Fatalf("unexpected ACP endpoint %q: %v", endpoint, err)
	}
	if err := wma.accessToken(context.Background(), new(map[string]any)); err == nil || !strings.Contains(err.Error(), "UnauthorizedOperation.Test") {
		t.Fatalf("unexpected Tencent Cloud error: %v", err)
	}
}
