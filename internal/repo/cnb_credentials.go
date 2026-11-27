package repo

import (
	"errors"
	"strings"
	"time"
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
