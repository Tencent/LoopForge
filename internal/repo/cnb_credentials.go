package repo

import "agentflow/internal/gitwork"

// GitHTTPSCredentials returns the user's host-scoped Git credentials, empty
// when the user has none configured.
func (r *UsersRepo) GitHTTPSCredentials(userID int64) []gitwork.HTTPSCredential {
	user, ok := r.User(userID)
	if !ok || user.CNBToken == "" {
		return nil
	}
	return []gitwork.HTTPSCredential{gitwork.CNBCredential(user.CNBToken)}
}
