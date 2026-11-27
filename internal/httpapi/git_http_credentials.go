package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"agentflow/internal/domain"
	"agentflow/internal/gitegress"
	"agentflow/internal/gitwork"
	"agentflow/internal/gongfeng"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// gitHTTPVerifyClient reaches user-configured hosts; tests replace it.
var gitHTTPVerifyClient = gitegress.Client

type gitHTTPCredentialInput struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

func (h *usersHandlers) gitHTTPCredentials(c *gin.Context) {
	id, ok := h.gitHTTPCredentialOwner(c)
	if !ok {
		return
	}
	switch c.Request.Method {
	case http.MethodPut:
		var input gitHTTPCredentialInput
		if !decodeLimit(c, &input, 16<<10) {
			return
		}
		cred, err := h.gitHTTPCredentialFromInput(c.Request.Context(), id, input)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := h.repo.SaveGitHTTPCredential(id, input.ID, cred); err != nil {
			respondGitHTTPCredentialError(c, err)
			return
		}
	case http.MethodDelete:
		if err := h.repo.DeleteGitHTTPCredential(id, c.Param("credID")); err != nil {
			respondGitHTTPCredentialError(c, err)
			return
		}
	}
	creds, err := h.repo.GitHTTPCredentials(id)
	if err != nil {
		respond(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, creds)
}

func (h *usersHandlers) gitHTTPCredentialOwner(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid user id is required"})
		return 0, false
	}
	if user, ok := h.requestUser(c.Request, false); !ok || user.ID != id {
		c.JSON(http.StatusForbidden, gin.H{"error": "只能管理当前登录用户的 Git 访问令牌"})
		return 0, false
	}
	return id, true
}

func respondGitHTTPCredentialError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, repo.ErrGitHTTPTokenRequired), errors.Is(err, repo.ErrGitHTTPCredentialManaged):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, repo.ErrGitHTTPCredentialExists):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, repo.ErrGitHTTPHostUnauthorized):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "此主机的访问令牌已不存在"})
	default:
		respond(c, err)
	}
}

// gitHTTPCredentialFromInput validates the form. The platform defaults to
// GitHub for github.com and "other" elsewhere.
func (h *usersHandlers) gitHTTPCredentialFromInput(ctx context.Context, userID int64, input gitHTTPCredentialInput) (domain.GitHTTPCredentialSecret, error) {
	endpoint, err := domain.ParseGitHTTPEndpoint(input.Host)
	if err != nil {
		return domain.GitHTTPCredentialSecret{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return domain.GitHTTPCredentialSecret{}, errors.New("请输入 1 到 80 个字符的凭据名称")
	}
	host := endpoint.Scope()
	platform := domain.GitPlatformOther
	if host == "github.com" {
		platform = domain.GitPlatformGitHub
	}
	if endpoint.Scheme != "http" && strings.TrimSpace(input.Platform) != "" {
		var ok bool
		if platform, ok = domain.NormalizeGitPlatform(input.Platform); !ok {
			return domain.GitHTTPCredentialSecret{}, errors.New("平台只能是 github、gitlab 或 other")
		}
	}
	username := strings.TrimSpace(input.Username)
	if len(username) > 128 || strings.ContainsRune(username, ':') || strings.IndexFunc(username, unicode.IsControl) >= 0 {
		return domain.GitHTTPCredentialSecret{}, errors.New("用户名不超过 128 个字符，且不能包含冒号或控制字符")
	}
	token := strings.TrimSpace(input.Token)
	if len(token) > 4096 || strings.IndexFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return domain.GitHTTPCredentialSecret{}, errors.New("访问令牌不超过 4096 个字符，且不能包含空白或控制字符")
	}
	return domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{
			Name: name, Platform: platform, Host: host, Username: username},
		Token: token,
	}, nil
}

// verifyGitHTTPCredential checks the provider account or read access to a
// plaintext Git repository.
func (h *usersHandlers) verifyGitHTTPCredential(c *gin.Context) {
	id, ok := h.gitHTTPCredentialOwner(c)
	if !ok {
		return
	}
	cred, ok := h.repo.GitHTTPCredentialByID(c.Request.Context(), id, c.Param("credID"))
	if !ok {
		c.JSON(http.StatusNotFound, gitTokenVerifyResult{Error: "此主机的访问令牌已不存在"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if strings.HasPrefix(cred.Host, "http://") {
		var input struct {
			RepoURL string `json:"repoUrl"`
		}
		if !decodeLimit(c, &input, 8<<10) {
			return
		}
		c.JSON(http.StatusOK, h.verifyPlainGitRepository(ctx, cred, input.RepoURL))
		return
	}
	c.JSON(http.StatusOK, verifyGitHTTPToken(ctx, cred, gitHTTPVerifyClient(h.repo.PlatformGitHTTPSAddresses)))
}

func (h *usersHandlers) verifyPlainGitRepository(ctx context.Context, cred domain.GitHTTPCredentialSecret, raw string) gitTokenVerifyResult {
	remote, _, err := gitwork.GitHTTPRepositoryURL(raw)
	if err != nil {
		return gitTokenVerifyResult{Error: err.Error()}
	}
	endpoint, ok := gitwork.GitHTTPRemoteEndpoint(remote)
	if !ok || endpoint.Scope() != cred.Host || endpoint.Scheme != "http" {
		return gitTokenVerifyResult{Error: "仓库地址必须与此令牌的 HTTP 主机和端口一致"}
	}
	authorized, err := h.repo.PlatformGitHTTPHostAuthorized(ctx, endpoint.Authority)
	if err != nil {
		return gitTokenVerifyResult{Error: "暂时无法读取 Git 主机授权，请稍后重试"}
	}
	if !authorized {
		return gitTokenVerifyResult{Error: repo.ErrGitHTTPHostUnauthorized.Error()}
	}
	workspace, err := os.MkdirTemp("", "loopforge-git-access-")
	if err != nil {
		return gitTokenVerifyResult{Error: "暂时无法创建 Git 校验目录"}
	}
	defer os.RemoveAll(workspace)
	ctx = gitwork.WithHTTPCredentials(ctx, map[string]gitwork.HTTPCredential{
		cred.Host: {Username: cred.GitUsername(), Token: cred.Token},
	})
	if _, err := gitwork.Run(ctx, workspace, "ls-remote", "--", remote); err != nil {
		return gitTokenVerifyResult{Error: "无法读取仓库，请检查仓库地址、HTTP 主机授权与令牌的读取权限"}
	}
	return gitTokenVerifyResult{RepositoryReadable: true}
}

func verifyGitHTTPToken(ctx context.Context, cred domain.GitHTTPCredentialSecret, client *http.Client) gitTokenVerifyResult {
	if cred.Managed {
		res := verifyGongfengToken(ctx, cred.Token)
		if res.Valid {
			if _, name, email, err := fetchManagedCommitIdentity(ctx, cred, client); err == nil {
				res.CommitName = name
				res.CommitEmail = email
				if email != "" {
					if base := managedGitAPIBase(); base != "" {
						gitCommitIdentityCache.set(gitCommitIdentityKeyFor(base, cred.Host, cred.Token), name, email, gitCommitIdentityCacheTTL, gitCommitIdentityNow())
					}
				}
			}
		}
		return res
	}
	base := cred.APIBase()
	if base == "" {
		return gitTokenVerifyResult{Error: "该平台不支持在线校验，令牌会在 Git 操作时使用"}
	}
	login, commitName, commitEmail, err := fetchGitHTTPCommitIdentityWithBase(ctx, cred, base, client)
	if err != nil {
		var statusErr gitHTTPAPIStatusError
		switch {
		case errors.Is(err, gitegress.ErrNonPublicAddress):
			return gitTokenVerifyResult{Error: "此 Git HTTPS 主机尚未授权，请联系平台管理员在「Git 主机信任」中登记。"}
		case errors.Is(err, gitegress.ErrHTTPSAddressChanged):
			return gitTokenVerifyResult{Error: "Git 主机解析地址已变更，请联系平台管理员更新该主机的 HTTPS 授权。"}
		case errors.Is(err, gitegress.ErrHTTPSHostLookup):
			return gitTokenVerifyResult{Error: "暂时无法读取 Git 主机授权，请稍后重试。"}
		case errors.As(err, &statusErr):
			return gitTokenVerifyResult{Error: fmt.Sprintf("%s 返回 HTTP %d，请检查令牌", cred.Host, statusErr.status)}
		case errors.Is(err, errInvalidGitHostResponse):
			return gitTokenVerifyResult{Error: fmt.Sprintf("%s 返回了无法识别的响应", cred.Host)}
		case errors.Is(err, errInvalidGitHostRequest):
			return gitTokenVerifyResult{Error: "无效的校验请求"}
		default:
			return gitTokenVerifyResult{Error: fmt.Sprintf("无法连接 %s：%v", cred.Host, err)}
		}
	}
	if commitEmail != "" {
		gitCommitIdentityCache.set(gitCommitIdentityKeyFor(base, cred.Host, cred.Token), commitName, commitEmail, gitCommitIdentityCacheTTL, gitCommitIdentityNow())
	}
	// Repository permissions are per repository; /user proves identity only.
	return gitTokenVerifyResult{Valid: true, Login: login, CommitName: commitName, CommitEmail: commitEmail}
}

var (
	errInvalidGitHostRequest  = errors.New("invalid git host request")
	errInvalidGitHostResponse = errors.New("invalid git host response")
)

type gitHTTPAPIStatusError struct{ status int }

func (e gitHTTPAPIStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.status)
}

func gitHubCommitIdentity(host string, id int64, login, name, email string) (commitName, commitEmail string) {
	login = strings.TrimSpace(login)
	commitName = strings.TrimSpace(name)
	if commitName == "" {
		commitName = login
	}
	commitEmail = strings.TrimSpace(email)
	if commitEmail == "" && login != "" {
		emailHost := strings.TrimSpace(host)
		if h, _, err := net.SplitHostPort(emailHost); err == nil && h != "" {
			emailHost = h
		}
		if emailHost == "" {
			emailHost = "github.com"
		}
		if id > 0 {
			commitEmail = fmt.Sprintf("%d+%s@users.noreply.%s", id, login, emailHost)
		} else {
			commitEmail = fmt.Sprintf("%s@users.noreply.%s", login, emailHost)
		}
	}
	return commitName, commitEmail
}

func gitLabCommitIdentity(username, name, commitEmail, publicEmail, email string) (commitName, resolvedEmail string) {
	commitName = strings.TrimSpace(name)
	if commitName == "" {
		commitName = strings.TrimSpace(username)
	}
	for _, candidate := range []string{commitEmail, publicEmail, email} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			resolvedEmail = trimmed
			break
		}
	}
	return commitName, resolvedEmail
}

func decodeGitHTTPUserIdentity(cred domain.GitHTTPCredential, body io.Reader) (login, commitName, commitEmail string, err error) {
	login, commitName, commitEmail, _, err = decodeGitHTTPUserIdentityDetail(cred, body)
	return login, commitName, commitEmail, err
}

func decodeGitHTTPUserIdentityDetail(cred domain.GitHTTPCredential, body io.Reader) (login, commitName, commitEmail string, explicitEmail bool, err error) {
	var user struct {
		ID          int64  `json:"id"`
		Login       string `json:"login"`
		Username    string `json:"username"`
		Name        string `json:"name"`
		Email       string `json:"email"`
		PublicEmail string `json:"public_email"`
		CommitEmail string `json:"commit_email"`
	}
	if json.NewDecoder(io.LimitReader(body, 64<<10)).Decode(&user) != nil {
		return "", "", "", false, errInvalidGitHostResponse
	}
	if cred.Platform == domain.GitPlatformGitLab {
		login = strings.TrimSpace(user.Username)
		if login == "" {
			return "", "", "", false, errInvalidGitHostResponse
		}
		commitName, commitEmail = gitLabCommitIdentity(login, user.Name, user.CommitEmail, user.PublicEmail, user.Email)
		return login, commitName, commitEmail, commitEmail != "", nil
	}
	login = strings.TrimSpace(user.Login)
	if login == "" {
		return "", "", "", false, errInvalidGitHostResponse
	}
	explicitEmail = strings.TrimSpace(user.Email) != ""
	commitName, commitEmail = gitHubCommitIdentity(cred.Host, user.ID, login, user.Name, user.Email)
	return login, commitName, commitEmail, explicitEmail, nil
}

func fetchGitHTTPCommitIdentityWithBase(ctx context.Context, cred domain.GitHTTPCredentialSecret, base string, client *http.Client) (login, commitName, commitEmail string, err error) {
	if base == "" || cred.Token == "" || client == nil {
		return "", "", "", errInvalidGitHostRequest
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/user", nil)
	if err != nil {
		return "", "", "", errInvalidGitHostRequest
	}
	req.Header.Set(cred.APIAuthHeader())
	if cred.Platform == domain.GitPlatformGitHub {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	res, err := client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", "", "", gitHTTPAPIStatusError{status: res.StatusCode}
	}
	var explicitEmail bool
	login, commitName, commitEmail, explicitEmail, err = decodeGitHTTPUserIdentityDetail(cred.GitHTTPCredential, res.Body)
	if err != nil {
		return "", "", "", err
	}
	if cred.Platform == domain.GitPlatformGitHub && !explicitEmail {
		if email := fetchGitHubVerifiedEmail(ctx, cred, base, client); email != "" {
			commitEmail = email
		}
	}
	return login, commitName, commitEmail, nil
}

func fetchGitHubVerifiedEmail(ctx context.Context, cred domain.GitHTTPCredentialSecret, base string, client *http.Client) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/user/emails", nil)
	if err != nil {
		return ""
	}
	req.Header.Set(cred.APIAuthHeader())
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&emails) != nil {
		return ""
	}
	var verifiedFallback, primaryFallback string
	for _, entry := range emails {
		addr := strings.TrimSpace(entry.Email)
		if addr == "" {
			continue
		}
		if entry.Primary && entry.Verified {
			return addr
		}
		if entry.Verified && verifiedFallback == "" {
			verifiedFallback = addr
		}
		if entry.Primary && primaryFallback == "" {
			primaryFallback = addr
		}
	}
	if verifiedFallback != "" {
		return verifiedFallback
	}
	return primaryFallback
}

func managedGitAPIBase() string {
	if gongfengAPIBase != "" {
		return gongfengAPIBase
	}
	return gongfeng.ConfigFromEnv().APIBase
}

func fetchManagedCommitIdentity(ctx context.Context, cred domain.GitHTTPCredentialSecret, client *http.Client) (login, commitName, commitEmail string, err error) {
	base := managedGitAPIBase()
	if base == "" || cred.Token == "" || client == nil {
		return "", "", "", errInvalidGitHostRequest
	}
	res, err := gongfengDo(ctx, client, strings.TrimRight(base, "/")+"/user", cred.Token)
	if err != nil {
		return "", "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", "", "", gitHTTPAPIStatusError{status: res.StatusCode}
	}
	glCred := cred.GitHTTPCredential
	glCred.Platform = domain.GitPlatformGitLab
	return decodeGitHTTPUserIdentity(glCred, res.Body)
}

const (
	gitCommitIdentityCacheTTL         = 1 * time.Hour
	gitCommitIdentityNegativeCacheTTL = 1 * time.Minute
	gitCommitIdentityCacheMaxEntries  = 4096
)

var gitCommitIdentityNow = time.Now

type gitCommitIdentityKey struct {
	apiBase   string
	host      string
	tokenHash [sha256.Size]byte
}

type gitCommitIdentityEntry struct {
	name      string
	email     string
	expiresAt time.Time
}

type gitCommitIdentityStore struct {
	mu      sync.Mutex
	entries map[gitCommitIdentityKey]gitCommitIdentityEntry
}

var gitCommitIdentityCache = &gitCommitIdentityStore{}

func gitCommitIdentityKeyFor(apiBase, host, token string) gitCommitIdentityKey {
	return gitCommitIdentityKey{
		apiBase:   strings.TrimRight(strings.TrimSpace(apiBase), "/"),
		host:      strings.TrimSpace(host),
		tokenHash: sha256.Sum256([]byte(token)),
	}
}

func (s *gitCommitIdentityStore) get(key gitCommitIdentityKey, now time.Time) (string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == 0 {
		return "", "", false
	}
	entry, ok := s.entries[key]
	if !ok || !now.Before(entry.expiresAt) {
		if ok {
			delete(s.entries, key)
		}
		return "", "", false
	}
	return entry.name, entry.email, true
}

func (s *gitCommitIdentityStore) set(key gitCommitIdentityKey, name, email string, ttl time.Duration, now time.Time) {
	if ttl <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[gitCommitIdentityKey]gitCommitIdentityEntry)
	}
	if len(s.entries) >= gitCommitIdentityCacheMaxEntries {
		for k, v := range s.entries {
			if !now.Before(v.expiresAt) {
				delete(s.entries, k)
			}
		}
		if len(s.entries) >= gitCommitIdentityCacheMaxEntries {
			clear(s.entries)
		}
	}
	s.entries[key] = gitCommitIdentityEntry{
		name:      name,
		email:     email,
		expiresAt: now.Add(ttl),
	}
}

func (s *gitCommitIdentityStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.entries)
}

func resolveGitCommitIdentity(ctx context.Context, st *repo.Store, cred domain.GitHTTPCredentialSecret) (name, email string) {
	if cred.Token == "" {
		return "", ""
	}
	var base string
	if cred.Managed {
		base = managedGitAPIBase()
	} else {
		base = gitHostAPIBase(cred.GitHTTPCredential)
	}
	if base == "" {
		return "", ""
	}
	now := gitCommitIdentityNow()
	key := gitCommitIdentityKeyFor(base, cred.Host, cred.Token)
	if cachedName, cachedEmail, hit := gitCommitIdentityCache.get(key, now); hit {
		return cachedName, cachedEmail
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var client *http.Client
	if st != nil {
		client = gitHostClient(st.PlatformGitHTTPSAddresses)
	} else {
		client = gitHostClient(nil)
	}
	var err error
	if cred.Managed {
		_, name, email, err = fetchManagedCommitIdentity(lookupCtx, cred, client)
	} else {
		_, name, email, err = fetchGitHTTPCommitIdentityWithBase(lookupCtx, cred, base, client)
	}
	if err != nil || strings.TrimSpace(email) == "" {
		gitCommitIdentityCache.set(key, "", "", gitCommitIdentityNegativeCacheTTL, gitCommitIdentityNow())
		return "", ""
	}
	gitCommitIdentityCache.set(key, name, email, gitCommitIdentityCacheTTL, gitCommitIdentityNow())
	return name, email
}
