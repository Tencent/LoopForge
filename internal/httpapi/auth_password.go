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

var passwordAttempts = struct {
	sync.Mutex
	entries map[string]loginAttempts
}{entries: map[string]loginAttempts{}}

func allowPasswordAttempt(ip string) bool {
	passwordAttempts.Lock()
	defer passwordAttempts.Unlock()
	now := time.Now()
	for k, v := range passwordAttempts.entries {
		if now.After(v.until) {
			delete(passwordAttempts.entries, k)
		}
	}
	v := passwordAttempts.entries[ip]
	if v.count >= 10 || (v.count == 0 && len(passwordAttempts.entries) >= 4096) {
		return false
	}
	if v.count == 0 {
		v.until = now.Add(time.Minute)
	}
	v.count++
	passwordAttempts.entries[ip] = v
	return true
}

func (h *authHandlers) authPassword(c *gin.Context) {
	// Require a same-origin browser request; proxy headers cannot grant identity.
	origin, err := url.Parse(c.GetHeader("Origin"))
	if err != nil || origin.Host != c.Request.Host || (origin.Scheme != "http" && origin.Scheme != "https") {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid request origin"})
		return
	}
	if !allowPasswordAttempt(c.RemoteIP()) {
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
