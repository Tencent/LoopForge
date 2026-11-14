package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The identity lookup runs only after GitHub has issued a token; its own
// failure must be reported as-is instead of being masked as "missing login",
// because the HTTP status hint (e.g. organization OAuth policy) is what the
// user needs to fix the problem.
func TestGitHubExchangeDeviceCodeKeepsIdentityFailure(t *testing.T) {
	cases := []struct {
		name     string
		identity func() *http.Response
		wantErr  string
	}{
		{
			name: "identity http error",
			identity: func() *http.Response {
				return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(`{"message":"org policy"}`)), Header: make(http.Header)}
			},
			wantErr: "GitHub 请求失败（HTTP 403），请检查授权与组织访问策略",
		},
		{
			name:     "identity invalid json",
			identity: func() *http.Response { return githubTestResponse(`{`) },
			wantErr:  "GitHub 返回了无效响应",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, _ := githubTestHandler(t)
			identityCalls := 0
			handler.client.Transport = githubTestTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() == "https://api.github.com/user" {
					identityCalls++
					return tc.identity(), nil
				}
				return githubTestResponse(`{"access_token":"issued"}`), nil
			})
			current, _ := githubTestContext(0, http.MethodPost, "/api/auth/github/poll", "")
			result, login, err := handler.exchangeDeviceCode(current, "client", "device")
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("err=%v, want %q", err, tc.wantErr)
			}
			if login != "" || identityCalls != 1 || result.AccessToken != "issued" {
				t.Fatalf("result=%+v login=%q identityCalls=%d", result, login, identityCalls)
			}
		})
	}
}

// poll must not store a token whose account could not be resolved, and the
// grant stays usable so the user can retry after fixing access.
func TestGitHubPollDoesNotStoreTokenWhenIdentityFails(t *testing.T) {
	handler, userID := githubTestHandler(t)
	handler.client.Transport = githubTestTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://github.com/login/device/code":
			return githubTestResponse(`{"device_code":"code","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`), nil
		case "https://github.com/login/oauth/access_token":
			return githubTestResponse(`{"access_token":"unverified-token"}`), nil
		case "https://api.github.com/user":
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		}
		t.Fatalf("unexpected endpoint %s", request.URL)
		return nil, nil
	})
	current, recorder := githubTestContext(userID, http.MethodPost, "/api/auth/github/device", "{}")
	handler.start(current)
	if recorder.Code != http.StatusOK {
		t.Fatalf("start=%d %s", recorder.Code, recorder.Body.String())
	}
	grant := handler.grants[userID]
	grant.next = time.Now().Add(-time.Second)
	pollBody, _ := json.Marshal(map[string]string{"flowId": grant.id})
	current, recorder = githubTestContext(userID, http.MethodPost, "/api/auth/github/poll", string(pollBody))
	handler.poll(current)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "HTTP 403") || strings.Contains(recorder.Body.String(), "unverified-token") {
		t.Fatalf("poll=%d %s", recorder.Code, recorder.Body.String())
	}
	user, _ := handler.repo.User(userID)
	if user.GitToken != "" {
		t.Fatal("token stored although identity lookup failed")
	}
	if handler.grants[userID] != grant || grant.busy {
		t.Fatal("grant should stay available for retry")
	}
}
