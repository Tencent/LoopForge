package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// decodeGitSSHKeyError requires the response to be exactly one JSON error
// object, so a handler that keeps going after writing an error (and appends
// the key listing) fails here.
func decodeGitSSHKeyError(t *testing.T, resp *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("error response is not a single JSON object: %v: %s", err, resp.Body.String())
	}
	if body["error"] == "" {
		t.Fatalf("error response has no message: %s", resp.Body.String())
	}
	return body["error"]
}

func TestGitSSHKeysSaveErrorResponsesStopTheHandler(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	endpoint := fmt.Sprintf("/api/users/%d/git-ssh-keys", f.user.ID)
	privateKey := testSSHPrivateKey(t)

	cases := []struct {
		name  string
		input map[string]string
		code  int
		error string
	}{
		{"invalid host", map[string]string{"host": "https://gitlab.com/team/app", "name": "GitLab", "privateKey": privateKey}, http.StatusBadRequest, "请输入 SSH 主机名"},
		{"blank name", map[string]string{"host": "gitlab.com", "name": "  ", "privateKey": privateKey}, http.StatusBadRequest, "请输入 1 到 80 个字符的密钥名称"},
		{"malformed key", map[string]string{"host": "gitlab.com", "name": "GitLab", "privateKey": "not a key"}, http.StatusBadRequest, "Git SSH 私钥格式无效"},
		{"new key without private key", map[string]string{"host": "gitlab.com", "name": "GitLab"}, http.StatusBadRequest, "添加密钥时请粘贴私钥"},
		{"unknown existing id", map[string]string{"id": "missing", "host": "gitlab.com", "name": "GitLab"}, http.StatusNotFound, "此主机的密钥已不存在"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := cvBRequest(t, f, http.MethodPut, endpoint, tc.input, true)
			if resp.Code != tc.code {
				t.Fatalf("HTTP %d, want %d: %s", resp.Code, tc.code, resp.Body.String())
			}
			if msg := decodeGitSSHKeyError(t, resp); !strings.Contains(msg, tc.error) {
				t.Fatalf("error = %q, want it to contain %q", msg, tc.error)
			}
		})
	}

	keys, err := f.s.GitSSHKeyDescriptions(f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("rejected saves stored keys: %+v", keys)
	}
}

func TestGitSSHKeysSaveRejectsOversizedBody(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	endpoint := fmt.Sprintf("/api/users/%d/git-ssh-keys", f.user.ID)
	// Larger than the 32 KiB PUT limit but well under the generic 2 MiB decode
	// limit, so only the handler's own MaxBytesReader rejects it.
	oversized := strings.Repeat("A", 40<<10)
	resp := cvBRequest(t, f, http.MethodPut, endpoint, map[string]string{"host": "gitlab.com", "name": "GitLab", "privateKey": oversized}, true)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: HTTP %d %s", resp.Code, resp.Body.String())
	}
	if msg := decodeGitSSHKeyError(t, resp); !strings.Contains(msg, "request body too large") {
		t.Fatalf("oversized body error = %q, want the request body limit", msg)
	}
}

func TestGitSSHKeysRenameWithMalformedKeyKeepsStoredKey(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	endpoint := fmt.Sprintf("/api/users/%d/git-ssh-keys", f.user.ID)
	privateKey := testSSHPrivateKey(t)

	resp := cvBRequest(t, f, http.MethodPut, endpoint, map[string]string{"host": "git.example.org:2222", "name": "Example", "privateKey": privateKey}, true)
	var keys []domain.GitSSHKey
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &keys) != nil || len(keys) != 1 {
		t.Fatalf("save key: HTTP %d %s", resp.Code, resp.Body.String())
	}
	saved := keys[0]

	// An edit that pastes a broken key must be rejected, not silently treated
	// as "keep the stored key" while the rename goes through.
	resp = cvBRequest(t, f, http.MethodPut, endpoint, map[string]string{"id": saved.ID, "host": "git.example.org:2222", "name": "Renamed", "privateKey": "-----BEGIN OPENSSH " + "PRIVATE KEY-----\nbroken\n-----END OPENSSH " + "PRIVATE KEY-----"}, true)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("rename with malformed key: HTTP %d %s", resp.Code, resp.Body.String())
	}
	if msg := decodeGitSSHKeyError(t, resp); !strings.Contains(msg, "Git SSH 私钥格式无效") {
		t.Fatalf("rename with malformed key error = %q", msg)
	}
	keys, err := f.s.GitSSHKeyDescriptions(f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Name != "Example" {
		t.Fatalf("rejected rename changed the stored key: %+v", keys)
	}
	stored := f.s.GitSSHKeysForRemotes(f.user.ID, []string{"ssh://git@git.example.org:2222/team/app.git"})
	if len(stored) != 1 {
		t.Fatalf("stored key for remote = %d entries, want 1", len(stored))
	}
	for _, key := range stored {
		if strings.TrimSpace(key) != strings.TrimSpace(privateKey) {
			t.Fatal("rejected rename replaced the stored private key")
		}
	}
}

func TestGitSSHKeysSaveConflictAndStoreErrorsStopTheHandler(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	endpoint := fmt.Sprintf("/api/users/%d/git-ssh-keys", f.user.ID)
	privateKey := testSSHPrivateKey(t)

	if resp := cvBRequest(t, f, http.MethodPut, endpoint, map[string]string{"host": "gitlab.com", "name": "GitLab", "privateKey": privateKey}, true); resp.Code != http.StatusOK {
		t.Fatalf("save key: HTTP %d %s", resp.Code, resp.Body.String())
	}
	resp := cvBRequest(t, f, http.MethodPut, endpoint, map[string]string{"host": "gitlab.com:22", "name": "Duplicate", "privateKey": privateKey}, true)
	if resp.Code != http.StatusConflict {
		t.Fatalf("duplicate host key: HTTP %d %s", resp.Code, resp.Body.String())
	}
	decodeGitSSHKeyError(t, resp)

	// A store failure that is none of the mapped errors goes through respond
	// and must not be followed by the key listing.
	if err := f.s.DB.Exec("DROP TABLE user_git_ssh_keys").Error; err != nil {
		t.Fatal(err)
	}
	resp = cvBRequest(t, f, http.MethodPut, endpoint, map[string]string{"host": "git.example.org", "name": "Example", "privateKey": privateKey}, true)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("store failure: HTTP %d %s", resp.Code, resp.Body.String())
	}
	if msg := decodeGitSSHKeyError(t, resp); !strings.Contains(msg, "user_git_ssh_keys") {
		t.Fatalf("store failure error = %q, want the store error", msg)
	}
}
