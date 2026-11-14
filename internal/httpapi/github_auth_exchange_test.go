package httpapi

import (
	"errors"
	"net/http"
	"testing"
)

func TestGitHubExchangeDeviceCodeResolvesLogin(t *testing.T) {
	handler, _ := githubTestHandler(t)
	var calls []string
	handler.client.Transport = githubTestTransport(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.Method+" "+request.URL.String())
		switch request.URL.String() {
		case "https://github.com/login/oauth/access_token":
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if request.Form.Get("client_id") != "client" || request.Form.Get("device_code") != "device" || request.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Fatalf("unexpected form: %v", request.Form)
			}
			return githubTestResponse(`{"access_token":"issued"}`), nil
		case "https://api.github.com/user":
			if request.Header.Get("Authorization") != "Bearer issued" {
				t.Fatalf("identity request used %q", request.Header.Get("Authorization"))
			}
			return githubTestResponse(`{"login":"octo"}`), nil
		}
		t.Fatalf("unexpected request %s", request.URL)
		return nil, nil
	})
	current, _ := githubTestContext(0, http.MethodPost, "/api/auth/github/poll", "")
	result, login, err := handler.exchangeDeviceCode(current, "client", "device")
	if err != nil || result.AccessToken != "issued" || login != "octo" || len(calls) != 2 {
		t.Fatalf("result=%+v login=%q err=%v calls=%v", result, login, err, calls)
	}
}

func TestGitHubExchangeDeviceCodePendingSkipsIdentity(t *testing.T) {
	handler, _ := githubTestHandler(t)
	calls := 0
	handler.client.Transport = githubTestTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.String() != "https://github.com/login/oauth/access_token" {
			t.Fatalf("pending grant must not query %s", request.URL)
		}
		return githubTestResponse(`{"error":"slow_down"}`), nil
	})
	current, _ := githubTestContext(0, http.MethodPost, "/api/auth/github/poll", "")
	result, login, err := handler.exchangeDeviceCode(current, "client", "device")
	if err != nil || result.Error != "slow_down" || result.AccessToken != "" || login != "" || calls != 1 {
		t.Fatalf("result=%+v login=%q err=%v calls=%d", result, login, err, calls)
	}
}

func TestGitHubExchangeDeviceCodeRejectsMissingLogin(t *testing.T) {
	handler, _ := githubTestHandler(t)
	handler.client.Transport = githubTestTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == "https://api.github.com/user" {
			return githubTestResponse(`{}`), nil
		}
		return githubTestResponse(`{"access_token":"issued","expires_in":28800}`), nil
	})
	current, _ := githubTestContext(0, http.MethodPost, "/api/auth/github/poll", "")
	result, login, err := handler.exchangeDeviceCode(current, "client", "device")
	if err == nil || err.Error() != "GitHub 未返回账号信息" || login != "" || result.ExpiresIn != 28800 {
		t.Fatalf("result=%+v login=%q err=%v", result, login, err)
	}
}

func TestGitHubExchangeDeviceCodeReportsTransportFailure(t *testing.T) {
	handler, _ := githubTestHandler(t)
	handler.client.Transport = githubTestTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})
	current, _ := githubTestContext(0, http.MethodPost, "/api/auth/github/poll", "")
	_, login, err := handler.exchangeDeviceCode(current, "client", "device")
	if err == nil || err.Error() != "无法连接 GitHub，请稍后重试" || login != "" {
		t.Fatalf("login=%q err=%v", login, err)
	}
}
