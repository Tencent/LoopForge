package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func TestVerifyGongfengTokenRejectsPrivateToken(t *testing.T) {
	var userHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/personal_access_tokens/self":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case "/user":
			userHits++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"username":"someone","private_token":"private-token"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	restoreGongfengAPI(t, srv.URL)

	got := verifyGongfengToken(context.Background(), "private-token")
	if got.Valid {
		t.Fatal("a private token that can open /user must not count as a clone credential")
	}
	if !strings.Contains(got.Error, "个人访问令牌") {
		t.Fatalf("error=%q", got.Error)
	}
	if userHits != 1 {
		t.Fatalf("/user fallback was called %d times", userHits)
	}
}

func TestVerifyGongfengTokenRequiresCloneScopes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/personal_access_tokens/self" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true,"revoked":false,"scopes":["read_repository"],"user":{"username":"someone"}}`))
	}))
	t.Cleanup(srv.Close)
	restoreGongfengAPI(t, srv.URL)

	got := verifyGongfengToken(context.Background(), "pat")
	if got.Valid || got.Error != "" {
		t.Fatalf("valid=%v error=%q", got.Valid, got.Error)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "write_repository" {
		t.Fatalf("missing=%v", got.Missing)
	}
}

func TestVerifyGongfengTokenAcceptsPatWithBothScopes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true,"revoked":false,"scopes":["read_repository","write_repository"],"user":{"username":"someone"}}`))
	}))
	t.Cleanup(srv.Close)
	restoreGongfengAPI(t, srv.URL)

	got := verifyGongfengToken(context.Background(), "pat")
	if !got.Valid || got.Error != "" || got.Login != "someone" {
		t.Fatalf("%+v", got)
	}
}

func restoreGongfengAPI(t *testing.T, base string) {
	t.Helper()
	prev := gongfengAPIBase
	gongfengAPIBase = base
	t.Cleanup(func() { gongfengAPIBase = prev })
}

type gitTokenTestTransport func(*http.Request) (*http.Response, error)

func (f gitTokenTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVerifyGongfengToken(t *testing.T) {
	cases := []struct {
		name, selfBody, userBody string
		selfStatus, userStatus   int
		valid                    bool
		errorContains, login     string
	}{
		{name: "private token rejected", selfStatus: 404, userStatus: 200, userBody: `{"username":"alice","private_token":"test-token"}`, errorContains: "私人令牌"},
		{name: "pat accepted when self returns not found", selfStatus: 404, userStatus: 200, userBody: `{"username":"alice","private_token":"different-private-token"}`, valid: true, login: "alice"},
		{name: "user without private token field", selfStatus: 404, userStatus: 200, userBody: `{"username":"alice"}`, valid: true, login: "alice"},
		{name: "malformed fallback rejected", selfStatus: 404, userStatus: 200, userBody: `not json`, errorContains: "无法解析"},
		{name: "invalid fallback token", selfStatus: 404, userStatus: 401, errorContains: "无效或已过期"},
		{name: "fallback upstream error", selfStatus: 404, userStatus: 503, errorContains: "HTTP 503"},
		{name: "valid PAT scopes", selfStatus: 200, selfBody: `{"active":true,"scopes":["read_repository","write_repository"],"user":{"username":"alice"}}`, valid: true, login: "alice"},
		{name: "missing write scope", selfStatus: 200, selfBody: `{"active":true,"scopes":["read_repository"]}`},
		{name: "revoked PAT", selfStatus: 200, selfBody: `{"active":true,"revoked":true}`, errorContains: "撤销"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			userCalls := 0
			http.DefaultTransport = gitTokenTestTransport(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("PRIVATE-TOKEN") != "test-token" && req.Header.Get("Authorization") != "Bearer test-token" {
					t.Fatal("missing token authentication")
				}
				status, body := tc.selfStatus, tc.selfBody
				switch req.URL.Path {
				case "/api/v3/personal_access_tokens/self":
				case "/api/v3/user":
					userCalls++
					status, body = tc.userStatus, tc.userBody
				default:
					t.Fatalf("unexpected request: %s", req.URL.Path)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			result := verifyGongfengToken(context.Background(), "test-token")
			if result.Valid != tc.valid || result.Login != tc.login {
				t.Fatalf("unexpected result: %+v", result)
			}
			if tc.errorContains != "" && !strings.Contains(result.Error, tc.errorContains) {
				t.Fatalf("expected %q, got %+v", tc.errorContains, result)
			}
			if strings.Contains(result.Error, "test-token") {
				t.Fatal("token leaked in error")
			}
			if tc.selfStatus != 404 && userCalls != 0 {
				t.Fatal("successful self endpoint should not call user")
			}
			if tc.name == "missing write scope" && (len(result.Missing) != 1 || result.Missing[0] != "write_repository") {
				t.Fatalf("missing scope: %+v", result)
			}
		})
	}
}

// Users usually paste a token and click 校验 before saving: POST verifies the
// typed token, GET falls back to the saved one, and only for oneself.
func TestVerifyGitTokenAcceptsUnsavedToken(t *testing.T) {
	h, u := workbenchFixture(t)
	users := &usersHandlers{deps: h.deps}
	var seen []string
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = gitTokenTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.github.com" || req.URL.Path != "/user" {
			t.Fatalf("token sent to %s", req.URL)
		}
		seen = append(seen, strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
		body := `{"login":"alice"}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	call := func(user domain.User, method, body string) (int, gitTokenVerifyResult) {
		c, w := workbenchContext(user, method, "/", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(u.ID, 10)}}
		users.verifyGitToken(c)
		var got gitTokenVerifyResult
		json.Unmarshal(w.Body.Bytes(), &got)
		return w.Code, got
	}
	if code, got := call(u, "GET", ""); code != 200 || got.Valid || !strings.Contains(got.Error, "尚未配置") {
		t.Fatalf("no saved token: %d %+v", code, got)
	}
	if code, got := call(u, "POST", `{"token":" typed-token "}`); code != 200 || !got.Valid || seen[len(seen)-1] != "typed-token" {
		t.Fatalf("typed token: %d %+v %v", code, got, seen)
	}
	if saved, _ := h.repo.User(u.ID); saved.GitToken != "" {
		t.Fatal("verification must not save the token")
	}
	if code, got := call(u, "POST", `{"token":""}`); code != 200 || !strings.Contains(got.Error, "尚未配置") {
		t.Fatalf("empty typed token: %d %+v", code, got)
	}
	other := domain.User{ID: u.ID + 1000, LoginName: "intruder"}
	if code, _ := call(other, "POST", `{"token":"x"}`); code != 403 {
		t.Fatalf("other user verified: %d", code)
	}
}
