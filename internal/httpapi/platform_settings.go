package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// 平台管理 → 成员账号 and the shared model credentials (共享模型 Key). Only
// platform administrators may use these endpoints.

// platformAdminWrite admits a platform administrator's change: an API token,
// or a same-origin browser request (the session cookie alone is not enough).
func (d deps) platformAdminWrite(c *gin.Context) bool {
	if !d.isPlatformAdmin(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以执行该操作"})
		return false
	}
	return requireWriteOrigin(c)
}

func requireWriteOrigin(c *gin.Context) bool {
	if _, bearer := bearerToken(c.Request); bearer {
		return true
	}
	origin, err := url.Parse(c.GetHeader("Origin"))
	if err != nil || origin.Host != c.Request.Host || (origin.Scheme != "http" && origin.Scheme != "https") {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid request origin"})
		return false
	}
	return true
}

func (h *usersHandlers) platformAccounts(c *gin.Context) {
	if c.Request.Method != http.MethodGet {
		h.createPlatformAccount(c)
		return
	}
	if !h.isPlatformAdmin(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以查看成员账号"})
		return
	}
	accounts, err := h.repo.ListPlatformAccounts()
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"accounts": accounts, "signupEnabled": signupEnabled()})
}

func validAccountPassword(c *gin.Context, password string) bool {
	if len(password) < repo.MinPasswordLength || len(password) > repo.MaxPasswordLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("密码需 %d–%d 个字符", repo.MinPasswordLength, repo.MaxPasswordLength)})
		return false
	}
	return true
}

func (h *usersHandlers) createPlatformAccount(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		LoginName string `json:"loginName"`
		Password  string `json:"password"`
		Admin     bool   `json:"admin"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account request"})
		return
	}
	input.LoginName = strings.TrimSpace(input.LoginName)
	if !loginPattern.MatchString(input.LoginName) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名需 2–64 位，以字母或数字开头，只能包含字母、数字、_ . -"})
		return
	}
	if !validAccountPassword(c, input.Password) {
		return
	}
	registerMu.Lock()
	u, err := h.repo.CreateLocalAccount(input.LoginName, input.Password, input.Admin)
	if err == nil {
		joinWelcomeProject(h.repo, u.ID)
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
	c.JSON(http.StatusCreated, h.platformAccount(u.ID))
}

func (h *usersHandlers) platformAccount(userID int64) repo.PlatformAccount {
	accounts, _ := h.repo.ListPlatformAccounts()
	for _, account := range accounts {
		if account.UserID == userID {
			return account
		}
	}
	return repo.PlatformAccount{UserID: userID}
}

// resetPlatformAccountPassword sets a new password for a password account.
// Accounts that sign in through GitHub or a gateway are left alone, so this
// cannot add a password login to them.
func (h *usersHandlers) resetPlatformAccountPassword(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid user id is required"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid password request"})
		return
	}
	if !validAccountPassword(c, input.Password) {
		return
	}
	account := h.platformAccount(id)
	if account.LoginName == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在"})
		return
	}
	if !account.Password {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该账号不是用户名密码账号（例如通过 GitHub 登录），不能在这里设置密码"})
		return
	}
	if err := h.repo.SetLocalPassword(id, input.Password); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, account)
}

var sharedModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// sharedModels normalizes the allow list of one shared credential: blanks and
// duplicates are dropped in submission order, every ID must match
// sharedModelPattern, and the list must hold 1..MaxSharedModels entries. The
// error text is shown to the administrator as is.
func sharedModels(raw []string) ([]string, error) {
	var models []string
	seen := map[string]bool{}
	for _, value := range raw {
		id := strings.TrimSpace(value)
		if id == "" || seen[id] {
			continue
		}
		if !sharedModelPattern.MatchString(id) {
			return nil, errors.New("模型 ID 格式不正确：" + id)
		}
		seen[id] = true
		models = append(models, id)
	}
	if len(models) == 0 {
		return nil, errors.New("请至少选择一个允许使用的模型")
	}
	if len(models) > repo.MaxSharedModels {
		return nil, fmt.Errorf("最多选择 %d 个模型", repo.MaxSharedModels)
	}
	return models, nil
}

// sharedProviderLabel is the CLI name shown on 平台管理 → 共享模型 Key.
func sharedProviderLabel(provider string) string {
	switch provider {
	case domain.SharedModelProviderCodex:
		return "Codex"
	case domain.SharedModelProviderClaude:
		return "Claude Code"
	}
	return "CodeBuddy"
}

// sharedProviderParam reads the engine of a shared credential. The older
// /api/platform/codebuddy-key endpoint has no path parameter and stays
// CodeBuddy-only.
func sharedProviderParam(c *gin.Context) (string, bool) {
	raw := c.Param("provider")
	if raw == "" {
		raw = c.Query("provider")
	}
	provider, ok := domain.NormalizeSharedModelProvider(raw)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "共享 Key 只支持 CodeBuddy、Codex 和 Claude Code"})
		return "", false
	}
	return provider, true
}

type platformModelKeyResponse struct {
	repo.PlatformModelKeyStatus
	// Catalog lists model IDs to offer on the page: the CodeBuddy models of
	// the requested network environment, or the CLI's built-in catalog.
	Catalog []assistantModel `json:"catalog"`
}

// platformModelKeyList shows every shared credential, without catalogs.
func (h *usersHandlers) platformModelKeyList(c *gin.Context) {
	if !h.isPlatformAdmin(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以查看共享 Key"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"keys": h.repo.PlatformModelKeyStatuses()})
}

// platformModelKeys shows and edits one engine's shared API Key. The key
// itself is write-only: responses only say whether one is configured.
func (h *usersHandlers) platformModelKeys(c *gin.Context) {
	provider, ok := sharedProviderParam(c)
	if !ok {
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		if !h.isPlatformAdmin(c.Request) {
			c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以查看共享 Key"})
			return
		}
		status := h.repo.PlatformModelKeyStatus(provider)
		environment := status.InternetEnvironment
		if raw, ok := c.GetQuery("environment"); ok && provider == domain.SharedModelProviderCodeBuddy {
			normalized, valid := domain.NormalizeCodeBuddyInternetEnvironment(raw)
			if !valid {
				c.JSON(http.StatusBadRequest, gin.H{"error": "CodeBuddy 网络环境只能是国际站（留空）、国内站（internal）或 iOA（ioa）"})
				return
			}
			environment = normalized
		}
		apiKey := ""
		if key, found := h.repo.SharedModelKey(provider); found && key.SponsorID == 0 && key.InternetEnvironment == environment {
			apiKey = key.APIKey
		}
		c.JSON(http.StatusOK, platformModelKeyResponse{
			PlatformModelKeyStatus: status,
			Catalog:                h.sharedModelCatalog(c.Request.Context(), provider, environment, apiKey),
		})
	case http.MethodPut:
		h.savePlatformModelKey(c, provider)
	case http.MethodDelete:
		if !h.platformAdminWrite(c) {
			return
		}
		if err := h.repo.ClearPlatformModelKey(provider); err != nil {
			respond(c, err)
			return
		}
		c.JSON(http.StatusOK, h.repo.PlatformModelKeyStatus(provider))
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

// platformCodeBuddyKey keeps the single-Key endpoint older frontends call.
func (h *usersHandlers) platformCodeBuddyKey(c *gin.Context) {
	h.platformModelKeys(c)
}

// sharedModelCatalog lists the models the page offers for an engine. CodeBuddy
// models come from the CLI in the chosen network environment; the other CLIs
// show their built-in catalog, and the page can probe a Base URL for more.
func (h *usersHandlers) sharedModelCatalog(ctx context.Context, provider, environment, apiKey string) []assistantModel {
	if provider == domain.SharedModelProviderCodeBuddy {
		refreshCLIModels(ctx, environment, apiKey)
		return loadAssistantModels(environment, apiKey).Models
	}
	var catalog []assistantModel
	for _, model := range assistantEngineBuiltinModels(provider) {
		name := model.Name
		if name == "" {
			name = model.ID
		}
		catalog = append(catalog, assistantListedModel(model.ID, name, ""))
	}
	return catalog
}

func (h *usersHandlers) savePlatformModelKey(c *gin.Context, provider string) {
	if !h.platformAdminWrite(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var input struct {
		APIKey              string   `json:"apiKey"`
		BaseURL             string   `json:"baseUrl"`
		InternetEnvironment string   `json:"internetEnvironment"`
		Models              []string `json:"models"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid shared key request"})
		return
	}
	environment := ""
	if provider == domain.SharedModelProviderCodeBuddy {
		normalized, ok := domain.NormalizeCodeBuddyInternetEnvironment(input.InternetEnvironment)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "CodeBuddy 网络环境只能是国际站（留空）、国内站（internal）或 iOA（ioa）"})
			return
		}
		environment = normalized
	}
	key := strings.TrimSpace(input.APIKey)
	if len(key) > 4096 || strings.ContainsAny(key, " \t\r\n") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "API Key 格式不正确"})
		return
	}
	baseURL := ""
	if provider != domain.SharedModelProviderCodeBuddy {
		baseURL = strings.TrimSpace(input.BaseURL)
		if !validCLIBaseURL(baseURL) {
			c.JSON(http.StatusBadRequest, gin.H{"error": errInvalidCLIBaseURL})
			return
		}
	}
	models, err := sharedModels(input.Models)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var actorID int64
	if current, ok := h.requestUser(c.Request, true); ok {
		actorID = current.ID
	}
	err = h.repo.SetPlatformModelKey(repo.PlatformModelKeyUpdate{
		Provider: provider, APIKey: key, BaseURL: baseURL,
		InternetEnvironment: environment, Models: models,
	}, actorID)
	if errors.Is(err, repo.ErrSharedModelKeyRequired) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 " + sharedProviderLabel(provider) + " API Key"})
		return
	}
	if errors.Is(err, repo.ErrSharedModelKeyBaseURLChanged) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "修改 Base URL 时请重新填写 " + sharedProviderLabel(provider) + " API Key"})
		return
	}
	if errors.Is(err, repo.ErrSharedModelKeyProvider) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "共享 Key 只支持 CodeBuddy、Codex 和 Claude Code"})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, h.repo.PlatformModelKeyStatus(provider))
}

// probePlatformModelKey lists the models the entered key can reach, for the
// 共享模型 Key form. Nothing is stored; like verifyGitToken it answers 200
// with a structured result so the form can show the reason.
func (h *usersHandlers) probePlatformModelKey(c *gin.Context) {
	if !h.isPlatformAdmin(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以读取共享 Key 的模型"})
		return
	}
	provider, ok := sharedProviderParam(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var input struct {
		APIKey  string `json:"apiKey"`
		BaseURL string `json:"baseUrl"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, cliModelProbeResult{Error: "invalid model probe request"})
		return
	}
	c.Header("Cache-Control", "private, no-store")
	if provider == domain.SharedModelProviderCodeBuddy {
		// CodeBuddy's catalog follows the network environment and the CLI
		// probe the page already runs on load, not a typed Base URL.
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "CodeBuddy 的模型按网络环境自动读取，不需要单独获取"})
		return
	}
	apiKey, baseURL := strings.TrimSpace(input.APIKey), strings.TrimSpace(input.BaseURL)
	if apiKey == "" {
		if stored, found := h.repo.SharedModelKey(provider); found && stored.SponsorID == 0 {
			if baseURL == "" {
				baseURL = stored.BaseURL
			}
			// The stored key is write-only: probing it against a Base URL
			// other than its own would hand it to whoever runs that URL.
			if !repo.SameSharedModelBaseURL(baseURL, stored.BaseURL) {
				c.JSON(http.StatusOK, cliModelProbeResult{Error: "修改了 Base URL，请重新填写 " + sharedProviderLabel(provider) + " API Key 再获取模型"})
				return
			}
			apiKey = stored.APIKey
		}
	}
	if !validCLIBaseURL(baseURL) {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: errInvalidCLIBaseURL})
		return
	}
	if apiKey == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "请先填写 " + sharedProviderLabel(provider) + " API Key"})
		return
	}
	source := "remote"
	if baseURL == "" {
		source = "official"
		switch provider {
		case domain.SharedModelProviderCodex:
			baseURL = "https://api.openai.com/v1"
		case domain.SharedModelProviderClaude:
			baseURL = assistantAnthropicAPIBase
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	models, err := assistantEngineDiscover(ctx, assistantEngineAnthropicModels(provider, ""), apiKey, baseURL)
	if err != nil {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "获取模型列表失败：" + err.Error()})
		return
	}
	if len(models) == 0 {
		c.JSON(http.StatusOK, cliModelProbeResult{Connected: true, Error: "服务没有返回可用模型，可以手动添加模型"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(provider, cliModelsOf(models), ""), Source: source, Connected: true})
}
