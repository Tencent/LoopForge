package httpapi

import (
	"agentflow/internal/repo"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

var (
	registerMu   sync.Mutex
	loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{1,63}$`)
)

// signupEnabled reports whether visitors may create accounts after the first
// administrator exists. Defaults to on; public deployments should disable it.
func signupEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGENTFLOW_ALLOW_SIGNUP"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// authSetup tells the login page which mode to show. While sign-up is open it
// also lists the projects a new account may pick to join.
func (h *authHandlers) authSetup(c *gin.Context) {
	needsSetup := h.repo != nil && h.repo.LocalAccountCount() == 0
	joinable := []joinableProject{}
	if h.repo != nil && !needsSetup && signupEnabled() {
		joinable = joinableProjectSummaries(h.repo.Projects())
	}
	c.JSON(http.StatusOK, gin.H{"needsSetup": needsSetup, "signupEnabled": needsSetup || signupEnabled(), "joinableProjects": joinable})
}

const welcomeProjectID = "welcome"

// joinWelcomeProject makes the seeded demo project usable by new accounts so
// the onboarding path works before they create a project of their own.
func (h *authHandlers) joinWelcomeProject(userID int64) {
	if _, ok := h.repo.Project(welcomeProjectID); !ok {
		return
	}
	if err := h.repo.AddProjectMember(welcomeProjectID, userID, "member"); err != nil {
		log.Printf("join welcome project: %v", err)
	}
}

// signupProjects validates the projects picked on the sign-up form. Every
// pick must be open to self-service joining.
func (h *authHandlers) signupProjects(ids []string) ([]string, bool) {
	ids = normalizeProjectIDs(ids)
	if len(ids) > maxSignupProjects {
		return nil, false
	}
	for _, id := range ids {
		p, ok := h.repo.Project(id)
		if !ok || !projectSelfJoinable(p) {
			return nil, false
		}
	}
	return ids, true
}

func (h *authHandlers) authRegister(c *gin.Context) {
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
		Login      string   `json:"login"`
		Password   string   `json:"password"`
		ProjectIDs []string `json:"projectIds"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid registration request"})
		return
	}
	input.Login = strings.TrimSpace(input.Login)
	if !loginPattern.MatchString(input.Login) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名需 2–64 位，以字母或数字开头，只能包含字母、数字、_ . -"})
		return
	}
	if len(input.Password) < repo.MinPasswordLength || len(input.Password) > repo.MaxPasswordLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("密码需 %d–%d 个字符", repo.MinPasswordLength, repo.MaxPasswordLength)})
		return
	}
	registerMu.Lock()
	first := h.repo.LocalAccountCount() == 0
	if !first && !signupEnabled() {
		registerMu.Unlock()
		c.JSON(http.StatusForbidden, gin.H{"error": "自助注册已关闭，请联系管理员创建账号"})
		return
	}
	projectIDs, ok := h.signupProjects(input.ProjectIDs)
	if !ok {
		registerMu.Unlock()
		c.JSON(http.StatusBadRequest, gin.H{"error": "所选项目不存在或未开放自助加入，请刷新页面后重新选择"})
		return
	}
	u, err := h.repo.CreateLocalAccount(input.Login, input.Password, first)
	if err == nil {
		if !first {
			h.joinWelcomeProject(u.ID)
		}
		for _, id := range projectIDs {
			if err := h.repo.AddProjectMember(id, u.ID, "member"); err != nil {
				log.Printf("join project %s at sign-up: %v", id, err)
			}
		}
	}
	registerMu.Unlock()
	if errors.Is(err, repo.ErrLoginTaken) {
		c.JSON(http.StatusConflict, gin.H{"error": "该用户名已被使用"})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	user := domainUserToAuth(u)
	token, err := h.getAuthClient().SignUser(user)
	if err != nil {
		respond(c, err)
		return
	}
	h.setAuthCookie(c.Writer, c.Request, token)
	c.JSON(http.StatusOK, gin.H{"user": h.annotateUser(user), "admin": first})
}
