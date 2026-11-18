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

func verifyAntigravityRefreshIdentity(before antigravityLogin, access string) error {
	identity, err := googleRunnerIdentity(access)
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

// canonicalAntigravityRefresh keeps host account metadata and the refresh token.
// A nil result without an error means the access token did not change.
func canonicalAntigravityRefresh(original, refreshed []byte) ([]byte, error) {
	before, err := parseAntigravityLogin(original)
	if err != nil {
		return nil, err
	}
	after, err := parseAntigravityLogin(refreshed)
	if err != nil {
		return nil, err
	}
	if before.Token.Refresh != after.Token.Refresh {
		return nil, errors.New("Antigravity refresh token changed; host login retained")
	}
	if before.Token.Access == after.Token.Access {
		return nil, nil
	}
	var expiry time.Time
	if json.Unmarshal(after.Token.Expiry, &expiry) != nil || !expiry.After(time.Now()) {
		return nil, errors.New("invalid Antigravity access token expiry; host login retained")
	}
	var tokenType string
	if after.Token.Type != nil && (json.Unmarshal(after.Token.Type, &tokenType) != nil || !strings.EqualFold(tokenType, "Bearer")) {
		return nil, errors.New("invalid Antigravity token type; host login retained")
	}
	if err := verifyAntigravityRefreshIdentity(before, after.Token.Access); err != nil {
		return nil, err
	}

	var host, token map[string]json.RawMessage
	if json.Unmarshal(original, &host) != nil || json.Unmarshal(host["token"], &token) != nil {
		return nil, errors.New("invalid Antigravity token")
	}
	token["access_token"], _ = json.Marshal(after.Token.Access)
	token["expiry"] = after.Token.Expiry
	if after.Token.Type != nil {
		token["token_type"] = json.RawMessage(`"Bearer"`)
	}
	host["token"], _ = json.Marshal(token)
	return json.Marshal(host)
}
