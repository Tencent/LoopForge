package httpapi

import "net/http"

// canAdmin denotes management authority, not authentication success.
// Development mode intentionally grants authority when authentication is disabled.
func businessDomainIdentity(h *deps, r *http.Request) (userID int64, canAdmin bool) {
	user, ok := h.requestUser(r, true)
	if !authRequired() {
		if ok {
			return user.ID, true
		}
		return 0, true
	}
	if !ok {
		return 0, false
	}
	return user.ID, h.isPlatformAdmin(r)
}
