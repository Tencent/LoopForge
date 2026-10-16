package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	stdsync "sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// CLI Runner connection API (cli-connect.md).
//
// Runner-agent endpoints authenticate with their own credentials and are
// therefore exempt from user access auth: register uses the shared bootstrap
// token, everything else uses the runner's session token. Management
// endpoints (list runners, create/cancel tasks) stay behind user auth.
//
// Task reliability never depends on a live connection: the task table is the
// queue, the long poll is only a wakeup path, and WebSocket is the low-latency
// control path.
//
// 路由（gin 注册，供 server.go 参考）：
//
// 管理面（userAccessAuth 保护）:
//
//	g.GET("/api/runners", h.runners)
//	g.POST("/api/runners/register", h.runnerRegister)
//	g.POST("/api/runners/deregister", h.runnerDeregister)
//	g.POST("/api/runners/disconnect", h.runnerDisconnect)
//	g.POST("/api/runners/bind-codes", h.issueRunnerBindCode)
//	g.GET("/api/runners/default", h.runnerDefault)
//	g.DELETE("/api/runners/:id", h.runnerRemove)
//	g.PUT("/api/runners/default", h.runnerDefaultSet)
//	g.PUT("/api/runners/runtimes", h.runnerRuntimeStatus)
//	g.POST("/api/runners/runtimes", h.runnerRuntimeStatus)
//	g.GET("/api/runners/tasks", h.runnerTasksCollection)
//	g.POST("/api/runners/tasks", h.runnerTasksCollection)
//	g.POST("/api/runners/tasks/claim", h.runnerTaskClaim)
//	g.GET("/api/runners/tasks/:id", h.runnerTaskGet)
//	g.POST("/api/runners/tasks/:id/cancel", h.runnerTaskCancel)
//	g.GET("/api/runners/tasks/:id/events", h.runnerTaskEventsOut)
//	g.POST("/api/runners/tasks/:id/events", h.runnerTaskEventsIngest)
//	g.POST("/api/runners/tasks/:id/start", h.runnerTaskStart)
//	g.POST("/api/runners/tasks/:id/lease/renew", h.runnerTaskLeaseRenew)
//	g.POST("/api/runners/tasks/:id/complete", h.runnerTaskComplete)
//	g.GET("/api/runners/tasks/:id/control", h.runnerTaskControl)
//
// 机器面 /runners/*（豁免 userAccessAuth，runner 使用自有凭证）:
//
//	g.GET("/runners/install.sh", serveBindCLIScript)（定义在 server.go）
//	g.GET("/runners/agentflow-runner", serveRunnerBinary)
//	g.GET("/runners/health", h.health)（定义在 server.go）
//	g.POST("/runners/register", h.runnerRegister)
//	g.POST("/runners/deregister", h.runnerDeregister)
//	g.POST("/runners/disconnect", h.runnerDisconnect)
//	g.POST("/runners/heartbeat", h.runnerHeartbeat)
//	g.POST("/runners/tasks/claim", h.runnerTaskClaim)
//	g.POST("/runners/tasks/:id/start", h.runnerTaskStart)
//	g.POST("/runners/tasks/:id/lease/renew", h.runnerTaskLeaseRenew)
//	g.POST("/runners/tasks/:id/events", h.runnerTaskEventsIngest)
//	g.POST("/runners/tasks/:id/complete", h.runnerTaskComplete)
//	g.GET("/runners/tasks/:id/control", h.runnerTaskControl)

const (
	runnerLongPollDefault    = 30 * time.Second
	runnerDBPollDefault      = time.Second
	runnerOfflineThreshold   = 90 * time.Second
	runnerTaskLeaseTTL       = 120 * time.Second
	runnerSweepInterval      = 15 * time.Second
	runnerSessionTokenPrefix = "afr_"
	runnerSessionTTL         = 30 * 24 * time.Hour
)

type runnersHandlers struct {
	*deps
	runnerWaiters   *runnerWaiterRegistry
	runnerControls  *runnerControlHub
	runnerBindMu    stdsync.Mutex
	runnerBindCodes map[string]runnerBindCode
}

// ---------- waiter registry ----------

// runnerWaiterRegistry wakes long-polling runners when a task may have become
// claimable. Notify is only "there might be work": the DB claim is still the
// single source of truth, so a lost or spurious wakeup is harmless.
type runnerWaiterRegistry struct {
	mu      stdsync.Mutex
	nextID  int64
	waiters map[int64]*runnerWaiter
}

type runnerWaiter struct {
	id        int64
	providers []string
	ch        chan struct{}
}

func newRunnerWaiterRegistry() *runnerWaiterRegistry {
	return &runnerWaiterRegistry{waiters: map[int64]*runnerWaiter{}}
}

func (r *runnerWaiterRegistry) register(providers []string) *runnerWaiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	w := &runnerWaiter{id: r.nextID, providers: providers, ch: make(chan struct{}, 1)}
	r.waiters[w.id] = w
	return w
}

func (r *runnerWaiterRegistry) unregister(w *runnerWaiter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.waiters, w.id)
}

func (r *runnerWaiterRegistry) notify(providers []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, w := range r.waiters {
		for _, p := range providers {
			if containsStringFold(w.providers, p) {
				select {
				case w.ch <- struct{}{}:
				default:
				}
				break
			}
		}
	}
}

func containsStringFold(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// ---------- background sweeper ----------

// startRunnerSweeper recovers tasks whose lease expired (runner slept or
// crashed) and marks silent runners offline. Both thresholds follow the
// defaults recommended in cli-connect.md.
func (h *runnersHandlers) startRunnerSweeper() {
	go func() {
		ticker := time.NewTicker(runnerSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now().UTC()
			h.repo.MarkOfflineRunners(runnerOfflineThreshold, now)
			recovered, err := h.repo.ExpireRunnerTasks(now)
			if err != nil {
				continue
			}
			for _, task := range recovered {
				h.engine.NotifyRunnerTaskChanged(task.ID)
			}
		}
	}()
}

// ---------- authentication ----------

func runnerBootstrapToken() string {
	return strings.TrimSpace(os.Getenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN"))
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// runnerFromSession resolves the Bearer session token issued at registration.
func (h *runnersHandlers) runnerFromSession(r *http.Request) (domain.Runner, bool) {
	raw, ok := bearerToken(r)
	if !ok || !strings.HasPrefix(raw, runnerSessionTokenPrefix) {
		return domain.Runner{}, false
	}
	sum := sha256.Sum256([]byte(raw))
	return h.repo.RunnerSessionByHash(hex.EncodeToString(sum[:]))
}

// runnerSessionAuth resolves the runner session for task-plane handlers and
// writes the 401 response when the credential is missing or invalid.
func (h *runnersHandlers) runnerSessionAuth(c *gin.Context) (domain.Runner, bool) {
	runner, ok := h.runnerFromSession(c.Request)
	if !ok {
		c.Header("WWW-Authenticate", `Bearer realm="AgentFlow Runner"`)
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid runner session"})
	}
	return runner, ok
}

func newRunnerSessionToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plain := runnerSessionTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(plain))
	return plain, hex.EncodeToString(sum[:]), nil
}

// ---------- management endpoints ----------

// 路由: g.GET("/api/runners", h.runners)
func (h *runnersHandlers) runners(c *gin.Context) {
	list := domain.PublicRunners(h.repo.Runners())
	// The personal 执行器 page lists only machines bound to the current user.
	if strings.TrimSpace(c.Query("mine")) == "1" {
		user, ok := h.requestUser(c.Request, true)
		if !ok {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后查看我的执行器"})
			return
		}
		mine := make([]domain.Runner, 0, len(list))
		for _, item := range list {
			if item.OwnerUserID == user.ID {
				mine = append(mine, item)
			}
		}
		list = mine
	}
	c.JSON(http.StatusOK, list)
}

// runnerDefault returns the user's default execution machine (执行机器).
// 路由: g.GET("/api/runners/default", h.runnerDefault)
func (h *runnersHandlers) runnerDefault(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后管理执行机器"})
		return
	}
	runnerID := h.repo.DefaultRunner(user.ID)
	runner, found := h.repo.Runner(runnerID)
	var poolConfigured bool
	var poolOnline int
	if h.engine != nil {
		poolConfigured, poolOnline = h.engine.CloudPoolStatus()
	}
	c.JSON(http.StatusOK, map[string]any{
		"runnerId":  runnerID,
		"runner":    runner,
		"online":    found && runner.Status == domain.RunnerStatusOnline,
		"cloudPool": map[string]any{"configured": poolConfigured, "online": poolOnline},
	})
}

// runnerRemove unbinds one of the caller's own offline machines, for a machine
// that is gone for good and can no longer deregister itself.
// 路由: g.DELETE("/api/runners/:id", h.runnerRemove)
func (h *runnersHandlers) runnerRemove(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后管理执行机器"})
		return
	}
	runner, found := h.repo.Runner(strings.TrimSpace(c.Param("id")))
	if !found {
		c.JSON(http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if runner.OwnerUserID != user.ID {
		c.JSON(http.StatusForbidden, map[string]string{"error": "只能移除自己绑定的执行机器"})
		return
	}
	if runner.Status == domain.RunnerStatusOnline {
		c.JSON(http.StatusConflict, map[string]string{"error": "机器在线，请先在该机器上执行卸载命令，或停止后再移除"})
		return
	}
	if err := h.repo.DeregisterRunner(runner.ID, time.Now().UTC()); err != nil && err != repo.ErrRunnerNotFound {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// runnerRunLimitSet sets how many top-level SOP runs one of the caller's own
// machines may hold at once. 0 clears the limit (unlimited, the default).
// 路由: g.PUT("/api/runners/:id/run-limit", h.runnerRunLimitSet)
func (h *runnersHandlers) runnerRunLimitSet(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后管理执行机器"})
		return
	}
	var input struct {
		RunLimit *int `json:"runLimit"`
	}
	if !decode(c, &input) {
		return
	}
	if input.RunLimit == nil || *input.RunLimit < 0 || *input.RunLimit > repo.MaxRunnerRunLimit {
		c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("runLimit 需为 0-%d 的整数，0 表示不限制", repo.MaxRunnerRunLimit)})
		return
	}
	runner, found := h.repo.Runner(strings.TrimSpace(c.Param("id")))
	if !found {
		c.JSON(http.StatusNotFound, map[string]string{"error": "执行机器不存在"})
		return
	}
	if runner.OwnerUserID == 0 || runner.OwnerUserID != user.ID {
		c.JSON(http.StatusForbidden, map[string]string{"error": "只能设置自己绑定的执行机器"})
		return
	}
	if err := h.repo.SetRunnerRunLimit(runner.ID, *input.RunLimit); err != nil {
		if err == repo.ErrRunnerNotFound {
			c.JSON(http.StatusNotFound, map[string]string{"error": "执行机器不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	runner, _ = h.repo.Runner(runner.ID)
	c.JSON(http.StatusOK, domain.PublicRunner(runner))
}

// runnerDefaultSet selects or clears the user's default execution machine.
// Only machines bound to the requesting user can be selected.
// 路由: g.PUT("/api/runners/default", h.runnerDefaultSet)
func (h *runnersHandlers) runnerDefaultSet(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后管理执行机器"})
		return
	}
	var input struct {
		RunnerID string `json:"runnerId"`
	}
	if !decode(c, &input) {
		return
	}
	runnerID := strings.TrimSpace(input.RunnerID)
	if runnerID != "" {
		runner, found := h.repo.Runner(runnerID)
		if !found {
			c.JSON(http.StatusNotFound, map[string]string{"error": "执行机器不存在"})
			return
		}
		if runner.OwnerUserID != user.ID {
			c.JSON(http.StatusForbidden, map[string]string{"error": "只能选择自己绑定的执行机器"})
			return
		}
	}
	if err := h.repo.SetDefaultRunner(user.ID, runnerID, time.Now().UTC()); err != nil {
		if err == repo.ErrRunnerNotFound {
			c.JSON(http.StatusNotFound, map[string]string{"error": "执行机器不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if runnerID == "" {
		c.JSON(http.StatusOK, map[string]any{"runnerId": "", "message": "已恢复云端 API Key 模式"})
	} else {
		c.JSON(http.StatusOK, map[string]any{"runnerId": runnerID, "message": "已设为默认执行机器，你的任务和沙箱将在这台机器上执行"})
	}
}

// runnerRuntimeStatus updates per-provider runtime status/auth mode. The
// trailing sub-path of the legacy /api/runners/runtimes/... form was always
// ignored, so this is registered as a flat PUT|POST route.
// 路由: g.PUT("/api/runners/runtimes", h.runnerRuntimeStatus)
//
//	g.POST("/api/runners/runtimes", h.runnerRuntimeStatus)
func (h *runnersHandlers) runnerRuntimeStatus(c *gin.Context) {
	var input struct {
		RunnerID string `json:"runnerId"`
		Provider string `json:"provider"`
		Status   string `json:"status"`
		AuthMode string `json:"authMode"`
	}
	if !decode(c, &input) {
		return
	}
	input.RunnerID = strings.TrimSpace(input.RunnerID)
	input.Provider = strings.TrimSpace(input.Provider)
	input.Status = strings.TrimSpace(input.Status)
	input.AuthMode = strings.TrimSpace(input.AuthMode)
	if input.RunnerID == "" || input.Provider == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "runnerId 和 provider 不能为空"})
		return
	}
	if input.Status == "" && input.AuthMode == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "status 和 authMode 至少提供一个"})
		return
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后管理执行机器"})
		return
	}
	runner, found := h.repo.Runner(input.RunnerID)
	if !found {
		c.JSON(http.StatusNotFound, map[string]string{"error": "执行机器不存在"})
		return
	}
	if runner.OwnerUserID != user.ID {
		c.JSON(http.StatusForbidden, map[string]string{"error": "只能管理自己绑定的执行机器"})
		return
	}
	now := time.Now().UTC()
	if input.Status != "" {
		if err := h.repo.SetRunnerRuntimeStatus(input.RunnerID, input.Provider, input.Status, now); err != nil {
			if err == repo.ErrRunnerNotFound {
				c.JSON(http.StatusNotFound, map[string]string{"error": "执行机器不存在"})
				return
			}
			c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if input.AuthMode != "" {
		if err := h.repo.SetRunnerRuntimeAuthMode(input.RunnerID, input.Provider, input.AuthMode, now); err != nil {
			if err == repo.ErrRunnerNotFound {
				c.JSON(http.StatusNotFound, map[string]string{"error": "执行机器不存在"})
				return
			}
			c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	runner, _ = h.repo.Runner(input.RunnerID)
	c.JSON(http.StatusOK, domain.PublicRunner(runner))
}

// runnerDisconnect marks a daemon offline without deleting its identity,
// session, or the user's default-runner selection. A normal service restart
// therefore reconnects with the existing session instead of needing a new
// one-time bind code. Explicit uninstall continues to use deregister.
// 路由: g.POST("/api/runners/disconnect", h.runnerDisconnect)
//
//	g.POST("/runners/disconnect", h.runnerDisconnect)
func (h *runnersHandlers) runnerDisconnect(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	if err := h.repo.MarkRunnerOffline(runner.ID, time.Now().UTC()); err != nil {
		if err == repo.ErrRunnerNotFound {
			c.JSON(http.StatusOK, map[string]bool{"ok": true})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// ---------- one-time bind codes ----------

// runnerBindCodeTTL keeps the copy-pasted onboarding command valid long
// enough to reach a terminal without leaving a standing credential behind.
const runnerBindCodeTTL = 10 * time.Minute

const runnerBindCodePrefix = "bind-"

type runnerBindCode struct {
	userID    int64
	expiresAt time.Time
}

// consumeRunnerBindCode resolves and burns a one-time bind code generated by
// the 执行器 page. Burning on match keeps codes strictly single-use: a leaked
// command cannot be replayed, and the user simply refreshes the page for a
// new one.
func (h *runnersHandlers) consumeRunnerBindCode(code string) (int64, bool) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, runnerBindCodePrefix) {
		return 0, false
	}
	h.runnerBindMu.Lock()
	defer h.runnerBindMu.Unlock()
	now := time.Now().UTC()
	entry, exists := h.runnerBindCodes[code]
	if !exists || !entry.expiresAt.After(now) {
		if exists {
			delete(h.runnerBindCodes, code)
		}
		return 0, false
	}
	delete(h.runnerBindCodes, code)
	return entry.userID, true
}

// newRunnerBindCode issues a one-time code that registers the machine under
// the requesting user. Called from the 执行器 page; requires an interactive
// login so the machine's owner is unambiguous.
// 路由: g.POST("/api/runners/bind-codes", h.issueRunnerBindCode)
func (h *runnersHandlers) issueRunnerBindCode(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后生成接入命令"})
		return
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "生成绑定码失败"})
		return
	}
	code := runnerBindCodePrefix + base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now().UTC()
	h.runnerBindMu.Lock()
	// Opportunistically drop expired codes so the map cannot grow unbounded.
	for key, entry := range h.runnerBindCodes {
		if !entry.expiresAt.After(now) {
			delete(h.runnerBindCodes, key)
		}
	}
	h.runnerBindCodes[code] = runnerBindCode{userID: user.ID, expiresAt: now.Add(runnerBindCodeTTL)}
	h.runnerBindMu.Unlock()
	c.JSON(http.StatusOK, map[string]any{"code": code, "expiresAt": now.Add(runnerBindCodeTTL)})
}

// ---------- registration ----------

// resolveRunnerRegistrant accepts, in order: a one-time bind code from the
// 执行器 page (binds the machine to that user), the shared server bootstrap
// token, or a user personal token (afp_).
func (h *runnersHandlers) resolveRunnerRegistrant(r *http.Request) (ownerUserID int64, ok bool) {
	provided := strings.TrimSpace(r.Header.Get("X-Bootstrap-Token"))
	if provided == "" {
		if raw, bearerOK := bearerToken(r); bearerOK {
			provided = raw
		}
	}
	if provided == "" {
		return 0, false
	}
	if userID, matched := h.consumeRunnerBindCode(provided); matched {
		return userID, true
	}
	if bootstrap := runnerBootstrapToken(); bootstrap != "" && constantTimeEqual(provided, bootstrap) {
		return 0, true
	}
	if user, userOK := h.requestUser(r, true); userOK {
		return user.ID, true
	}
	return 0, false
}

// runnerDeregister permanently removes an uninstalled machine. Ordinary
// daemon shutdown uses runnerDisconnect so service restarts retain identity.
// 路由: g.POST("/api/runners/deregister", h.runnerDeregister)
//
//	g.POST("/runners/deregister", h.runnerDeregister)
func (h *runnersHandlers) runnerDeregister(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	if err := h.repo.DeregisterRunner(runner.ID, time.Now().UTC()); err != nil {
		if err == repo.ErrRunnerNotFound {
			c.JSON(http.StatusOK, map[string]bool{"ok": true})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// 路由: g.POST("/api/runners/register", h.runnerRegister)
//
//	g.POST("/runners/register", h.runnerRegister)
func (h *runnersHandlers) runnerRegister(c *gin.Context) {
	provided := strings.TrimSpace(c.GetHeader("X-Bootstrap-Token"))
	if provided == "" {
		if raw, bearerOK := bearerToken(c.Request); bearerOK {
			provided = raw
		}
	}
	owner, authorized := h.resolveRunnerRegistrant(c.Request)
	if !authorized {
		// No credential at all and no shared bootstrap token configured:
		// registration is closed. A presented-but-invalid credential (for
		// example an expired or replayed bind code) is always 401 so the
		// machine-side message tells the user what to do next.
		if provided == "" && runnerBootstrapToken() == "" {
			c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "runner registration is disabled: set AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN on the server"})
			return
		}
		c.Header("WWW-Authenticate", `Bearer realm="AgentFlow Runner"`)
		if strings.HasPrefix(provided, runnerBindCodePrefix) {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "绑定码无效、已使用或已过期：请在「个人 → 执行器」页面刷新后重新复制命令"})
			return
		}
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid bind token"})
		return
	}
	var input struct {
		RunnerID       string                 `json:"runnerId"`
		Name           string                 `json:"name"`
		OS             string                 `json:"os"`
		Arch           string                 `json:"arch"`
		Version        string                 `json:"version"`
		Labels         []string               `json:"labels"`
		MaxConcurrency int                    `json:"maxConcurrency"`
		Runtimes       []domain.RunnerRuntime `json:"runtimes"`
	}
	if !decode(c, &input) {
		return
	}
	if input.MaxConcurrency < 1 {
		input.MaxConcurrency = 1
	}
	if input.MaxConcurrency > 32 {
		input.MaxConcurrency = 32
	}
	// A re-registration with a known runner id rotates the session token and
	// re-binds ownership to the token used for this registration.
	now := time.Now().UTC()
	runner := domain.Runner{
		Name:           strings.TrimSpace(input.Name),
		OS:             strings.TrimSpace(input.OS),
		Arch:           strings.TrimSpace(input.Arch),
		RunnerVersion:  strings.TrimSpace(input.Version),
		Labels:         input.Labels,
		MaxConcurrency: input.MaxConcurrency,
		Status:         domain.RunnerStatusOnline,
		Runtimes:       normalizeRunnerRuntimes(input.Runtimes, nil),
		OwnerUserID:    owner,
		LastSeenAt:     &now,
	}
	if existing, ok := h.repo.Runner(strings.TrimSpace(input.RunnerID)); ok {
		runner.ID = existing.ID
		runner.CreatedAt = existing.CreatedAt
		runner.Runtimes = normalizeRunnerRuntimes(input.Runtimes, existing.Runtimes)
	} else {
		runner.ID = "runner-" + randomID()
	}
	plain, hash, err := newRunnerSessionToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "生成 session token 失败"})
		return
	}
	expires := now.Add(runnerSessionTTL)
	runner.SessionTokenHash = hash
	runner.SessionExpiresAt = &expires
	if err := h.repo.PutRunner(runner); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"runnerId":          runner.ID,
		"sessionToken":      plain,
		"expiresAt":         expires,
		"heartbeatInterval": 30 * time.Second,
		"leaseTTL":          runnerTaskLeaseTTL,
	})
}

func normalizeRunnerRuntimes(reported, existing []domain.RunnerRuntime) []domain.RunnerRuntime {
	preferences := map[string]domain.RunnerRuntime{}
	for _, rt := range existing {
		preferences[strings.ToLower(strings.TrimSpace(rt.Provider))] = rt
	}
	out := append([]domain.RunnerRuntime(nil), reported...)
	for i := range out {
		previous := preferences[strings.ToLower(strings.TrimSpace(out[i].Provider))]
		if previous.Status == domain.RuntimeStatusDisabled {
			out[i].Status = domain.RuntimeStatusDisabled
		}
		if previous.AuthMode == domain.RuntimeAuthLocal || previous.AuthMode == domain.RuntimeAuthPersonal {
			out[i].AuthMode = previous.AuthMode
		} else if strings.EqualFold(out[i].Provider, "codebuddy") {
			out[i].AuthMode = domain.RuntimeAuthPersonal
		} else {
			out[i].AuthMode = domain.RuntimeAuthLocal
		}
	}
	return out
}

// ---------- heartbeat ----------

// 路由: g.POST("/api/runners/heartbeat", h.runnerHeartbeat)
//
//	g.POST("/runners/heartbeat", h.runnerHeartbeat)
func (h *runnersHandlers) runnerHeartbeat(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	var input struct {
		Status       string                 `json:"status"`
		RunningTasks int                    `json:"runningTasks"`
		Version      string                 `json:"version"`
		Runtimes     []domain.RunnerRuntime `json:"runtimes"`
	}
	if !decode(c, &input) {
		return
	}
	now := time.Now().UTC()
	expires := now.Add(runnerSessionTTL)
	if err := h.repo.RunnerHeartbeat(runner.ID, input.Runtimes, strings.TrimSpace(input.Version), now, expires); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"ok": true, "serverTime": now, "expiresAt": expires})
}

// ---------- task collection (management) ----------

// 路由: g.GET("/api/runners/tasks", h.runnerTasksCollection)
//
//	g.POST("/api/runners/tasks", h.runnerTasksCollection)
func (h *runnersHandlers) runnerTasksCollection(c *gin.Context) {
	if !h.requireRunnerTaskAdmin(c) {
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		tasks := h.repo.RunnerTasks()
		for i := range tasks {
			tasks[i] = domain.PublicRunnerTask(tasks[i])
			if len(tasks[i].Prompt) > 512 {
				tasks[i].Prompt = tasks[i].Prompt[:512] + "..."
			}
		}
		c.JSON(http.StatusOK, tasks)
	case http.MethodPost:
		var input struct {
			Provider    string   `json:"provider"`
			Prompt      string   `json:"prompt"`
			Workspace   string   `json:"workspace"`
			RunnerID    string   `json:"runnerId"`
			Args        []string `json:"args"`
			MaxAttempts int      `json:"maxAttempts"`
		}
		if !decode(c, &input) {
			return
		}
		input.Provider = strings.TrimSpace(input.Provider)
		if input.Provider == "" {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "provider 不能为空"})
			return
		}
		if strings.TrimSpace(input.Prompt) == "" {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "prompt 不能为空"})
			return
		}
		if input.MaxAttempts < 1 {
			input.MaxAttempts = 1
		}
		if input.MaxAttempts > 5 {
			input.MaxAttempts = 5
		}
		task := domain.RunnerTask{
			ID:          "rt-" + randomID(),
			RunnerID:    strings.TrimSpace(input.RunnerID),
			Provider:    input.Provider,
			Prompt:      input.Prompt,
			Workspace:   strings.TrimSpace(input.Workspace),
			Args:        input.Args,
			Status:      domain.RunnerTaskQueued,
			MaxAttempts: input.MaxAttempts,
		}
		if err := h.repo.PutRunnerTask(task); err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		// Re-read so the response carries the persisted timestamps.
		created, ok := h.repo.RunnerTask(task.ID)
		if !ok {
			created = task
		}
		// Wake any long-polling runner that can serve this provider.
		h.runnerWaiters.notify([]string{task.Provider})
		c.JSON(http.StatusCreated, created)
	}
}

// ---------- long-poll claim ----------

func runnerLongPollTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("AGENTFLOW_RUNNER_LONG_POLL_TIMEOUT")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 && parsed <= 5*time.Minute {
			return parsed
		}
	}
	return runnerLongPollDefault
}

func runnerDBPollInterval() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("AGENTFLOW_RUNNER_DB_POLL_INTERVAL")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 && parsed <= 30*time.Second {
			return parsed
		}
	}
	return runnerDBPollDefault
}

// 路由: g.POST("/api/runners/tasks/claim", h.runnerTaskClaim)
//
//	g.POST("/runners/tasks/claim", h.runnerTaskClaim)
func (h *runnersHandlers) runnerTaskClaim(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	var input struct {
		AvailableRuntimes []string `json:"availableRuntimes"`
		Capacity          int      `json:"capacity"`
		Preflight         bool     `json:"preflight"`
		NoWait            bool     `json:"noWait"`
	}
	if !decode(c, &input) {
		return
	}
	if len(input.AvailableRuntimes) == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "availableRuntimes 不能为空"})
		return
	}
	if input.Capacity <= 0 {
		input.Capacity = 1
	}
	claimOrSignal := func() (domain.RunnerTask, bool, error) {
		if input.Preflight {
			available, err := h.repo.RunnerTaskAvailable(runner.ID, input.AvailableRuntimes)
			return domain.RunnerTask{}, available, err
		}
		return h.repo.ClaimRunnerTask(runner.ID, input.AvailableRuntimes, time.Now().UTC(), runnerTaskLeaseTTL)
	}
	respond := func(task domain.RunnerTask) {
		if input.Preflight {
			c.JSON(http.StatusOK, map[string]bool{"taskAvailable": true})
			return
		}
		c.JSON(http.StatusOK, map[string]any{"task": task})
	}
	deadline := time.Now().Add(runnerLongPollTimeout())
	for {
		if input.Capacity <= 0 {
			c.JSON(http.StatusNoContent, nil)
			return
		}
		task, claimed, err := claimOrSignal()
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if claimed {
			respond(task)
			return
		}
		if input.NoWait {
			c.Status(http.StatusNoContent)
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.Status(http.StatusNoContent)
			return
		}
		// Register before the second claim. This closes the race where a task
		// is inserted after the first DB check but before the waiter exists.
		// In an all-in-one process the waiter is notified immediately. In the
		// production API/worker split, the worker writes the durable queue from
		// another process, so a bounded DB poll is also required.
		waiter := h.runnerWaiters.register(input.AvailableRuntimes)
		task, claimed, err = claimOrSignal()
		if err != nil {
			h.runnerWaiters.unregister(waiter)
			c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if claimed {
			h.runnerWaiters.unregister(waiter)
			respond(task)
			return
		}
		waitFor := runnerDBPollInterval()
		if remaining < waitFor {
			waitFor = remaining
		}
		wake := time.NewTimer(waitFor)
		select {
		case <-waiter.ch:
		case <-wake.C:
		case <-c.Request.Context().Done():
			h.runnerWaiters.unregister(waiter)
			wake.Stop()
			return
		}
		wake.Stop()
		h.runnerWaiters.unregister(waiter)
	}
}

// ---------- task item endpoints (session-authenticated) ----------
//
// 原 runnerTaskItem 分发器按 "{id}" / "{id}/{action}" 拆分为独立 handler，
// id 由 gin 路由参数 :id 提供。

// runnerTaskGet returns one task by id.
// 路由: g.GET("/api/runners/tasks/:id", h.runnerTaskGet)
func (h *runnersHandlers) runnerTaskGet(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	task, exists := h.repo.RunnerTask(c.Param("id"))
	if !exists || !runnerMaySeeTask(runner, task) {
		c.Status(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, domain.PublicRunnerTask(task))
}

// runnerMaySeeTask limits task reads to the runner it is targeted at or
// leased by. An untargeted, unleased task is visible to every runner because
// any of them may claim it.
func runnerMaySeeTask(runner domain.Runner, task domain.RunnerTask) bool {
	if task.LeaseOwner != "" {
		return task.LeaseOwner == runner.ID
	}
	return task.RunnerID == "" || task.RunnerID == runner.ID
}

// requireRunnerTaskAdmin guards raw task management (list/create/cancel):
// creating a task runs an arbitrary prompt on a user's machine, and the list
// exposes every project's prompts. Only platform admins may use it once
// authentication is enabled; the engine dispatches workflow tasks directly.
func (h *runnersHandlers) requireRunnerTaskAdmin(c *gin.Context) bool {
	if !authRequired() || h.isPlatformAdmin(c.Request) {
		return true
	}
	c.JSON(http.StatusForbidden, map[string]string{"error": "只有平台管理员可以直接管理执行机器任务"})
	return false
}

// runnerTaskCancel is a management action: it authenticates the interactive
// user (userAccessAuth), not the runner session.
// 路由: g.POST("/api/runners/tasks/:id/cancel", h.runnerTaskCancel)
func (h *runnersHandlers) runnerTaskCancel(c *gin.Context) {
	if !h.requireRunnerTaskAdmin(c) {
		return
	}
	taskID := c.Param("id")
	if err := h.repo.CancelRunnerTask(taskID, time.Now().UTC()); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.engine.NotifyRunnerTaskChanged(taskID)
	c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// runnerTaskEventsOut serves stored task events to management callers.
// 路由: g.GET("/api/runners/tasks/:id/events", h.runnerTaskEventsOut)
func (h *runnersHandlers) runnerTaskEventsOut(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	task, exists := h.repo.RunnerTask(c.Param("id"))
	if !exists || !runnerMaySeeTask(runner, task) {
		c.Status(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, h.repo.RunnerTaskEvents(task.ID))
}

// runnerTaskStart acknowledges the runner picked up the leased task.
// 路由: g.POST("/api/runners/tasks/:id/start", h.runnerTaskStart)
//
//	g.POST("/runners/tasks/:id/start", h.runnerTaskStart)
func (h *runnersHandlers) runnerTaskStart(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	if err := h.repo.StartRunnerTask(c.Param("id"), runner.ID, time.Now().UTC()); err != nil {
		if err == repo.ErrRunnerTaskLeaseLost {
			c.JSON(http.StatusConflict, map[string]string{"error": "task lease lost"})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// runnerTaskLeaseRenew extends the lease and reports the desired state.
// 路由: g.POST("/api/runners/tasks/:id/lease/renew", h.runnerTaskLeaseRenew)
//
//	g.POST("/runners/tasks/:id/lease/renew", h.runnerTaskLeaseRenew)
func (h *runnersHandlers) runnerTaskLeaseRenew(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	desired, err := h.repo.RenewRunnerTaskLease(c.Param("id"), runner.ID, runnerTaskLeaseTTL, time.Now().UTC())
	if err != nil {
		if err == repo.ErrRunnerTaskLeaseLost {
			c.JSON(http.StatusConflict, map[string]string{"error": "task lease lost"})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"leaseUntil": time.Now().UTC().Add(runnerTaskLeaseTTL), "desiredState": desired})
}

// runnerTaskEventsIngest accepts batched task events from the lease owner.
// 路由: g.POST("/api/runners/tasks/:id/events", h.runnerTaskEventsIngest)
//
//	g.POST("/runners/tasks/:id/events", h.runnerTaskEventsIngest)
func (h *runnersHandlers) runnerTaskEventsIngest(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	taskID := c.Param("id")
	task, exists := h.repo.RunnerTask(taskID)
	if !exists {
		c.Status(http.StatusNotFound)
		return
	}
	// Events are accepted from the lease owner; retries after a lease loss
	// are idempotent through (task_id, seq) anyway.
	if task.LeaseOwner != runner.ID || (task.Status != domain.RunnerTaskLeased && task.Status != domain.RunnerTaskRunning) {
		c.JSON(http.StatusConflict, map[string]string{"error": "task lease lost"})
		return
	}
	var input struct {
		Events []domain.RunnerTaskEvent `json:"events"`
	}
	if !decode(c, &input) {
		return
	}
	if len(input.Events) == 0 {
		c.JSON(http.StatusOK, map[string]int{"accepted": 0})
		return
	}
	if len(input.Events) > 500 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "单批事件数量不能超过 500"})
		return
	}
	for i := range input.Events {
		if input.Events[i].Type == "" {
			input.Events[i].Type = "stdout"
		}
		if input.Events[i].Ts.IsZero() {
			input.Events[i].Ts = time.Now().UTC()
		}
	}
	accepted, err := h.repo.AppendRunnerTaskEvents(taskID, input.Events)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]int{"accepted": accepted})
}

// runnerTaskComplete closes out a task with result and artifacts.
// 路由: g.POST("/api/runners/tasks/:id/complete", h.runnerTaskComplete)
//
//	g.POST("/runners/tasks/:id/complete", h.runnerTaskComplete)
func (h *runnersHandlers) runnerTaskComplete(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	var input struct {
		Status    string                    `json:"status"`
		ExitCode  *int                      `json:"exitCode"`
		Result    string                    `json:"result"`
		Error     string                    `json:"error"`
		Artifacts map[string]string         `json:"artifacts"`
		Outputs   *domain.RunnerTaskOutputs `json:"outputs"`
	}
	if !decode(c, &input) {
		return
	}
	if input.Status == "" {
		input.Status = domain.RunnerTaskSuccess
	}
	if len(input.Artifacts) > 64 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "单次最多上报 64 个产物文件"})
		return
	}
	for name, content := range input.Artifacts {
		if len(content) > 1<<20 {
			c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("产物 %s 超过 1MB 上传限制", name)})
			return
		}
	}
	if message := validateRunnerTaskOutputs(input.Outputs); message != "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": message})
		return
	}
	if err := h.repo.CompleteRunnerTask(c.Param("id"), runner.ID, input.Status, input.ExitCode, input.Result, input.Error, input.Artifacts, input.Outputs, time.Now().UTC()); err != nil {
		if err == repo.ErrRunnerTaskLeaseLost {
			c.JSON(http.StatusConflict, map[string]string{"error": "task lease lost"})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.engine.NotifyRunnerTaskChanged(c.Param("id"))
	if task, ok := h.repo.RunnerTask(c.Param("id")); ok {
		h.engine.OnRunnerTaskCompleted(task)
	}
	c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// runnerTaskControl lets the runner poll the desired state while running.
// 路由: g.GET("/api/runners/tasks/:id/control", h.runnerTaskControl)
//
//	g.GET("/runners/tasks/:id/control", h.runnerTaskControl)
func (h *runnersHandlers) runnerTaskControl(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	task, exists := h.repo.RunnerTaskControl(c.Param("id"))
	if !exists {
		c.Status(http.StatusNotFound)
		return
	}
	if task.LeaseOwner != runner.ID {
		c.JSON(http.StatusConflict, map[string]string{"error": "task lease lost"})
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"desiredState": task.DesiredState,
		"status":       task.Status,
		"leaseOwner":   task.LeaseOwner,
	})
}

// isRunnerAgentPath reports whether a path is a runner-agent endpoint that
// authenticates with bootstrap/session credentials instead of a user session.
// Used by userAccessAuth to skip the user-identity requirement. Method checks
// and session validation happen inside each handler, so an exempted path never
// exposes data without its own credential.
// validateRunnerTaskOutputs applies the per-file limits of legacy artifact
// uploads to reported outputs and rejects paths outside the task workspace.
func validateRunnerTaskOutputs(outputs *domain.RunnerTaskOutputs) string {
	if outputs == nil {
		return ""
	}
	if len(outputs.Files) > 64 {
		return "单次最多上报 64 个产物文件"
	}
	if len(outputs.Patches) > 64 {
		return "单次最多上报 64 个仓库的变更清单"
	}
	for path, content := range outputs.Files {
		if !domain.SafeWorkspaceRelPath(path) {
			return fmt.Sprintf("产物路径 %q 不合法", path)
		}
		if len(content) > 1<<20 {
			return fmt.Sprintf("产物 %s 超过 1MB 上传限制", path)
		}
	}
	for dir, patch := range outputs.Patches {
		if dir != "" && !domain.SafeWorkspaceRelPath(dir) {
			return fmt.Sprintf("仓库目录 %q 不合法", dir)
		}
		if len(patch) > 1<<20 {
			return fmt.Sprintf("仓库 %s 的变更清单超过 1MB", dir)
		}
	}
	if len(outputs.Recovered) > 64 {
		return "单次最多上报 64 条收回记录"
	}
	return ""
}

func isRunnerAgentPath(path string) bool {
	if strings.HasPrefix(path, "/runners/") {
		return true
	}
	if !strings.HasPrefix(path, "/api/runners/") {
		return false
	}
	rest := strings.Trim(strings.TrimPrefix(path, "/api/runners/"), "/")
	switch rest {
	case "register", "deregister", "disconnect", "heartbeat", "tasks/claim":
		return true
	}
	// tasks/{id} and tasks/{id}/{action}: only the runner-agent actions are
	// exempt; task creation (POST /api/runners/tasks) and cancel stay behind
	// user auth.
	if after, ok := strings.CutPrefix(rest, "tasks/"); ok && after != "" {
		parts := strings.Split(after, "/")
		if len(parts) == 1 {
			return true
		}
		switch strings.Join(parts[1:], "/") {
		case "start", "lease/renew", "events", "complete", "control":
			return true
		}
	}
	return false
}

// runnerPreviewContent accepts a runner's compressed preview upload. The
// upload token comes from the task payload; the runner session identifies the
// uploader.
func (h *runnersHandlers) runnerPreviewContent(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	svc := h.engine.Preview()
	if svc == nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "preview is not enabled"})
		return
	}
	token := strings.TrimSpace(c.GetHeader("X-Preview-Upload-Token"))
	if token == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "preview upload token required"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, svc.Config().MaxCompressed+1)
	if err := svc.AcceptUpload(c.Request.Context(), c.Param("previewId"), runner.ID, token, c.Request.Body); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func (h *runnersHandlers) runnerPreviewComplete(c *gin.Context) {
	if _, ok := h.runnerSessionAuth(c); !ok {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(c.Request.Body, 1<<20))
	c.JSON(http.StatusOK, map[string]bool{"ok": true})
}
