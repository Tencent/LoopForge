package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingSSOServer 返回一个记录请求次数与最后一次 code 的 SSO 网关替身。
func countingSSOServer(t *testing.T, body string) (*httptest.Server, *int32, *atomic.Value) {
	t.Helper()
	var hits int32
	var lastCode atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		lastCode.Store(r.URL.Query().Get("code"))
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &lastCode
}

const guardOKBody = `{"Ret":0,"Data":{"StaffId":42,"LoginName":" carol ","ChineseName":"  ","DeptId":9,"DeptName":" 基础架构 "}}`

func assertGuest(t *testing.T, user *User, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("ExchangeCode must never return an error, got %v", err)
	}
	if user == nil || !user.IsGuest || user.StaffID != 0 || user.LoginName != "guest" || user.ChineseName != "访客" || user.DeptID != 0 {
		t.Fatalf("want clean guest fallback without partial identity, got %+v", user)
	}
}

func TestExchangeCodeTrimsCodeBeforeCallingGateway(t *testing.T) {
	srv, hits, lastCode := countingSSOServer(t, guardOKBody)
	c := NewClient(Config{PaaSID: "paas", AppToken: "token", AuthHost: srv.URL, AuthURI: "/sso"})
	c.SetHTTPClient(srv.Client())

	user, err := c.ExchangeCode(context.Background(), " \tabc123 \n")
	if err != nil || user == nil || user.IsGuest {
		t.Fatalf("user, err = %+v, %v; want SSO user", user, err)
	}
	if atomic.LoadInt32(hits) != 1 {
		t.Fatalf("gateway hits = %d, want 1", atomic.LoadInt32(hits))
	}
	if got, _ := lastCode.Load().(string); got != "abc123" {
		t.Fatalf("code sent to gateway = %q, want trimmed %q", got, "abc123")
	}
}

func TestExchangeCodeBlankCodeNeverCallsGateway(t *testing.T) {
	srv, hits, _ := countingSSOServer(t, guardOKBody)
	c := NewClient(Config{PaaSID: "paas", AppToken: "token", AuthHost: srv.URL, AuthURI: "/sso"})
	c.SetHTTPClient(srv.Client())

	user, err := c.ExchangeCode(context.Background(), " \t\n ")
	assertGuest(t, user, err)
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Fatalf("blank code must not reach the gateway, hits = %d", n)
	}
}

func TestExchangeCodeUnconfiguredGatewayShortCircuits(t *testing.T) {
	srv, hits, _ := countingSSOServer(t, guardOKBody)
	cases := []struct {
		name string
		cfg  Config
	}{
		{"missing paas id", Config{AppToken: "token", AuthHost: srv.URL, AuthURI: "/sso"}},
		{"missing app token", Config{PaaSID: "paas", AuthHost: srv.URL, AuthURI: "/sso"}},
		{"missing auth uri", Config{PaaSID: "paas", AppToken: "token", AuthHost: srv.URL}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := atomic.LoadInt32(hits)
			c := NewClient(tc.cfg)
			c.SetHTTPClient(srv.Client())
			user, err := c.ExchangeCode(context.Background(), "code")
			assertGuest(t, user, err)
			if after := atomic.LoadInt32(hits); after != before {
				t.Fatalf("unconfigured gateway must not send any request, hits %d -> %d", before, after)
			}
		})
	}
}

func TestExchangeCodeMapsProfileWithCurrentAuthTime(t *testing.T) {
	srv, _, _ := countingSSOServer(t, guardOKBody)
	c := NewClient(Config{PaaSID: "paas", AppToken: "token", AuthHost: srv.URL, AuthURI: "/sso"})
	c.SetHTTPClient(srv.Client())

	before := time.Now().UTC()
	user, err := c.ExchangeCode(context.Background(), "code")
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	want := User{StaffID: 42, LoginName: "carol", ChineseName: "carol", DeptID: 9, DeptName: "基础架构"}
	got := *user
	authTime := got.AuthTime
	got.AuthTime = time.Time{}
	if got != want {
		t.Fatalf("user = %+v, want %+v", got, want)
	}
	if authTime.IsZero() || authTime.Before(before) || authTime.After(after) {
		t.Fatalf("AuthTime = %v, want within [%v, %v]", authTime, before, after)
	}
	if authTime.Location() != time.UTC {
		t.Fatalf("AuthTime location = %v, want UTC", authTime.Location())
	}
}

func TestExchangeCodeRejectsFailureRetEvenWithData(t *testing.T) {
	body := `{"Ret":1,"ErrCode":40001,"ErrMsg":"expired","Data":{"StaffId":7,"LoginName":"mallory"}}`
	srv, _, _ := countingSSOServer(t, body)
	c := NewClient(Config{PaaSID: "paas", AppToken: "token", AuthHost: srv.URL, AuthURI: "/sso"})
	c.SetHTTPClient(srv.Client())

	data, err := c.fetchSSOProfile(context.Background(), "code")
	if err == nil || data != nil || !strings.HasPrefix(err.Error(), "SSO returned failure: Ret=1 ErrCode=40001 ErrMsg=expired") {
		t.Fatalf("fetchSSOProfile = %+v, %v; want Ret failure", data, err)
	}
	user, err := c.ExchangeCode(context.Background(), "code")
	assertGuest(t, user, err)
}

func TestExchangeCodeGatewayErrorsDegradeToGuest(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"http status", http.StatusInternalServerError, guardOKBody},
		{"bad json", http.StatusOK, `{"Ret":0,"Data":`},
		{"nil data", http.StatusOK, `{"Ret":0}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := NewClient(Config{PaaSID: "paas", AppToken: "token", AuthHost: srv.URL, AuthURI: "/sso"})
			c.SetHTTPClient(srv.Client())
			user, err := c.ExchangeCode(context.Background(), "code")
			assertGuest(t, user, err)
		})
	}

	t.Run("unreachable gateway", func(t *testing.T) {
		c := NewClient(Config{PaaSID: "paas", AppToken: "token", AuthHost: "http://127.0.0.1:1", AuthURI: "/sso"})
		user, err := c.ExchangeCode(context.Background(), "code")
		assertGuest(t, user, err)
	})
}

// 响应体超过 1MB 时只读前 1MB：被截断的 JSON 解析失败并降级为访客；1MB 以内的完整响应正常登录。
func TestExchangeCodeResponseBodyLimit(t *testing.T) {
	const limit = 1 << 20
	build := func(total int) string {
		head := `{"Ret":0,"ErrMsg":"`
		tail := `","Data":{"StaffId":5,"LoginName":"dave"}}`
		return head + strings.Repeat("x", total-len(head)-len(tail)) + tail
	}
	cases := []struct {
		name      string
		size      int
		wantGuest bool
	}{
		{"exactly at limit", limit, false},
		{"over limit", limit + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := build(tc.size)
			if len(body) != tc.size {
				t.Fatalf("body size = %d, want %d", len(body), tc.size)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			c := NewClient(Config{PaaSID: "paas", AppToken: "token", AuthHost: srv.URL, AuthURI: "/sso"})
			c.SetHTTPClient(srv.Client())

			data, ferr := c.fetchSSOProfile(context.Background(), "code")
			user, err := c.ExchangeCode(context.Background(), "code")
			if tc.wantGuest {
				if ferr == nil || data != nil || !strings.HasPrefix(ferr.Error(), "decode auth response JSON failed: ") {
					t.Fatalf("fetchSSOProfile = %+v, %v; want truncated JSON error", data, ferr)
				}
				assertGuest(t, user, err)
				return
			}
			if ferr != nil || data == nil || data.StaffId != 5 {
				t.Fatalf("fetchSSOProfile = %+v, %v; want profile", data, ferr)
			}
			if err != nil || user.IsGuest || user.LoginName != "dave" {
				t.Fatalf("user, err = %+v, %v; want dave", user, err)
			}
		})
	}
}
