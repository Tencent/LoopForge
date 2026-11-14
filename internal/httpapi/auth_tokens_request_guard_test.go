package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"

	"github.com/gin-gonic/gin"
)

func tokenGuardDirect(h *tokenHandlers, user *auth.User, method, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, "/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, user))
	h.authTokens(c)
	return rec
}

// createAPIToken must answer with normalizeAPITokenRequest's exact reason, so
// the split keeps the user-facing messages unchanged.
func TestAuthTokensCreateRepliesWithValidationReason(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, tc := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"name": "   "}, "Token 名称不能为空"},
		{map[string]any{"name": strings.Repeat("令", 129)}, "Token 名称不能超过 128 个字符"},
		{map[string]any{"name": "ci", "expiresInDays": -1}, "有效期必须在 1 到 365 天之间"},
		{map[string]any{"name": "ci", "expiresInDays": 366}, "有效期必须在 1 到 365 天之间"},
	} {
		rec := cvBRequest(t, f, http.MethodPost, "/api/auth/tokens", tc.body, true)
		var got map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != http.StatusBadRequest || got["error"] != tc.want {
			t.Errorf("create %v = %d %q, want 400 %q", tc.body, rec.Code, got["error"], tc.want)
		}
	}
	if tokens := f.s.APITokens(f.user.ID); len(tokens) != 0 {
		t.Fatalf("rejected requests stored tokens: %+v", tokens)
	}
}

// The 128-rune limit applies to the trimmed name, and the created response
// carries the one-time plaintext whose prefix and hash match what is stored.
func TestAuthTokensCreateTrimsBeforeLengthCheckAndReturnsPlaintextOnce(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	name := strings.Repeat("令", 128)
	before := time.Now().UTC()
	rec := cvBRequest(t, f, http.MethodPost, "/api/auth/tokens", map[string]any{"name": "  " + name + "  ", "expiresInDays": 2}, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("padded 128-rune name = %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Token    string `json:"token"`
		Metadata struct {
			ID        string     `json:"id"`
			UserID    int64      `json:"userId"`
			Name      string     `json:"name"`
			Prefix    string     `json:"prefix"`
			TokenHash string     `json:"tokenHash"`
			ExpiresAt *time.Time `json:"expiresAt"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "afp_") || created.Metadata.Prefix != tokenPrefix(created.Token) {
		t.Fatalf("token %q / prefix %q", created.Token, created.Metadata.Prefix)
	}
	if created.Metadata.Name != name || created.Metadata.UserID != f.user.ID || created.Metadata.TokenHash != "" {
		t.Fatalf("metadata = %+v", created.Metadata)
	}
	if created.Metadata.ExpiresAt == nil || created.Metadata.ExpiresAt.Before(before.Add(48*time.Hour)) ||
		created.Metadata.ExpiresAt.After(time.Now().UTC().Add(48*time.Hour)) {
		t.Fatalf("expiresAt = %v, want about 2 days after %v", created.Metadata.ExpiresAt, before)
	}
	sum := sha256.Sum256([]byte(created.Token))
	stored, ok := f.s.ActiveAPITokenByHash(hex.EncodeToString(sum[:]), time.Now().UTC())
	if !ok || stored.ID != created.Metadata.ID || stored.UserID != f.user.ID {
		t.Fatalf("returned plaintext does not resolve to the stored token: %+v ok=%v", stored, ok)
	}
}

// A session user without a stored row is saved first, and the token it then
// creates is keyed by that new ID; when saving the user fails the request
// stops with the original 500 instead of listing or creating under ID 0.
func TestAuthTokensZeroIDOwnerIsPersistedOrRejected(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &tokenHandlers{deps: &deps{repo: f.s}}
	fresh := &auth.User{LoginName: "token-guard-fresh", ChineseName: "Fresh"}
	rec := tokenGuardDirect(h, fresh, http.MethodPost, `{"name":"first"}`)
	if rec.Code != http.StatusCreated || fresh.ID <= 0 {
		t.Fatalf("zero-id create = %d user=%+v body=%s", rec.Code, fresh, rec.Body.String())
	}
	stored, ok := f.s.UserByLoginName("token-guard-fresh")
	if !ok || stored.ID != fresh.ID {
		t.Fatalf("stored user = %+v ok=%v, session user ID %d", stored, ok, fresh.ID)
	}
	if tokens := f.s.APITokens(stored.ID); len(tokens) != 1 || tokens[0].Name != "first" {
		t.Fatalf("tokens of new user = %+v", tokens)
	}

	closed := cvBNewHTTPFixture(t)
	closed.s.Close()
	h = &tokenHandlers{deps: &deps{repo: closed.s}}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		user := &auth.User{LoginName: "token-guard-closed", ChineseName: "Closed"}
		rec = tokenGuardDirect(h, user, method, `{"name":"x"}`)
		var got map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != http.StatusInternalServerError || got["error"] != "保存登录用户失败" {
			t.Fatalf("%s with unsaved user on closed store = %d %s", method, rec.Code, rec.Body.String())
		}
	}
}

// A store failure after validation is still reported, not answered with 201.
func TestAuthTokensCreateReportsStoreFailure(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	f.s.Close()
	h := &tokenHandlers{deps: &deps{repo: f.s}}
	rec := tokenGuardDirect(h, domainUserToAuth(f.user), http.MethodPost, `{"name":"ci","expiresInDays":1}`)
	var got map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusInternalServerError || got["error"] != "保存 Token 失败" {
		t.Fatalf("closed store create = %d %s", rec.Code, rec.Body.String())
	}
}
