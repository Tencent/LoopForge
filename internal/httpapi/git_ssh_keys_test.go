package httpapi

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"golang.org/x/crypto/ssh"
)

func testSSHPrivateKey(t *testing.T) string {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

func TestGitSSHKeysSaveValidation(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	endpoint := fmt.Sprintf("/api/users/%d/git-ssh-keys", f.user.ID)
	privateKey := testSSHPrivateKey(t)

	if resp := cvBRequest(t, f, http.MethodPut, fmt.Sprintf("/api/users/%d/git-ssh-keys", f.user.ID+1), map[string]string{"host": "gitlab.com", "name": "x", "privateKey": privateKey}, true); resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "只能管理当前登录用户的 Git SSH 密钥") {
		t.Fatalf("another account's key PUT = HTTP %d %s", resp.Code, resp.Body.String())
	}

	cases := []struct {
		name  string
		input map[string]string
		code  int
		error string
	}{
		{"invalid host", map[string]string{"host": "https://gitlab.com/team/app", "name": "GitLab", "privateKey": privateKey}, http.StatusBadRequest, "请输入 SSH 主机名"},
		{"blank name", map[string]string{"host": "gitlab.com", "name": "  ", "privateKey": privateKey}, http.StatusBadRequest, "请输入 1 到 80 个字符的密钥名称"},
		{"long name", map[string]string{"host": "gitlab.com", "name": strings.Repeat("名", 81), "privateKey": privateKey}, http.StatusBadRequest, "请输入 1 到 80 个字符的密钥名称"},
		{"malformed key", map[string]string{"host": "gitlab.com", "name": "GitLab", "privateKey": "not a key"}, http.StatusBadRequest, ""},
		{"new key without private key", map[string]string{"host": "gitlab.com", "name": "GitLab"}, http.StatusBadRequest, "添加密钥时请粘贴私钥"},
		{"unknown existing id", map[string]string{"id": "missing", "host": "gitlab.com", "name": "GitLab"}, http.StatusNotFound, "此主机的密钥已不存在"},
	}
	for _, tc := range cases {
		resp := cvBRequest(t, f, http.MethodPut, endpoint, tc.input, true)
		if resp.Code != tc.code || !strings.Contains(resp.Body.String(), tc.error) {
			t.Fatalf("%s: HTTP %d %s", tc.name, resp.Code, resp.Body.String())
		}
	}

	resp := cvBRequest(t, f, http.MethodPut, endpoint, map[string]string{"host": "gitlab.com", "name": " GitLab ", "privateKey": privateKey}, true)
	var keys []domain.GitSSHKey
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &keys) != nil || len(keys) != 1 || keys[0].Name != "GitLab" || keys[0].Host != "gitlab.com:22" {
		t.Fatalf("save key: HTTP %d %s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "PRIVATE KEY") || resp.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("key listing leaked secrets or is cacheable: %s", resp.Body.String())
	}
	if resp := cvBRequest(t, f, http.MethodPut, endpoint, map[string]string{"host": "gitlab.com:22", "name": "Duplicate", "privateKey": privateKey}, true); resp.Code != http.StatusConflict {
		t.Fatalf("duplicate host key: HTTP %d %s", resp.Code, resp.Body.String())
	}
	resp = cvBRequest(t, f, http.MethodPut, endpoint, map[string]string{"id": keys[0].ID, "host": "gitlab.com", "name": "Renamed"}, true)
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &keys) != nil || len(keys) != 1 || keys[0].Name != "Renamed" {
		t.Fatalf("rename key: HTTP %d %s", resp.Code, resp.Body.String())
	}
	resp = cvBRequest(t, f, http.MethodDelete, endpoint+"/"+keys[0].ID, nil, true)
	if resp.Code != http.StatusOK || strings.TrimSpace(resp.Body.String()) != "[]" {
		t.Fatalf("delete key: HTTP %d %s", resp.Code, resp.Body.String())
	}
}
