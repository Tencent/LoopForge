package repo

import (
	"errors"
	"strings"
	"time"

	"agentflow/internal/gitwork"
)

// SetCNBToken stores the user's CNB access token; an empty token clears it.
func (r *UsersRepo) SetCNBToken(userID int64, token string) error {
	user, ok := r.User(userID)
	if !ok {
		return errors.New("user not found")
	}
	user.CNBToken = strings.TrimSpace(token)
	user.UpdatedAt = time.Now().UTC()
	return r.writeUserConfig(user)
}

// GitHTTPSCredentials returns the user's host-scoped Git credentials, empty
// when the user has none configured.
func (r *UsersRepo) GitHTTPSCredentials(userID int64) []gitwork.HTTPSCredential {
	user, ok := r.User(userID)
	if !ok || user.CNBToken == "" {
		return nil
	}
	return []gitwork.HTTPSCredential{gitwork.CNBCredential(user.CNBToken)}
}
