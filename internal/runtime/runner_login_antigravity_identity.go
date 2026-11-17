package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

var antigravityIdentityClient = &http.Client{Timeout: 8 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type antigravityIdentity struct {
	Subject  string `json:"sub"`
	Email    string `json:"email"`
	Verified bool   `json:"email_verified"`
}

func googleRunnerIdentity(access string) (antigravityIdentity, error) {
	var identity antigravityIdentity
	if access == "" {
		return identity, errors.New("Antigravity access token missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := antigravityIdentityClient.Do(req)
	if err != nil {
		return identity, errors.New("Antigravity identity verification unavailable; host login retained")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || resp.StatusCode != http.StatusOK || json.Unmarshal(data, &identity) != nil || identity.Subject == "" {
		return identity, errors.New("cannot verify refreshed Antigravity identity; host login retained")
	}
	return identity, nil
}

func verifyAntigravityRefresh(original, refreshed []byte) error {
	before, err := parseAntigravityLogin(original)
	if err != nil {
		return err
	}
	after, err := parseAntigravityLogin(refreshed)
	if err != nil {
		return err
	}
	if before.Token.Refresh != after.Token.Refresh {
		return errors.New("Antigravity refresh token changed; host login retained")
	}
	if before.Token.Access == after.Token.Access {
		return nil
	}
	identity, err := googleRunnerIdentity(after.Token.Access)
	if err != nil {
		return err
	}
	// Compare server-confirmed identity with trusted host metadata. Metadata
	// in the sandbox is not an identity proof, even when its email matches.
	trusted := antigravityIdentity{Email: before.Email}
	parts := strings.Split(before.IDToken, ".")
	if len(parts) == 3 {
		if data, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims antigravityIdentity
			if json.Unmarshal(data, &claims) == nil {
				trusted.Subject = claims.Subject
				if trusted.Email == "" {
					trusted.Email = claims.Email
				}
			}
		}
	}
	if trusted.Subject != "" {
		if identity.Subject != trusted.Subject {
			return errors.New("Antigravity account changed; host login retained")
		}
		return nil
	}
	if trusted.Email != "" {
		if !identity.Verified || !strings.EqualFold(strings.TrimSpace(identity.Email), strings.TrimSpace(trusted.Email)) {
			return errors.New("Antigravity account changed; host login retained")
		}
		return nil
	}
	baseline, err := googleRunnerIdentity(before.Token.Access)
	if err != nil {
		return err
	}
	if baseline.Subject != identity.Subject {
		return errors.New("Antigravity account changed; host login retained")
	}
	return nil
}

// Keep account metadata and the refresh token from the trusted source. A
// successful userinfo response authorizes only the refreshed access token.
func canonicalAntigravityRefresh(original, refreshed []byte) ([]byte, error) {
	if err := verifyAntigravityRefresh(original, refreshed); err != nil {
		return nil, err
	}
	var before, after map[string]json.RawMessage
	if json.Unmarshal(original, &before) != nil || json.Unmarshal(refreshed, &after) != nil {
		return nil, errors.New("invalid Antigravity login")
	}
	var token, candidate map[string]json.RawMessage
	if json.Unmarshal(before["token"], &token) != nil || json.Unmarshal(after["token"], &candidate) != nil {
		return nil, errors.New("invalid Antigravity token")
	}
	for _, key := range []string{"access_token", "token_type", "expiry"} {
		if value := candidate[key]; len(value) > 0 {
			token[key] = value
		}
	}
	before["token"], _ = json.Marshal(token)
	return json.Marshal(before)
}
