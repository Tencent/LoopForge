package notification

import (
	"slices"
	"strings"
	"testing"
)

func TestWecomCLIEnvWhitelistsHostVariables(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HTTPS_PROXY", "http://proxy:8080")
	t.Setenv("AGENTFLOW_SECRET_SHOULD_NOT_LEAK", "secret")
	env := wecomCLIEnv("tok", "/tmp/cfg")
	for _, want := range []string{"PATH=/usr/bin", "HTTPS_PROXY=http://proxy:8080", "WECOM_CLI_ACCESS_TOKEN=tok", "WECOM_CLI_CONFIG_DIR=/tmp/cfg"} {
		if !slices.Contains(env, want) {
			t.Fatalf("env missing %q: %v", want, env)
		}
	}
	for _, item := range env {
		if strings.HasPrefix(item, "AGENTFLOW_SECRET_SHOULD_NOT_LEAK=") {
			t.Fatalf("non-whitelisted variable leaked: %v", env)
		}
	}
	if n := len(env); env[n-2] != "WECOM_CLI_ACCESS_TOKEN=tok" || env[n-1] != "WECOM_CLI_CONFIG_DIR=/tmp/cfg" {
		t.Fatalf("token/config dir must be appended last: %v", env)
	}
}

func TestWecomResultCode(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		code    int
		wantErr bool
	}{
		{name: "success", raw: `{"errcode":0,"success":true}`, code: 0},
		{name: "top level errcode", raw: `{"errcode":850003}`, code: 850003},
		{name: "nested error wins", raw: `{"errcode":1,"error":{"code":853004}}`, code: 853004},
		{name: "nested zero overrides top level", raw: `{"errcode":7,"error":{"code":0}}`, code: 0},
		{name: "malformed", raw: `not json`, wantErr: true},
		{name: "empty", raw: ``, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, err := wecomResultCode([]byte(tc.raw))
			if tc.wantErr {
				if err == nil || err.Error() != "企业微信 CLI 响应格式异常" {
					t.Fatalf("want format error, got code=%d err=%v", code, err)
				}
				return
			}
			if err != nil || code != tc.code {
				t.Fatalf("code=%d err=%v, want %d", code, err, tc.code)
			}
		})
	}
}
