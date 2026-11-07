package httpapi

import (
	"agentflow/internal/auth"
	"context"
	"net/http"
)

// API-token administration requires a browser session. The actor cached by
// userAccessAuth may originate from a Bearer token, so resolve the cookie again
// instead of accepting that cached identity as interactive authentication.
func (d deps) interactiveUser(r *http.Request) (*auth.User, bool) {
	ctx := context.WithValue(r.Context(), requestUserContextKey{}, (*auth.User)(nil))
	return d.requestUser(r.WithContext(ctx), false)
}
