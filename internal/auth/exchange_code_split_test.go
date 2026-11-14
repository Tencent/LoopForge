package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newSSOProfileClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient(Config{PaaSID: "paas", AppToken: "token", AuthHost: srv.URL, AuthURI: "/sso"})
	c.SetHTTPClient(srv.Client())
	return c
}

func TestFetchSSOProfileReturnsData(t *testing.T) {
	c := newSSOProfileClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("code"); got != "a b" {
			t.Errorf("code = %q, want %q", got, "a b")
		}
		_, _ = w.Write([]byte(`{"Ret":0,"Data":{"StaffId":7,"LoginName":"alice"}}`))
	})
	data, err := c.fetchSSOProfile(context.Background(), "a b")
	if err != nil {
		t.Fatalf("fetchSSOProfile: %v", err)
	}
	if data.StaffId != 7 || data.LoginName != "alice" {
		t.Fatalf("data = %+v", data)
	}
}

func TestFetchSSOProfileErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"status", http.StatusBadGateway, "oops", "unexpected status code 502 from SSO, body: oops"},
		{"json", http.StatusOK, "not-json", "decode auth response JSON failed: "},
		{"ret", http.StatusOK, `{"Ret":1,"ErrCode":40001,"ErrMsg":"expired"}`, "SSO returned failure: Ret=1 ErrCode=40001 ErrMsg=expired"},
		{"nil data", http.StatusOK, `{"Ret":0}`, "SSO returned failure: Ret=0 ErrCode=0 ErrMsg="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newSSOProfileClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			data, err := c.fetchSSOProfile(context.Background(), "code")
			if err == nil || data != nil {
				t.Fatalf("data, err = %+v, %v; want nil, error", data, err)
			}
			if !strings.HasPrefix(err.Error(), tc.wantErr) {
				t.Fatalf("err = %q, want prefix %q", err, tc.wantErr)
			}
		})
	}
}

func TestFetchSSOProfileRequestErrors(t *testing.T) {
	bad := NewClient(Config{PaaSID: "paas", AppToken: "token", AuthHost: "://bad", AuthURI: "/sso"})
	if _, err := bad.fetchSSOProfile(context.Background(), "code"); err == nil || !strings.HasPrefix(err.Error(), "create request failed: ") {
		t.Fatalf("err = %v, want create request failed", err)
	}

	down := NewClient(Config{PaaSID: "paas", AppToken: "token", AuthHost: "http://127.0.0.1:1", AuthURI: "/sso"})
	if _, err := down.fetchSSOProfile(context.Background(), "code"); err == nil || !strings.HasPrefix(err.Error(), "request SSO endpoint failed: ") {
		t.Fatalf("err = %v, want request SSO endpoint failed", err)
	}
}

func TestUserFromSSOProfile(t *testing.T) {
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	got := userFromSSOProfile(&IOAAuthData{
		StaffId:     12,
		LoginName:   " alice ",
		ChineseName: " 爱丽丝 ",
		DeptId:      34,
		DeptName:    " 平台部 ",
	}, at)
	want := User{StaffID: 12, LoginName: "alice", ChineseName: "爱丽丝", DeptID: 34, DeptName: "平台部", AuthTime: at}
	if *got != want {
		t.Fatalf("user = %+v, want %+v", *got, want)
	}

	fallback := userFromSSOProfile(&IOAAuthData{LoginName: " bob ", ChineseName: "  "}, at)
	if fallback.ChineseName != "bob" || fallback.LoginName != "bob" || fallback.IsGuest {
		t.Fatalf("fallback user = %+v", fallback)
	}
}
