package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

type githubTestTransport func(*http.Request) (*http.Response, error)

func (transport githubTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func githubTestHandler(t *testing.T) (*githubHandlers, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("AGENTFLOW_GITHUB_CLIENT_ID", "test-client")
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "")
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.PutUser(domain.User{LoginName: "github-owner"}); err != nil {
		t.Fatal(err)
	}
	user, _ := store.UserByLoginName("github-owner")
	return newGitHubHandlers(&deps{repo: store, authClient: auth.NewClient(auth.Config{SessionSecret: "github-test-secret"})}), user.ID
}

func githubTestContext(userID int64, method, endpoint, body string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	current, _ := gin.CreateTestContext(recorder)
	current.Request = httptest.NewRequest(method, "http://loopforge.test"+endpoint, strings.NewReader(body))
	current.Request.Header.Set("Origin", "http://loopforge.test")
	current.Request.Header.Set("Content-Type", "application/json")
	if userID > 0 {
		current.Request = current.Request.WithContext(context.WithValue(current.Request.Context(), requestUserContextKey{}, &auth.User{ID: userID, LoginName: "github-owner"}))
	}
	return current, recorder
}

func githubTestResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestGitHubApplicationConfiguration(t *testing.T) {
	handler, userID := githubTestHandler(t)
	t.Setenv("AGENTFLOW_GITHUB_CLIENT_ID", "")
	current, recorder := githubTestContext(userID, http.MethodPut, "/api/auth/github/config", `{"clientId":"Iv1.testapplication"}`)
	handler.configure(current)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin changed application: %d", recorder.Code)
	}
	if _, err := handler.repo.GrantPlatformAdmin("github-owner"); err != nil {
		t.Fatal(err)
	}
	current, recorder = githubTestContext(userID, http.MethodPut, "/api/auth/github/config", `{"clientId":"Iv1.testapplication"}`)
	handler.configure(current)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"enabled":true`) {
		t.Fatalf("configuration: %d %s", recorder.Code, recorder.Body)
	}
	restarted := newGitHubHandlers(handler.deps)
	if clientID, err := restarted.clientID(); err != nil || clientID != "Iv1.testapplication" {
		t.Fatalf("configuration not persisted: %q %v", clientID, err)
	}
	for _, scenario := range []struct {
		origin, body string
		expected     int
	}{
		{"https://other.test", `{"clientId":"Iv1.otherapplication"}`, http.StatusForbidden},
		{"http://loopforge.test", `{"clientId":"bad secret"}`, http.StatusBadRequest},
	} {
		current, recorder = githubTestContext(userID, http.MethodPut, "/api/auth/github/config", scenario.body)
		current.Request.Header.Set("Origin", scenario.origin)
		handler.configure(current)
		if recorder.Code != scenario.expected {
			t.Fatalf("configuration accepted invalid request: %d", recorder.Code)
		}
	}
	t.Setenv("AGENTFLOW_GITHUB_CLIENT_ID", "environment-client")
	current, recorder = githubTestContext(userID, http.MethodPut, "/api/auth/github/config", `{"clientId":"Iv1.otherapplication"}`)
	handler.configure(current)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("overwrote managed configuration: %d", recorder.Code)
	}
}

func TestGitHubDeviceFlowStoresOnlyCurrentUsersGrant(t *testing.T) {
	handler, userID := githubTestHandler(t)
	tokenCalls := 0
	handler.client.Transport = githubTestTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://github.com/login/device/code":
			_ = request.ParseForm()
			if request.Form.Get("client_id") != "test-client" || request.Form.Get("scope") != "repo" {
				t.Fatalf("device form=%v", request.Form)
			}
			return githubTestResponse(`{"device_code":"server-only-code","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`), nil
		case "https://github.com/login/oauth/access_token":
			tokenCalls++
			_ = request.ParseForm()
			if request.Form.Get("device_code") != "server-only-code" {
				t.Fatal("missing device code")
			}
			if tokenCalls == 1 {
				return githubTestResponse(`{"error":"authorization_pending"}`), nil
			}
			if tokenCalls == 2 {
				return githubTestResponse(`{"error":"slow_down"}`), nil
			}
			return githubTestResponse(`{"access_token":"test-private-token","token_type":"bearer"}`), nil
		case "https://api.github.com/user":
			if request.Header.Get("Authorization") != "Bearer test-private-token" {
				t.Fatal("missing bearer")
			}
			return githubTestResponse(`{"login":"authorized-account"}`), nil
		default:
			t.Fatalf("unexpected endpoint %s", request.URL)
			return nil, nil
		}
	})
	current, recorder := githubTestContext(userID, http.MethodPost, "/api/auth/github/device", "{}")
	handler.start(current)
	if recorder.Code != http.StatusOK || bytes.Contains(recorder.Body.Bytes(), []byte("server-only-code")) {
		t.Fatalf("start=%d %s", recorder.Code, recorder.Body.String())
	}
	grant := handler.grants[userID]
	pollBody, _ := json.Marshal(map[string]string{"flowId": grant.id})
	current, recorder = githubTestContext(userID+1, http.MethodPost, "/api/auth/github/poll", string(pollBody))
	handler.poll(current)
	if recorder.Code != http.StatusBadRequest {
		t.Fatal("other user could poll this grant")
	}
	current, recorder = githubTestContext(userID, http.MethodPost, "/api/auth/github/poll", string(pollBody))
	handler.poll(current)
	if tokenCalls != 0 {
		t.Fatal("polled GitHub before interval")
	}
	for attempt := 0; attempt < 3; attempt++ {
		grant.next = time.Now().Add(-time.Second)
		current, recorder = githubTestContext(userID, http.MethodPost, "/api/auth/github/poll", string(pollBody))
		handler.poll(current)
		if recorder.Code != http.StatusOK {
			t.Fatalf("poll %d: %d %s", attempt, recorder.Code, recorder.Body.String())
		}
	}
	if grant.interval != 10*time.Second {
		t.Fatalf("slow_down interval=%s", grant.interval)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("test-private-token")) || !strings.Contains(recorder.Body.String(), "authorized-account") {
		t.Fatalf("public result=%s", recorder.Body.String())
	}
	user, _ := handler.repo.User(userID)
	if user.GitToken != "test-private-token" || user.GongfengRefreshToken != "" {
		t.Fatal("grant not stored correctly")
	}
	if handler.grants[userID] != nil {
		t.Fatal("completed grant can be replayed")
	}
}

func TestGitHubReconfigurationCancelsInFlightGrants(t *testing.T) {
	for _, stage := range []string{"start", "poll"} {
		t.Run(stage, func(t *testing.T) {
			handler, userID := githubTestHandler(t)
			t.Setenv("AGENTFLOW_GITHUB_CLIENT_ID", "")
			if _, err := handler.repo.GrantPlatformAdmin("github-owner"); err != nil {
				t.Fatal(err)
			}
			if err := handler.repo.UsersRepo.SetGitHubClientID("Iv1.original-application"); err != nil {
				t.Fatal(err)
			}
			disable := func() {
				current, recorder := githubTestContext(userID, http.MethodPut, "/api/auth/github/config", `{"clientId":""}`)
				handler.configure(current)
				if recorder.Code != http.StatusOK {
					t.Fatalf("disable application: %d %s", recorder.Code, recorder.Body)
				}
			}
			handler.client.Transport = githubTestTransport(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/login/device/code":
					if stage == "start" {
						disable()
					}
					return githubTestResponse(`{"device_code":"server-only-code","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`), nil
				case "/login/oauth/access_token":
					disable()
					return githubTestResponse(`{"access_token":"must-not-be-saved"}`), nil
				case "/user":
					return githubTestResponse(`{"login":"github-account"}`), nil
				default:
					t.Fatalf("unexpected endpoint: %s", request.URL)
					return nil, nil
				}
			})
			current, recorder := githubTestContext(userID, http.MethodPost, "/api/auth/github/device", "{}")
			handler.start(current)
			if stage == "start" {
				if recorder.Code != http.StatusConflict || strings.Contains(recorder.Body.String(), "ABCD-EFGH") {
					t.Fatalf("disabled grant exposed: %d %s", recorder.Code, recorder.Body)
				}
			} else {
				if recorder.Code != http.StatusOK {
					t.Fatalf("start: %d %s", recorder.Code, recorder.Body)
				}
				grant := handler.grants[userID]
				grant.next = time.Now().Add(-time.Second)
				payload, _ := json.Marshal(map[string]string{"flowId": grant.id})
				current, recorder = githubTestContext(userID, http.MethodPost, "/api/auth/github/poll", string(payload))
				handler.poll(current)
				if recorder.Code != http.StatusBadRequest {
					t.Fatalf("disabled grant completed: %d %s", recorder.Code, recorder.Body)
				}
			}
			user, _ := handler.repo.User(userID)
			if user.GitToken != "" || len(handler.grants) != 0 {
				t.Fatal("disabled application retained a grant or saved its token")
			}
		})
	}
}

func TestGitHubReplicaRejectsChangedApplication(t *testing.T) {
	handler, userID := githubTestHandler(t)
	t.Setenv("AGENTFLOW_GITHUB_CLIENT_ID", "")
	if err := handler.repo.UsersRepo.SetGitHubClientID("Iv1.replacement-application"); err != nil {
		t.Fatal(err)
	}
	handler.grants[userID] = &githubGrant{id: "flow", code: "old-app-code", clientID: "Iv1.original-application", expires: time.Now().Add(time.Minute)}
	handler.client.Transport = githubTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("a replica contacted GitHub with a disabled application grant")
		return nil, nil
	})
	current, recorder := githubTestContext(userID, http.MethodPost, "/api/auth/github/poll", `{"flowId":"flow"}`)
	handler.poll(current)
	if recorder.Code != http.StatusConflict || len(handler.grants) != 0 {
		t.Fatalf("stale replica grant accepted: %d %s", recorder.Code, recorder.Body)
	}
}

func TestGitHubDeviceFlowRejectsInvalidRequests(t *testing.T) {
	for _, scenario := range []string{"anonymous", "origin", "disabled", "expired", "denied", "expiring", "upstream"} {
		t.Run(scenario, func(t *testing.T) {
			handler, userID := githubTestHandler(t)
			handler.grants[userID] = &githubGrant{id: "flow", code: "code", clientID: "test-client", expires: time.Now().Add(time.Minute), interval: 5 * time.Second}
			handler.client.Transport = githubTestTransport(func(*http.Request) (*http.Response, error) {
				if scenario == "upstream" {
					return &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader("private upstream detail"))}, nil
				}
				if scenario == "expiring" {
					return githubTestResponse(`{"access_token":"temporary-token","expires_in":28800,"login":"account"}`), nil
				}
				return githubTestResponse(`{"error":"access_denied"}`), nil
			})
			actor := userID
			if scenario == "anonymous" {
				actor = 0
			}
			current, recorder := githubTestContext(actor, http.MethodPost, "/api/auth/github/poll", `{"flowId":"flow"}`)
			if scenario == "origin" {
				current.Request.Header.Set("Origin", "https://other.test")
			}
			if scenario == "expired" {
				handler.grants[userID].expires = time.Now().Add(-time.Second)
			}
			if scenario == "disabled" {
				t.Setenv("AGENTFLOW_GITHUB_CLIENT_ID", "")
				handler.start(current)
			} else {
				handler.poll(current)
			}
			if recorder.Code < 400 || strings.Contains(recorder.Body.String(), "private upstream detail") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			user, _ := handler.repo.User(userID)
			if user.GitToken != "" {
				t.Fatal("invalid flow overwrote credentials")
			}
		})
	}
}

func TestGitHubRepositoryPickerUsesCurrentUsersTokenAndPages(t *testing.T) {
	handler, userID := githubTestHandler(t)
	_, err := handler.repo.UpdateUserCredentials(userID, repo.UserCredentialUpdate{GitToken: "oauth2:owner-only-token"})
	if err != nil {
		t.Fatal(err)
	}
	handler.client.Transport = githubTestTransport(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer owner-only-token" || request.URL.Query().Get("page") != "2" || request.URL.Query().Get("affiliation") != "owner,collaborator,organization_member" {
			t.Fatalf("repository request=%s", request.URL)
		}
		return githubTestResponse(`[{"full_name":"team/private-repository","html_url":"https://github.com/team/private-repository","default_branch":"develop","private":true}]`), nil
	})
	current, recorder := githubTestContext(userID, http.MethodGet, "/api/auth/github/repositories?page=2", "")
	handler.repositories(current)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"default_branch":"develop"`) || strings.Contains(recorder.Body.String(), "owner-only-token") {
		t.Fatalf("repositories=%d %s", recorder.Code, recorder.Body.String())
	}
	current, recorder = githubTestContext(userID, http.MethodGet, "/api/auth/github/repositories?page=bad", "")
	handler.repositories(current)
	if recorder.Code != http.StatusBadRequest {
		t.Fatal("invalid page accepted")
	}
}
