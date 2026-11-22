package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// GitHub sign-in uses the OAuth web flow, so it needs its own OAuth App with a
// client secret. The Device Flow app used for repository credentials has no
// secret and is unrelated.
const (
	githubLoginCookieName = "agentflow_github_login"
	githubLoginCookiePath = "/api/auth/github/login"
	githubLoginStateTTL   = 10 * time.Minute
)

var (
	githubLoginAuthorizeURL = "https://github.com/login/oauth/authorize"
	githubLoginTokenURL     = "https://github.com/login/oauth/access_token"
	githubLoginUserURL      = "https://api.github.com/user"
	githubLoginHTTPClient   = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
)

type githubLoginConfig struct {
	ClientID, ClientSecret, RedirectURL string
}

func githubLoginConfigFromEnv() githubLoginConfig {
	return githubLoginConfig{
		ClientID:     strings.TrimSpace(os.Getenv("AGENTFLOW_GITHUB_LOGIN_CLIENT_ID")),
		ClientSecret: strings.TrimSpace(os.Getenv("AGENTFLOW_GITHUB_LOGIN_CLIENT_SECRET")),
		RedirectURL:  strings.TrimSpace(os.Getenv("AGENTFLOW_GITHUB_LOGIN_REDIRECT_URL")),
	}
}

func (c githubLoginConfig) Enabled() bool { return c.ClientID != "" && c.ClientSecret != "" }

// redirectURL must match the OAuth App's callback; GitHub rejects any other
// host, so deriving it from the request cannot send the code elsewhere.
func (c githubLoginConfig) redirectURL(r *http.Request) string {
	if c.RedirectURL != "" {
		return c.RedirectURL
	}
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + githubLoginCookiePath + "/callback"
}

type githubLoginState struct {
	Exp   int64  `json:"exp"`
	Nonce string `json:"n"`
}

func githubLoginStateMAC(encoded, secret string) string {
	mac := hmac.New(sha256.New, []byte("github-login:"+secret))
	mac.Write([]byte(encoded))
	return hex.EncodeToString(mac.Sum(nil))
}

func signGitHubLoginState(state githubLoginState, secret string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", errNoOAuthStateSecret
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + githubLoginStateMAC(encoded, secret), nil
}

func parseGitHubLoginState(raw, secret string) (githubLoginState, error) {
	encoded, sig, ok := strings.Cut(raw, ".")
	if strings.TrimSpace(secret) == "" || !ok || encoded == "" || !hmac.Equal([]byte(sig), []byte(githubLoginStateMAC(encoded, secret))) {
		return githubLoginState{}, os.ErrInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return githubLoginState{}, err
	}
	var state githubLoginState
	err = json.Unmarshal(body, &state)
	return state, err
}

func githubLoginEnabled() bool { return githubLoginConfigFromEnv().Enabled() }

// githubLoginFail sends the browser back to the login page, which shows the
// message for the given reason.
func githubLoginFail(c *gin.Context, reason string) {
	c.Redirect(http.StatusFound, "/login.html?github_error="+url.QueryEscape(reason))
}

func (h *authHandlers) setGitHubLoginCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     githubLoginCookieName,
		Value:    value,
		Path:     githubLoginCookiePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(c.Request),
		MaxAge:   maxAge,
	})
}

func (h *authHandlers) githubLoginStart(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	cfg := githubLoginConfigFromEnv()
	if !cfg.Enabled() {
		githubLoginFail(c, "unavailable")
		return
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		githubLoginFail(c, "failed")
		return
	}
	state := githubLoginState{Exp: time.Now().Add(githubLoginStateTTL).Unix(), Nonce: hex.EncodeToString(nonce)}
	signed, err := signGitHubLoginState(state, h.getAuthClient().Config().SessionSecret)
	if err != nil {
		githubLoginFail(c, "failed")
		return
	}
	// The nonce cookie ties the callback to the browser that started it, so a
	// third party cannot sign a victim into the third party's account.
	h.setGitHubLoginCookie(c, state.Nonce, int(githubLoginStateTTL.Seconds()))
	query := url.Values{
		"client_id":    {cfg.ClientID},
		"redirect_uri": {cfg.redirectURL(c.Request)},
		"scope":        {"read:user"},
		"state":        {signed},
		"allow_signup": {"true"},
	}
	c.Redirect(http.StatusFound, githubLoginAuthorizeURL+"?"+query.Encode())
}

type githubLoginAccount struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

func (h *authHandlers) githubLoginCallback(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	cfg := githubLoginConfigFromEnv()
	if !cfg.Enabled() {
		githubLoginFail(c, "unavailable")
		return
	}
	cookie, cookieErr := c.Request.Cookie(githubLoginCookieName)
	h.setGitHubLoginCookie(c, "", -1)
	if strings.TrimSpace(c.Query("error")) != "" {
		githubLoginFail(c, "denied")
		return
	}
	state, err := parseGitHubLoginState(c.Query("state"), h.getAuthClient().Config().SessionSecret)
	if err != nil || time.Now().Unix() > state.Exp || cookieErr != nil || state.Nonce == "" ||
		!hmac.Equal([]byte(cookie.Value), []byte(state.Nonce)) {
		githubLoginFail(c, "expired")
		return
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		githubLoginFail(c, "expired")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	account, err := fetchGitHubLoginAccount(ctx, cfg, code, cfg.redirectURL(c.Request))
	if err != nil {
		log.Printf("[auth] github sign-in: %v", err)
		githubLoginFail(c, "failed")
		return
	}
	u, err := h.githubLoginUser(account)
	if err != nil {
		var reason githubLoginRefusal
		if errors.As(err, &reason) {
			githubLoginFail(c, string(reason))
			return
		}
		log.Printf("[auth] github sign-in for %d: %v", account.ID, err)
		githubLoginFail(c, "failed")
		return
	}
	token, err := h.getAuthClient().SignUser(domainUserToAuth(u))
	if err != nil {
		githubLoginFail(c, "failed")
		return
	}
	h.setAuthCookie(c.Writer, c.Request, token)
	c.Redirect(http.StatusFound, "/")
}

type githubLoginRefusal string

func (r githubLoginRefusal) Error() string { return string(r) }

// githubLoginUser returns the user linked to the GitHub account, registering
// one when self sign-up allows it.
func (h *authHandlers) githubLoginUser(account githubLoginAccount) (domain.User, error) {
	if u, ok := h.repo.UserByGitHubID(account.ID); ok {
		if err := h.repo.TouchGitHubLogin(account.ID, account.Login); err != nil {
			log.Printf("[auth] github sign-in: record login: %v", err)
		}
		return u, nil
	}
	registerMu.Lock()
	defer registerMu.Unlock()
	if u, ok := h.repo.UserByGitHubID(account.ID); ok {
		return u, nil
	}
	// The first account becomes the administrator and must be created with a
	// password on the setup page.
	if h.repo.LocalAccountCount() == 0 {
		return domain.User{}, githubLoginRefusal("setup")
	}
	if !signupEnabled() {
		return domain.User{}, githubLoginRefusal("signup_closed")
	}
	u, err := h.repo.CreateGitHubAccount(account.ID, account.Login, account.Name, githubLoginCandidates(account))
	if err != nil {
		return domain.User{}, err
	}
	h.joinWelcomeProject(u.ID)
	return u, nil
}

// githubLoginCandidates lists LoopForge login names for a new GitHub user.
// A GitHub login never claims a name granted administration by configuration.
func githubLoginCandidates(account githubLoginAccount) []string {
	login := strings.TrimSpace(account.Login)
	var out []string
	for _, name := range []string{login, "gh-" + login, "gh-" + login + "-" + strconv.FormatInt(account.ID, 10)} {
		if loginPattern.MatchString(name) && !repo.IsAllowlistedPlatformAdmin(name) {
			out = append(out, name)
		}
	}
	return out
}

func fetchGitHubLoginAccount(ctx context.Context, cfg githubLoginConfig, code, redirectURL string) (githubLoginAccount, error) {
	form := url.Values{"client_id": {cfg.ClientID}, "client_secret": {cfg.ClientSecret}, "code": {code}, "redirect_uri": {redirectURL}}
	var grant struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := githubLoginRequest(ctx, http.MethodPost, githubLoginTokenURL, "", strings.NewReader(form.Encode()), &grant); err != nil {
		return githubLoginAccount{}, err
	}
	if grant.AccessToken == "" {
		return githubLoginAccount{}, errors.New("token exchange refused: " + grant.Error)
	}
	var account githubLoginAccount
	if err := githubLoginRequest(ctx, http.MethodGet, githubLoginUserURL, grant.AccessToken, nil, &account); err != nil {
		return githubLoginAccount{}, err
	}
	if account.ID <= 0 || account.Login == "" {
		return githubLoginAccount{}, errors.New("github returned no account")
	}
	return account, nil
}

func githubLoginRequest(ctx context.Context, method, endpoint, token string, body io.Reader, output any) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "LoopForge")
	if body != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := githubLoginHTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New(endpoint + ": HTTP " + strconv.Itoa(response.StatusCode))
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
}
