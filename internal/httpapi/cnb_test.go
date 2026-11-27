package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

const testCNBToken = "cnb-fake-token-0000"

func setCNBToken(s *repo.Store, userID int64, token string) error {
	_, err := s.UpdateUserCredentials(userID, repo.UserCredentialUpdate{CNBToken: token})
	return err
}

func TestCNBTokenSaveClearThroughCredentialsAndPublicUserNeverLeaks(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	url := fmt.Sprintf("/api/users/%d/credentials", f.user.ID)

	resp := cvBRequest(t, f, http.MethodPut, url, map[string]string{"cnbToken": " " + testCNBToken + " "}, true)
	var saved domain.User
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &saved) != nil || !saved.CNBTokenConfigured || strings.Contains(resp.Body.String(), testCNBToken) {
		t.Fatalf("save response must report configured and hide the token (HTTP %d): %s", resp.Code, resp.Body.String())
	}
	if got := f.s.GitHTTPCredentialSecrets(context.Background(), f.user.ID); len(got) != 1 || got["cnb.cool"].Token != testCNBToken {
		t.Fatalf("stored credentials = %+v", got)
	}
	if list := cvBRequest(t, f, http.MethodGet, "/api/users", nil, true); strings.Contains(list.Body.String(), testCNBToken) {
		t.Fatal("user directory leaks the CNB token")
	}
	// Another credential write must keep the token.
	if resp := cvBRequest(t, f, http.MethodPut, url, map[string]string{"codexBaseUrl": "https://example.org"}, true); resp.Code != http.StatusOK || len(f.s.GitHTTPCredentialSecrets(context.Background(), f.user.ID)) != 1 {
		t.Fatalf("unrelated credential update dropped the CNB token (HTTP %d)", resp.Code)
	}

	resp = cvBRequest(t, f, http.MethodPut, url, map[string]bool{"cnbTokenClear": true}, true)
	if resp.Code != http.StatusOK || strings.Contains(resp.Body.String(), `"cnbTokenConfigured":true`) || len(f.s.GitHTTPCredentialSecrets(context.Background(), f.user.ID)) != 0 {
		t.Fatalf("clear failed (HTTP %d): %s", resp.Code, resp.Body.String())
	}
}

func TestCNBTokenVerifyIsOnlyForTheSignedInUser(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if resp := cvBRequest(t, f, http.MethodGet, fmt.Sprintf("/api/users/%d/cnb-token/verify", f.user.ID), nil, false); resp.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated verify: HTTP %d", resp.Code)
	}
	if resp := cvBRequest(t, f, http.MethodGet, fmt.Sprintf("/api/users/%d/cnb-token/verify", f.user.ID+1), nil, true); resp.Code != http.StatusForbidden {
		t.Fatalf("verifying another user's token: HTTP %d", resp.Code)
	}
}

func TestCNBTokenVerifyMapsStatusToResult(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	status, body := http.StatusOK, `{"username":"alice"}`
	var sawAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(api.Close)
	previous := cnbAPIBase
	cnbAPIBase = api.URL
	t.Cleanup(func() { cnbAPIBase = previous })
	h := &cnbHandlers{deps: &deps{repo: f.s}}
	router := gin.New()
	router.GET("/api/users/:id/cnb-token/verify", h.verifyToken)
	router.POST("/api/users/:id/cnb-token/verify", h.verifyToken)
	verify := func(method string, input any) gitTokenVerifyResult {
		t.Helper()
		var raw []byte
		if input != nil {
			raw, _ = json.Marshal(input)
		}
		req := authedRequest(t, httptest.NewRequest(method, fmt.Sprintf("/api/users/%d/cnb-token/verify", f.user.ID), strings.NewReader(string(raw))), f.user)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var result gitTokenVerifyResult
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &result) != nil {
			t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
		}
		return result
	}

	if got := verify(http.MethodGet, nil); got.Valid || got.Error == "" {
		t.Fatalf("no saved token must be reported, got %+v", got)
	}
	if got := verify(http.MethodPost, map[string]string{"token": testCNBToken}); !got.Valid || got.Login != "alice" || sawAuth != "Bearer "+testCNBToken {
		t.Fatalf("200: %+v auth=%q", got, sawAuth)
	}
	status, body = http.StatusForbidden, `{"errcode":1,"errmsg":"forbidden"}`
	if got := verify(http.MethodPost, map[string]string{"token": testCNBToken}); !got.Valid || got.Login != "" {
		t.Fatalf("403 means a valid token without /user access: %+v", got)
	}
	status = http.StatusUnauthorized
	if got := verify(http.MethodPost, map[string]string{"token": testCNBToken}); got.Valid || got.Error == "" {
		t.Fatalf("401 means an invalid token: %+v", got)
	}
	if err := setCNBToken(f.s, f.user.ID, testCNBToken); err != nil {
		t.Fatal(err)
	}
	status = http.StatusOK
	if got := verify(http.MethodGet, nil); !got.Valid {
		t.Fatalf("saved token should verify: %+v", got)
	}
}
