package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/auth"
)

// gatewayCurrent asks /api/auth/current as a caller behind a loopback proxy.
func gatewayCurrent(t *testing.T, handler http.Handler, headers map[string]string) (auth.User, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth/current status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		User auth.User `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.User, rec.Header().Get("Set-Cookie")
}

func TestGatewayIdentityIgnoresForwardedClientHeaders(t *testing.T) {
	t.Setenv("AGENTFLOW_TRUST_IDENTITY_HEADERS", "true")
	t.Setenv("AGENTFLOW_IDENTITY_HEADER_SECRET", "")
	handler, st := newTestAuthServer(t, auth.NewClient(auth.Config{SessionSecret: "test-secret-123"}))
	for name, forwarded := range map[string]map[string]string{
		"x-forwarded-for": {"X-Forwarded-For": "198.51.100.10"},
		"forwarded":       {"Forwarded": "for=198.51.100.10"},
		"x-real-ip":       {"X-Real-IP": "198.51.100.10"},
	} {
		t.Run(name, func(t *testing.T) {
			headers := map[string]string{"Staffname": "real-admin", "x-staff-name": "real-admin"}
			for key, value := range forwarded {
				headers[key] = value
			}
			user, cookie := gatewayCurrent(t, handler, headers)
			if !user.IsGuest || user.LoginName == "real-admin" || strings.Contains(cookie, authCookieName+"=") {
				t.Fatalf("forwarded client minted a gateway session: user=%+v cookie=%q", user, cookie)
			}
		})
	}
	if _, ok := st.UserByLoginName("real-admin"); ok {
		t.Fatal("forged gateway identity was provisioned")
	}
	// A gateway that connects directly without forwarding headers still works.
	if user, cookie := gatewayCurrent(t, handler, map[string]string{"Staffname": "direct-gateway"}); user.IsGuest || user.LoginName != "direct-gateway" || !strings.Contains(cookie, authCookieName+"=") {
		t.Fatalf("direct loopback gateway rejected: user=%+v", user)
	}
}

func TestGatewayIdentityRequiresConfiguredSecret(t *testing.T) {
	t.Setenv("AGENTFLOW_TRUST_IDENTITY_HEADERS", "true")
	t.Setenv("AGENTFLOW_IDENTITY_HEADER_SECRET", "gateway-shared-secret")
	handler, _ := newTestAuthServer(t, auth.NewClient(auth.Config{SessionSecret: "test-secret-123"}))
	for name, secret := range map[string]string{"missing": "", "wrong": "gateway-shared-secreT", "prefix": "gateway-shared"} {
		t.Run(name, func(t *testing.T) {
			headers := map[string]string{"Staffname": "real-admin"}
			if secret != "" {
				headers[gatewayIdentitySecretHeader] = secret
			}
			if user, cookie := gatewayCurrent(t, handler, headers); !user.IsGuest || strings.Contains(cookie, authCookieName+"=") {
				t.Fatalf("gateway identity accepted without the secret: %+v", user)
			}
		})
	}
	// The authenticating gateway sits behind other proxies and forwards the
	// client address; the secret, not the absence of forwarding, proves it.
	user, cookie := gatewayCurrent(t, handler, map[string]string{
		"Staffname": "real-admin", gatewayIdentitySecretHeader: "gateway-shared-secret", "X-Forwarded-For": "198.51.100.10",
	})
	if user.IsGuest || user.LoginName != "real-admin" || !strings.Contains(cookie, authCookieName+"=") {
		t.Fatalf("gateway with the secret rejected: %+v", user)
	}
}
