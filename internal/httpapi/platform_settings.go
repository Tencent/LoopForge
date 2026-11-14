package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// 平台管理 → 成员账号 and 平台共享 CodeBuddy Key. Only platform
// administrators may use these endpoints.

// platformAdminWrite admits a platform administrator's change: an API token,
// or a same-origin browser request (the session cookie alone is not enough).
func (h *usersHandlers) platformAdminWrite(c *gin.Context) bool {
	if !h.isPlatformAdmin(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以执行该操作"})
		return false
	}
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

// sharedCodeBuddyModels normalizes the allow list of the shared CodeBuddy
// key: blanks and duplicates are dropped in submission order, every ID must
// match sharedModelPattern, and the list must hold 1..MaxSharedCodeBuddyModels
// entries. The error text is shown to the administrator as is.
func sharedCodeBuddyModels(raw []string) ([]string, error) {
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
	if len(models) > repo.MaxSharedCodeBuddyModels {
		return nil, fmt.Errorf("最多选择 %d 个模型", repo.MaxSharedCodeBuddyModels)
	}
	return models, nil
}

type platformCodeBuddyKeyResponse struct {
	repo.PlatformCodeBuddyKeyStatus
	// Catalog lists the models offered in the requested network environment.
	Catalog []assistantModel `json:"catalog"`
}

// platformCodeBuddyKey shows and edits the shared CodeBuddy API Key. The key
// itself is write-only: responses only say whether one is configured.
func (h *usersHandlers) platformCodeBuddyKey(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet:
		if !h.isPlatformAdmin(c.Request) {
			c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以查看共享 Key"})
			return
		}
		status := h.repo.PlatformCodeBuddyKeyStatus()
		environment := status.InternetEnvironment
		if raw, ok := c.GetQuery("environment"); ok {
			normalized, valid := domain.NormalizeCodeBuddyInternetEnvironment(raw)
			if !valid {
				c.JSON(http.StatusBadRequest, gin.H{"error": "CodeBuddy 网络环境只能是国际站（留空）、国内站（internal）或 iOA（ioa）"})
				return
			}
			environment = normalized
		}
		apiKey := ""
		if key, ok := h.repo.SharedCodeBuddyKey(); ok && key.SponsorID == 0 && key.InternetEnvironment == environment {
			apiKey = key.APIKey
		}
		refreshCLIModels(c.Request.Context(), environment, apiKey)
		catalog := loadAssistantModels(c.Request.Context(), environment).Models
		c.JSON(http.StatusOK, platformCodeBuddyKeyResponse{PlatformCodeBuddyKeyStatus: status, Catalog: catalog})
	case http.MethodPut:
		h.savePlatformCodeBuddyKey(c)
	case http.MethodDelete:
		if !h.platformAdminWrite(c) {
			return
		}
		if err := h.repo.ClearPlatformCodeBuddyKey(); err != nil {
			respond(c, err)
			return
		}
		c.JSON(http.StatusOK, h.repo.PlatformCodeBuddyKeyStatus())
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

func (h *usersHandlers) savePlatformCodeBuddyKey(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var input struct {
		APIKey              string   `json:"apiKey"`
		InternetEnvironment string   `json:"internetEnvironment"`
		Models              []string `json:"models"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid shared key request"})
		return
	}
	environment, ok := domain.NormalizeCodeBuddyInternetEnvironment(input.InternetEnvironment)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CodeBuddy 网络环境只能是国际站（留空）、国内站（internal）或 iOA（ioa）"})
		return
	}
	key := strings.TrimSpace(input.APIKey)
	if len(key) > 4096 || strings.ContainsAny(key, " \t\r\n") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "API Key 格式不正确"})
		return
	}
	models, err := sharedCodeBuddyModels(input.Models)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var actorID int64
	if current, ok := h.requestUser(c.Request, true); ok {
		actorID = current.ID
	}
	err = h.repo.SetPlatformCodeBuddyKey(repo.PlatformCodeBuddyKeyUpdate{APIKey: key, InternetEnvironment: environment, Models: models}, actorID)
	if errors.Is(err, repo.ErrSharedCodeBuddyKeyRequired) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 CodeBuddy API Key"})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, h.repo.PlatformCodeBuddyKeyStatus())
}
