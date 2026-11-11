package httpapi

import (
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type loginAttempts struct {
	count int
	until time.Time
}

// Password attempts allowed per client IP per window, and the number of IPs
// tracked at once before new ones are refused.
const (
	passwordAttemptLimit   = 10
	passwordAttemptWindow  = time.Minute
	passwordAttemptSources = 4096
)

// passwordLimiter is the brute-force budget for one API server. Login and
// registration share it: both reveal whether credentials or account names
// exist. Each server owns its own limiter, so separately constructed servers
// (and tests) never share counters.
//
// Contract:
//   - Every login request counts, malformed or not, before any lookup.
//   - A registration request counts once it is well-formed and valid, i.e.
//     when it reaches account creation (including "login taken", which
//     discloses that an account exists). Malformed or invalid sign-up input
//     is rejected for free: it reveals nothing and creates nothing.
type passwordLimiter struct {
	mu      sync.Mutex
	entries map[string]loginAttempts
	now     func() time.Time
}

func newPasswordLimiter() *passwordLimiter {
	return &passwordLimiter{entries: map[string]loginAttempts{}, now: time.Now}
}

func (l *passwordLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, v := range l.entries {
		if now.After(v.until) {
			delete(l.entries, k)
		}
	}
	v := l.entries[ip]
	if v.count >= passwordAttemptLimit || (v.count == 0 && len(l.entries) >= passwordAttemptSources) {
		return false
	}
	if v.count == 0 {
		v.until = now.Add(passwordAttemptWindow)
	}
	v.count++
	l.entries[ip] = v
	return true
}

// passwordLimiterInit guards lazy creation for handlers built without New.
var passwordLimiterInit sync.Mutex

// allowPasswordAttempt spends one attempt from this server's budget.
func (d *deps) allowPasswordAttempt(ip string) bool {
	passwordLimiterInit.Lock()
	if d.passwordAttempts == nil {
		d.passwordAttempts = newPasswordLimiter()
	}
	limiter := d.passwordAttempts
	passwordLimiterInit.Unlock()
	return limiter.allow(ip)
}

func (h *authHandlers) authPassword(c *gin.Context) {
	// Require a same-origin browser request; proxy headers cannot grant identity.
	origin, err := url.Parse(c.GetHeader("Origin"))
	if err != nil || origin.Host != c.Request.Host || (origin.Scheme != "http" && origin.Scheme != "https") {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid request origin"})
		return
	}
	if !h.allowPasswordAttempt(c.RemoteIP()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many attempts. Try again in a minute."})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.Login) > 64 || len(input.Password) > 72 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid login request"})
		return
	}
	u, found := h.repo.UserByLoginName(input.Login)
	valid := h.repo.CheckLocalPassword(u.ID, input.Password)
	if !found || !valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		return
	}
	user := domainUserToAuth(u)
	token, err := h.getAuthClient().SignUser(user)
	if err != nil {
		respond(c, err)
		return
	}
	h.setAuthCookie(c.Writer, c.Request, token)
	c.JSON(http.StatusOK, gin.H{"user": h.annotateUser(user)})
}
