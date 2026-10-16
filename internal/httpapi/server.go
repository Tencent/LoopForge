package httpapi

import (
	"compress/gzip"
	"context"
	"crypto/subtle"
	"flag"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	stdsync "sync"
	"time"

	deployassets "agentflow/deploy"
	apidocs "agentflow/docs/api"
	"agentflow/internal/application"
	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/knowledge"
	"agentflow/internal/mcpserver"
	"agentflow/internal/notification"
	"agentflow/internal/observability"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/schedule"
	"agentflow/internal/tracker"
	webassets "agentflow/web"

	"github.com/gin-gonic/gin"
)

// deps 聚合所有领域 handler 共享的基础依赖与跨领域行为。
type deps struct {
	repo       *repo.Store
	engine     *engine.Engine
	sync       *tracker.Service
	schedule   *schedule.Service
	authClient *auth.Client
	api        http.Handler
}

// configureSync 是跨领域共享的依赖配置行为（mcp/issues/sync 都调用）。
func (d deps) configureSync(cfg domain.SyncConfig) {
	if d.sync == nil {
		return
	}
	d.sync.ConfigureWithStore(cfg, d.repo)
}

// getAuthClient 挂 deps（token 鉴权与 auth 流程共用）。
func (d deps) getAuthClient() *auth.Client {
	if d.authClient == nil {
		d.authClient = auth.NewClientFromEnv()
	}
	return d.authClient
}

// Server 组合各领域 handler，仅保留服务级端点与入口装配。
// 领域 handler 一律以指针持有：deps 是共享指针，schedule 服务装配后
// 对所有领域可见；需要跨领域协作的领域（issues → tasks/stream）显式注入。
type Server struct {
	*deps
	auth            *authHandlers  // auth.go
	users           *usersHandlers // users.go
	projects        *projectsHandlers
	businessDomains *businessDomainsHandlers
	catalog         *catalogHandlers           // catalog.go
	mcp             *mcpHandlers               // mcp.go
	market          *marketHandlers            // mcp_market.go
	issues          *issuesHandlers            // issues.go（依赖 tasks/stream）
	tasks           *tasksHandlers             // tasks.go
	runs            *runsHandlers              // runs.go
	previews        *previewsHandlers          // run_preview.go
	stream          *streamHandlers            // stream.go
	runners         *runnersHandlers           // runners.go
	schedule        *scheduleHandlers          // schedule.go（align.go 并入）
	tokens          *tokenHandlers             // auth_tokens.go
	syncAPI         *syncHandlers              // tracker.go
	deploy          *deploymentMetricsHandlers // deployment_metrics.go
	notifications   *notificationHandlers
	botInteractions *botInteractionHandlers
	botIdentities   *botIdentityHandlers
	know            *knowledgeHandlers // knowledge.go / knowledge_proxy.go
}

func New(s *repo.Store, e *engine.Engine, syncSvc *tracker.Service, basicUser, basicPass string) *gin.Engine {
	return NewWithNotifications(s, e, syncSvc, basicUser, basicPass, notification.New(s))
}

func NewWithNotifications(s *repo.Store, e *engine.Engine, syncSvc *tracker.Service, basicUser, basicPass string, notifications *notification.Service) *gin.Engine {
	return newWithNotifications(s, e, syncSvc, basicUser, basicPass, notifications, nil)
}

// NewWithNotificationsAndBackground ties autonomous group follow-ups to the
// caller's shutdown context. The returned stop function cancels and joins them.
func NewWithNotificationsAndBackground(ctx context.Context, s *repo.Store, e *engine.Engine, syncSvc *tracker.Service, basicUser, basicPass string, notifications *notification.Service) (*gin.Engine, func()) {
	stop := func() {}
	router := newWithNotifications(s, e, syncSvc, basicUser, basicPass, notifications, func(h *assistantHandlers, runs *runsHandlers) {
		stopFollowUps := h.startGroupFollowUps(ctx)
		stopHandoffs := runs.startSOPHandoffJobs(ctx)
		stop = func() { stopHandoffs(); stopFollowUps() }
	})
	return router, stop
}

func newWithNotifications(s *repo.Store, e *engine.Engine, syncSvc *tracker.Service, basicUser, basicPass string, notifications *notification.Service, startBackground func(*assistantHandlers, *runsHandlers)) *gin.Engine {
	d := &deps{repo: s, engine: e, sync: syncSvc, authClient: auth.NewClientFromEnv()}
	if err := ensureDefaultMCPsForAllProjects(s); err != nil {
		log.Printf("ensure default project MCPs: %v", err)
	} else {
		d.configureSync(s.SyncConfig())
	}
	tasks := &tasksHandlers{deps: d}
	stream := &streamHandlers{deps: d, clarifyCancels: map[string]context.CancelFunc{}, clarifyRepoLock: map[string]*stdsync.Mutex{}}
	stream.jobs = newClarifyHub(stream)
	stream.clarifyRun = stream.executeClarify
	if flag.Lookup("test.v") != nil {
		stream.clarifyRun = testClarifyExec
	}
	runnerControls := newRunnerControlHub()
	e.WorkspaceCheckpointTransport = func(ctx context.Context, run domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return runnerControls.requestWorkspace(ctx, e.RunExecutionRunner(run), request)
	}
	api := &Server{
		deps:            d,
		notifications:   &notificationHandlers{deps: d, service: notifications},
		auth:            &authHandlers{deps: d},
		users:           &usersHandlers{deps: d},
		catalog:         &catalogHandlers{deps: d},
		businessDomains: &businessDomainsHandlers{deps: d},
		projects:        &projectsHandlers{deps: d},
		mcp:             &mcpHandlers{deps: d},
		market:          &marketHandlers{deps: d},
		issues:          &issuesHandlers{deps: d, tasks: tasks, stream: stream},
		tasks:           tasks,
		runs:            &runsHandlers{deps: d, runnerControls: runnerControls, evalJobs: newRunEvalHub()},
		previews:        &previewsHandlers{deps: d},
		stream:          stream,
		runners:         &runnersHandlers{deps: d, runnerWaiters: newRunnerWaiterRegistry(), runnerControls: runnerControls, runnerBindCodes: map[string]runnerBindCode{}},
		schedule:        &scheduleHandlers{deps: d},
		tokens:          &tokenHandlers{deps: d},
		syncAPI:         &syncHandlers{deps: d},
		deploy:          &deploymentMetricsHandlers{deps: d},
		know:            &knowledgeHandlers{deps: d, svc: knowledge.NewService(d.repo, randomID), activity: &knowledgeActivityLog{}},
	}
	e.WorkspaceActive = func(run domain.Run) bool { return api.runs.terminals.HasRunScope(run.ID) }
	api.catalog.notifications = api.notifications
	api.schedule.notifications = api.notifications
	if syncSvc != nil {
		e.MergeIntoBranch = syncSvc.EnsureAndMergeBranchMR
		e.FindBranchMR = syncSvc.FindBranchMR
		syncSvc.OnRequirementImported = func(issue domain.Issue) {
			go stream.autoClarifyIssue(issue.ID)
		}
	}
	// Wake long-polling CLI runners the moment the engine queues a task.
	e.SetRunnerDispatchNotify(func(provider string) { api.runners.runnerWaiters.notify([]string{provider}) })
	// Push durable task controls (cancel) to connected runners over WebSocket.
	e.SetRunnerControlNotify(api.runners.runnerControls.sendControl)
	sched := schedule.New(s, syncSvc, api.schedule.dispatchScheduled)
	sched.SetBaselineGuard(api.schedule.guardScheduledBaseline)
	// deps 由所有领域共享，schedule 服务就绪后回填一次即全局可见。
	d.schedule = sched
	if schedulerEnabled() {
		d.schedule.Start()
	}
	api.runners.startRunnerSweeper()
	stream.recoverClarifyJobs()
	g := gin.New()
	g.HandleMethodNotAllowed = true
	// master(net/http) 对带尾斜杠的 /api 子树路径按各自 handler 语义返回
	// 405/404；gin 默认会 301 重定向到无斜杠路径，行为不同，关闭之。
	g.RedirectTrailingSlash = false
	g.Use(api.previews.previewHost())
	// gin 只有在显式注册 NoMethod 后才会对"路径匹配但方法不符"返回 405，
	// 否则落入 404；这里同时把 405 响应统一成 JSON 形态。
	g.NoMethod(func(c *gin.Context) {
		// gin 的 :id 通配路由会把 "/api/repositories/"（尾斜杠）匹配成
		// "方法不符"；master(net/http) 对这类请求按子树 handler 的空 id
		// 语义返回 404/405，这里先走兼容层再落 405。
		if masterTrailingSlashCompat(c, api) {
			return
		}
		c.JSON(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	})
	g.Use(logging(), securityHeaders(), cors(), gzipMaybe())
	g.POST("/api/runs/:id/group/mcp", api.runs.groupMCP)
	g.Use(api.tokens.userAccessAuth())
	if basicUser != "" && basicPass != "" {
		g.Use(basicAuth(basicUser, basicPass))
	}
	g.Use(d.runWriteGuard(), d.assetWriteGuard())

	g.GET("/api/notification-bots", api.notifications.bots)
	g.POST("/api/notification-bots", api.notifications.bots)
	g.PUT("/api/notification-bots/:id", api.notifications.bot)
	g.DELETE("/api/notification-bots/:id", api.notifications.bot)
	g.GET("/api/notification-bots/:id/sessions", api.notifications.sessions)
	g.POST("/api/notification-bots/:id/test", api.notifications.test)
	g.POST("/api/notification-preview", api.notifications.preview)
	g.GET("/api/notification-deliveries", api.notifications.deliveries)
	g.POST("/api/notification-deliveries/:deliveryId/retry", api.notifications.retry)

	// --- auth & users ---
	g.GET("/api/health", api.health)
	g.GET("/api/auth/current", api.auth.authCurrent)
	g.POST("/api/auth/password", api.auth.authPassword)
	g.GET("/api/auth/setup", api.auth.authSetup)
	g.POST("/api/auth/register", api.auth.authRegister)
	g.GET("/api/auth/local", api.auth.authLocal)
	g.POST("/api/auth/local", api.auth.authLocal)
	g.GET("/api/auth/exchange", api.auth.authExchange)
	g.POST("/api/auth/exchange", api.auth.authExchange)
	g.POST("/api/auth/logout", api.auth.authLogout)
	g.GET("/api/auth/photo", api.auth.authPhoto)
	g.GET("/api/auth/photo/:spec/:username", api.auth.authPhoto)
	g.GET("/api/auth/tokens", api.tokens.authTokens)
	g.POST("/api/auth/tokens", api.tokens.authTokens)
	g.DELETE("/api/auth/tokens/:id", api.tokens.authToken)
	g.GET("/api/users", api.users.users)
	g.GET("/api/platform/admins", api.users.platformAdmins)
	g.PUT("/api/platform/admins", api.users.platformAdmins)
	// master(net/http) 的 /api/users/ 子树只接受 PUT /api/users/<id>/credentials
	// 一种形态，其余任何方法/子路径都走 method(w) → 405 JSON。
	g.Any("/api/users/:id/credentials", func(c *gin.Context) {
		if c.Request.Method != http.MethodPut {
			c.JSON(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		api.users.user(c)
	})
	// 工蜂 Git Token 校验：只允许当前登录用户校验自己的令牌。
	g.GET("/api/users/:id/git-token/verify", api.users.verifyGitToken)
	g.Any("/api/users/:id", func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	})

	api.botIdentities = &botIdentityHandlers{deps: d, publicURL: api.notifications.service.PublicURL}
	asst := &assistantHandlers{deps: d, sourceRoot: assistantSourceRoot(), notifications: api.notifications, identities: api.botIdentities}
	api.runs.assistant = asst
	api.notifications.service.GenerateReport = asst.generateSOPReport
	if api.notifications.service.Interactions != nil {
		api.notifications.service.Interactions.Generate = asst.generateBotReply
		api.notifications.service.Interactions.Action = asst.handleBotCardAction
	}
	api.botInteractions = &botInteractionHandlers{deps: d, notifications: api.notifications}
	g.GET("/api/notification-bots/:id/interactions", api.botInteractions.records)
	g.POST("/api/bot-bindings/claim", api.botIdentities.binding)
	g.POST("/api/bot-bindings/status", api.botIdentities.binding)
	g.GET("/api/bot-bindings", api.botIdentities.list)
	g.DELETE("/api/bot-bindings/:id", api.botIdentities.remove)
	g.POST("/api/assistant/chat", asst.chat)
	g.POST("/api/assistant/generations", asst.startGeneration)
	g.GET("/api/assistant/sessions/:id/generation", asst.generation)
	g.POST("/api/assistant/sessions/:id/generation/cancel", asst.generation)
	g.GET("/api/assistant/models", asst.models)
	g.POST("/api/assistant/attachments", asst.uploadAttachment)
	g.GET("/api/assistant/attachments/:id", asst.downloadAttachment)
	g.GET("/api/assistant/references", asst.workbenchReferences)
	g.GET("/api/runs/:id/conversation", asst.runConversation)
	g.POST("/api/task-groups", api.runs.taskGroups)
	g.POST("/api/runs/:id/group/mode", api.runs.groupControl)
	g.POST("/api/runs/:id/group/members", api.runs.groupControl)
	g.POST("/api/runs/:id/group/sop", api.runs.groupControl)
	g.GET("/api/runs/:id/group/sop/handoff", api.runs.sopHandoffJob)
	g.POST("/api/runs/:id/group/sop/handoff/retry", api.runs.retrySOPHandoffJob)
	g.GET("/api/runs/group/sop/handoffs", api.runs.sopHandoffJobs)
	g.POST("/api/assistant/sessions/:id/edit", asst.editSession)
	g.POST("/api/runs/:id/group/tasks", api.runs.groupControl)
	g.GET("/api/runs/:id/group/conversation", api.runs.groupConversation)
	g.GET("/api/runs/:id/group/activity", api.runs.groupActivity)
	g.GET("/api/runs/:id/group/followup", asst.groupFollowUpStatus)
	g.GET("/api/assistant/sessions", asst.listSessions)
	g.POST("/api/assistant/sessions/:id/archive", asst.archiveSession)
	g.POST("/api/assistant/sessions/:id/fork", asst.forkSession)
	g.GET("/api/assistant/sessions/:id", asst.session)
	g.PUT("/api/assistant/sessions/:id", asst.session)
	g.DELETE("/api/assistant/sessions/:id", asst.session)
	g.GET("/api/assistant/tasks", asst.listTasks)
	g.PUT("/api/assistant/tasks", asst.putTask)
	g.DELETE("/api/assistant/tasks/:id", asst.deleteTask)
	g.POST("/api/assistant/tasks/:id/run", asst.runTaskNow)
	prefetchAssistantModels()

	// --- catalog ---
	g.GET("/api/capabilities", api.capabilities)
	g.GET("/api/executor-models", api.catalog.executorModels)
	g.GET("/api/openapi.yaml", api.openAPI)
	g.GET("/api/bootstrap", api.bootstrap)
	g.GET("/api/business-domains", api.businessDomains.domains)
	g.PUT("/api/business-domains", api.businessDomains.domains)
	g.GET("/api/business-domain-report", api.businessDomains.report)
	g.PUT("/api/projects/:id/business-domain", api.projects.businessDomainAssignment)
	g.GET("/api/projects/:id/business-domain/history", api.projects.businessDomainAssignment)
	g.GET("/api/projects", api.projects.projects)
	g.PUT("/api/projects", api.projects.projects)
	g.POST("/api/projects", api.projects.projects)
	g.GET("/api/projects/:id", api.projects.project)
	g.GET("/api/repositories", api.catalog.repositories)
	g.POST("/api/repositories", api.catalog.repositories)
	g.PUT("/api/repositories", api.catalog.repositories)
	g.DELETE("/api/repositories", api.catalog.repositories)
	g.GET("/api/repositories/:id", api.catalog.repository)
	g.GET("/api/issues", api.issues.issues)
	g.PUT("/api/issues", api.issues.issues)
	g.POST("/api/issues", api.issues.issues)
	g.POST("/api/issues/import-tapd", api.issues.importTAPD)
	// master(net/http) 的 /api/issues/<id> 子树对任意方法先做存在性检查
	//（404 JSON），再按 (子路径, method) 分派，不匹配的组合兜底 404 text。
	// gin 用 Any 注册 + handler 内方法分派复刻该语义。
	g.Any("/api/issues/:id", api.issues.issue)
	g.Any("/api/issues/:id/comments", api.issues.issue)
	g.Any("/api/issues/:id/runs", api.issues.issue)
	g.Any("/api/issues/:id/clarify", api.issues.issue)
	g.Any("/api/issues/:id/clarification", api.issues.issue)
	g.Any("/api/issues/:id/chat-clarify", api.issues.issue)
	g.Any("/api/issues/:id/chat-clarify/cancel", api.issues.issue)
	g.Any("/api/issues/:id/reopen", api.issues.issue)
	g.Any("/api/issues/:id/close", api.issues.issue)
	g.Any("/api/issues/:id/resync-tapd", api.issues.issue)
	g.Any("/api/issues/:id/requirement", api.issues.issue)
	g.Any("/api/issues/:id/sops", api.issues.issue)
	g.Any("/api/issues/:id/dispatch", api.issues.issue)
	rooms := &roomsHandlers{deps: d}
	g.GET("/api/rooms", rooms.rooms)
	g.POST("/api/rooms", rooms.rooms)
	g.GET("/api/rooms/:id", rooms.room)
	g.GET("/api/rooms/:id/messages", rooms.messages)
	g.POST("/api/rooms/:id/messages", rooms.messages)
	g.POST("/api/rooms/:id/issues", rooms.createIssue)
	g.POST("/api/rooms/:id/decisions", rooms.decide)
	g.POST("/api/chat-clarify", api.stream.chatClarify)
	g.GET("/api/chat-clarify", api.stream.attachChatClarify)
	g.POST("/api/chat-clarify/cancel", api.stream.chatClarifyCancel)
	g.GET("/api/tasks", api.tasks.tasks)
	g.GET("/api/tasks/:id", api.tasks.task)
	g.GET("/api/agents", api.catalog.agents)
	g.PUT("/api/agents", api.catalog.agents)
	g.GET("/api/agents/:id", api.catalog.agent)
	g.DELETE("/api/agents/:id", api.catalog.agent)
	g.GET("/api/agents/:id/revisions", api.catalog.agent)
	g.GET("/api/agents/:id/revisions/:rev", api.catalog.agent)
	g.POST("/api/agents/:id/revisions/:rev/restore", api.catalog.agent)
	g.POST("/api/agents/:id/clone", api.catalog.agent)
	g.GET("/api/rules", api.catalog.rules)
	g.PUT("/api/rules", api.catalog.rules)
	g.DELETE("/api/rules", api.catalog.rules)
	g.GET("/api/skills", api.catalog.skills)
	g.PUT("/api/skills", api.catalog.skills)
	g.DELETE("/api/skills", api.catalog.skills)
	g.POST("/api/skills/:id/files", api.catalog.skillFiles)
	g.GET("/api/skills/:id/files/*path", api.catalog.skillFile)
	g.DELETE("/api/skills/:id/files/*path", api.catalog.skillFile)
	g.GET("/api/mcp-servers", api.mcp.mcpServers)
	g.PUT("/api/mcp-servers", api.mcp.mcpServers)
	g.DELETE("/api/mcp-servers", api.mcp.mcpServers)
	g.POST("/api/mcp-servers/import", api.mcp.mcpImport)
	g.POST("/api/mcp-servers/:id/files", api.mcp.mcpFiles)
	g.GET("/api/mcp-servers/:id/files/*path", api.mcp.mcpFile)
	g.DELETE("/api/mcp-servers/:id/files/*path", api.mcp.mcpFile)
	g.POST("/api/mcp-servers/custom", api.market.mcpCustomInstall)
	g.GET("/api/mcp-market", api.market.mcpMarket)
	g.POST("/api/mcp-market/install", api.market.mcpMarketInstall)
	g.GET("/api/executors", api.catalog.executors)
	g.GET("/api/dockerfiles", api.catalog.dockerfiles)
	g.PUT("/api/dockerfiles", api.catalog.dockerfiles)
	g.DELETE("/api/dockerfiles", api.catalog.dockerfiles)
	g.GET("/api/environments", api.catalog.environments)
	g.PUT("/api/environments", api.catalog.environments)
	g.DELETE("/api/environments", api.catalog.environments)
	g.GET("/api/sandboxes", api.catalog.sandboxes)
	g.PUT("/api/sandboxes", api.catalog.sandboxes)
	g.DELETE("/api/sandboxes", api.catalog.sandboxes)
	g.GET("/api/workflows", api.catalog.workflows)
	g.PUT("/api/workflows", api.catalog.workflows)
	g.GET("/api/workflows/:id", api.catalog.workflow)
	g.DELETE("/api/workflows/:id", api.catalog.workflow)
	g.GET("/api/workflows/:id/revisions", api.catalog.workflow)
	g.GET("/api/workflows/:id/revisions/:rev", api.catalog.workflow)
	g.POST("/api/workflows/:id/revisions/:rev/restore", api.catalog.workflow)
	g.POST("/api/workflows/:id/publish", api.catalog.workflow)
	g.GET("/api/workflows/:id/agents", api.catalog.workflow)
	g.PUT("/api/workflows/:id/agents", api.catalog.workflow)
	g.POST("/api/workflows/:id/clone", api.catalog.workflow)
	g.GET("/api/workflow-metrics", api.runs.workflowMetrics)
	g.GET("/api/analytics", api.runs.analytics)
	g.GET("/api/observability/spec", api.observabilitySpec)
	g.GET("/api/deployment-metrics", api.deploy.deploymentMetrics)
	g.POST("/api/otel/*path", api.otel)

	// --- knowledge base ---
	g.GET("/api/knowledge/settings", api.know.settings)
	g.PUT("/api/knowledge/settings", api.know.settings)
	g.POST("/api/knowledge/check", api.know.check)
	g.GET("/api/knowledge/sources", api.know.sources)
	g.POST("/api/knowledge/search", api.know.search)
	g.GET("/api/knowledge/document", api.know.document)
	g.GET("/api/knowledge/usage", api.know.usage)
	g.GET("/api/knowledge/candidates", api.know.candidates)
	g.GET("/api/knowledge/submissions", api.know.submissions)
	g.POST("/api/knowledge/submissions", api.know.submissions)
	g.GET("/api/knowledge/submissions/:id", api.know.submission)
	g.PUT("/api/knowledge/submissions/:id", api.know.submission)
	g.DELETE("/api/knowledge/submissions/:id", api.know.submission)
	g.POST("/api/knowledge/submissions/:id/publish", api.know.publish)
	g.POST("/api/knowledge/submissions/:id/reject", api.know.reject)

	// --- runs ---
	g.GET("/api/runs", api.runs.runs)
	g.POST("/api/runs", api.runs.runs)
	g.GET("/api/runs/:id", api.runs.run)
	g.DELETE("/api/runs/:id", api.runs.run)
	g.GET("/api/runs/:id/telemetry", api.runs.run)
	g.GET("/api/runs/:id/logs", api.runs.run)
	g.GET("/api/runs/:id/logs.txt", api.runs.run)
	g.POST("/api/runs/:id/workspace/takeover", api.runs.workspaceTakeover)
	g.POST("/api/runs/:id/workspace/release", api.runs.workspaceRelease)
	g.POST("/api/runs/:id/workspace/restore", api.runs.workspaceRestore)
	g.GET("/api/runs/:id/workspace/preparation", api.runs.workspacePreparation)
	g.POST("/api/runs/:id/workspace/preparation", api.runs.workspacePreparation)
	g.POST("/api/runs/:id/workspace/use", api.runs.workspaceActivity)
	g.PUT("/api/runs/:id/workspace/file", api.runs.workspaceWrite)
	g.POST("/api/runs/:id/workspace/terminal", api.runs.workspaceTerminal)
	g.GET("/api/runs/:id/workspace", api.runs.runWorkspace)
	g.GET("/api/runs/:id/workspace/tree", api.runs.runWorkspace)
	g.GET("/api/runs/:id/workspace/file", api.runs.runWorkspace)
	g.GET("/api/runs/:id/workspace/log", api.runs.runWorkspace)
	g.GET("/api/runs/:id/workspace/commit", api.runs.runWorkspace)
	g.GET("/api/runs/:id/workspace/diff", api.runs.runWorkspace)
	g.GET("/api/runs/:id/workspace/search", api.runs.runWorkspace)
	g.POST("/api/runs/:id/signals", api.runs.run)
	g.GET("/api/runs/:id/artifacts/:name", api.runs.run)
	g.POST("/api/runs/:id/approve", api.runs.run)
	g.POST("/api/runs/:id/reject", api.runs.run)
	g.POST("/api/runs/:id/retry", api.runs.run)
	g.POST("/api/runs/:id/decline-retry", api.runs.run)
	g.POST("/api/runs/:id/cancel", api.runs.run)
	g.POST("/api/runs/:id/pause", api.runs.run)
	g.POST("/api/runs/:id/resume", api.runs.run)
	g.POST("/api/runs/:id/refresh-sop", api.runs.run)
	g.POST("/api/runs/:id/pass", api.runs.run)
	g.POST("/api/runs/:id/eval", api.runs.startRunEval)
	g.GET("/api/runs/:id/eval", api.runs.getRunEval)
	g.GET("/api/runs/:id/evals", api.runs.listRunEvals)
	g.GET("/api/runs/:id/eval/stream", api.runs.streamRunEval)
	g.POST("/api/runs/:id/eval/apply", api.runs.applyRunEval)
	g.GET("/api/runs/:id/preview", api.previews.runPreview)
	g.PUT("/api/runs/:id/preview/backend", api.previews.updatePreviewBackend)
	g.POST("/api/runs/:id/preview/session", api.previews.runPreviewSession)
	g.DELETE("/api/runs/:id/preview/session/:sid", api.previews.deletePreviewSession)
	g.DELETE("/api/runs/:id/preview", api.previews.deletePreview)
	g.GET("/api/runs/:id/stream", api.stream.streamRun)

	// --- sync & schedule ---
	g.GET("/api/sync/config", api.syncAPI.syncConfig)
	g.PUT("/api/sync/config", api.syncAPI.syncConfig)
	g.POST("/api/sync/config", api.syncAPI.syncConfig)
	g.POST("/api/sync/now", api.syncAPI.syncNow)
	g.GET("/api/sync/logs", api.syncAPI.syncLogs)
	g.GET("/api/sync/status", api.syncAPI.syncStatus)
	g.POST("/api/sync/create", api.syncAPI.syncCreate)
	g.GET("/api/schedule-jobs", api.schedule.scheduleJobs)
	g.PUT("/api/schedule-jobs", api.schedule.scheduleJobs)
	g.POST("/api/schedule-jobs", api.schedule.scheduleJobs)
	// master 子树语义：非 DELETE（或 DELETE 之外的方法）落到 handler 末尾
	// http.NotFound（404 text），由 handler 内方法分派实现。
	g.Any("/api/schedule-jobs/:id", api.schedule.scheduleJob)
	g.POST("/api/schedule-jobs/:id/run", api.schedule.scheduleJobRun)

	// --- runner management plane ---
	g.GET("/api/runners", api.runners.runners)
	g.POST("/api/runners/register", api.runners.runnerRegister)
	g.POST("/api/runners/deregister", api.runners.runnerDeregister)
	g.POST("/api/runners/disconnect", api.runners.runnerDisconnect)
	g.POST("/api/runners/bind-codes", api.runners.issueRunnerBindCode)
	g.GET("/api/runners/default", api.runners.runnerDefault)
	g.PUT("/api/runners/default", api.runners.runnerDefaultSet)
	g.DELETE("/api/runners/:id", api.runners.runnerRemove)
	g.PUT("/api/runners/:id/run-limit", api.runners.runnerRunLimitSet)
	g.POST("/api/runners/heartbeat", api.runners.runnerHeartbeat)
	g.GET("/api/runners/tasks", api.runners.runnerTasksCollection)
	g.POST("/api/runners/tasks", api.runners.runnerTasksCollection)
	g.POST("/api/runners/tasks/claim", api.runners.runnerTaskClaim)
	g.GET("/api/runners/tasks/:id", api.runners.runnerTaskGet)
	g.POST("/api/runners/tasks/:id/cancel", api.runners.runnerTaskCancel)
	g.POST("/api/runners/tasks/:id/start", api.runners.runnerTaskStart)
	g.POST("/api/runners/tasks/:id/lease/renew", api.runners.runnerTaskLeaseRenew)
	g.GET("/api/runners/tasks/:id/events", api.runners.runnerTaskEventsOut)
	g.POST("/api/runners/tasks/:id/events", api.runners.runnerTaskEventsIngest)
	g.POST("/api/runners/tasks/:id/complete", api.runners.runnerTaskComplete)
	g.GET("/api/runners/tasks/:id/control", api.runners.runnerTaskControl)
	g.PUT("/api/runners/runtimes", api.runners.runnerRuntimeStatus)
	g.POST("/api/runners/runtimes", api.runners.runnerRuntimeStatus)

	// Machine plane: this prefix is safe to exempt from gateway interactive
	// login. Public install assets are read-only; daemon APIs authenticate with
	// one-time bind codes or runner session tokens instead of Taihu identity.
	g.GET("/runners/install.sh", serveBindCLIScript)
	g.GET("/runners/agentflow-runner", serveRunnerBinary)
	g.GET("/runners/agentflow-gate-mcp", serveGateMCPBinary)
	g.GET("/runners/health", api.health)
	g.GET("/runners/connect", api.runners.runnerControlConnect)
	g.POST("/runners/register", api.runners.runnerRegister)
	g.POST("/runners/deregister", api.runners.runnerDeregister)
	g.POST("/runners/disconnect", api.runners.runnerDisconnect)
	g.POST("/runners/heartbeat", api.runners.runnerHeartbeat)
	g.POST("/runners/tasks/claim", api.runners.runnerTaskClaim)
	g.POST("/runners/tasks/:id/start", api.runners.runnerTaskStart)
	g.POST("/runners/tasks/:id/lease/renew", api.runners.runnerTaskLeaseRenew)
	g.POST("/runners/tasks/:id/events", api.runners.runnerTaskEventsIngest)
	g.POST("/runners/tasks/:id/complete", api.runners.runnerTaskComplete)
	g.GET("/runners/tasks/:id/control", api.runners.runnerTaskControl)
	g.Any(knowledge.ProxyPath, api.know.proxyMCP)
	g.PUT("/runners/previews/:previewId/content", api.runners.runnerPreviewContent)
	g.POST("/runners/previews/:previewId/complete", api.runners.runnerPreviewComplete)
	g.GET("/deploy/bind-cli.sh", serveBindCLIScript)
	g.GET("/deploy/agentflow-runner", serveRunnerBinary)
	g.GET("/deploy/agentflow-gate-mcp", serveGateMCPBinary)
	g.Any("/mcp", gin.WrapH(mcpserver.New(mcpserver.LocalInvoker{Handler: g}).HTTPHandler()))
	g.POST("/internal/workspace-checkpoint", func(c *gin.Context) { e.HandleWorkspaceCheckpoint(c.Writer, c.Request) })
	g.POST("/internal/agent-tools/:token/mcp", api.handleAgentToolMCP)
	g.DELETE("/internal/agent-tools/:token/mcp", api.handleAgentToolMCP)
	asst.api = g
	d.api = g
	if d.schedule != nil {
		d.schedule.SetAssistantPrep(asst.prepareScheduleJob)
	}
	static, _ := fs.Sub(webassets.Files, ".")
	g.NoRoute(func(c *gin.Context) {
		// master(net/http ServeMux 前缀挂载) 时代每个实体同时注册了
		// "/api/<entity>" 与 "/api/<entity>/" 两个模式，子树 handler 对
		// 空/多段 id 的返回不一致（405 JSON / 404 text / 401 JSON）。gin
		// 的精确路由会把这类请求全部交给 NoRoute，这里按 master 的
		// handler 语义复刻，保证行为等价。
		if masterTrailingSlashCompat(c, api) {
			return
		}
		// gin 预置 NoRoute 状态码为 404；studioFileServer 的 index 分支只写
		// body 不显式 WriteHeader，会继承该 404。先显式置 200，静态文件
		// 缺失时 FileServer 仍会以显式 WriteHeader(404) 覆盖。
		c.Status(http.StatusOK)
		studioFileServer(static).ServeHTTP(c.Writer, c.Request)
	})
	if startBackground != nil {
		startBackground(asst, api.runs)
	}
	return g
}

func schedulerEnabled() bool {
	raw := strings.TrimSpace(os.Getenv("AGENTFLOW_SCHEDULER_ENABLED"))
	if raw == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	return err != nil || enabled
}

// serveBindCLIScript serves the machine-facing bind-cli installer so onboarding
// is a single copy-pasted curl command (个人 → 执行器 页面).
func serveBindCLIScript(c *gin.Context) {
	script := deployassets.BindCLIScript()
	if len(script) == 0 {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Type", "text/x-shellscript; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	_, _ = c.Writer.Write(script)
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, map[string]any{"status": "ok", "role": "api"})
}

func (s *Server) capabilities(c *gin.Context) {
	c.JSON(http.StatusOK, map[string]any{
		"operations": application.PublicOperations(),
		"mcp": map[string]any{
			"streamableHttp": "/mcp",
			"stdioCommand":   "agentflow-mcp",
		},
	})
}

func (s *Server) openAPI(c *gin.Context) {
	c.Header("Content-Type", "application/yaml; charset=utf-8")
	c.Header("Cache-Control", "public, max-age=300")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(apidocs.OpenAPI)
}

func (s *Server) bootstrap(c *gin.Context) {
	var projects []domain.Project
	var repositories []domain.Repository
	var agents []domain.AgentDefinition
	var rules []domain.RuleDefinition
	var skills []domain.SkillDefinition
	var mcpServers []domain.MCPServerDefinition
	var executors []domain.ExecutorDefinition
	var dockerfiles []domain.DockerfileDefinition
	var environments []domain.EnvironmentDefinition
	var sandboxes []domain.SandboxProfile
	var workflows []domain.WorkflowDefinition
	var syncConfig domain.SyncConfig
	var scheduleJobs []domain.ScheduleJob
	var users []domain.User
	var load stdsync.WaitGroup
	load.Add(14)
	go func() { defer load.Done(); projects = s.repo.Projects() }()
	go func() { defer load.Done(); repositories = s.repo.Repositories() }()
	go func() { defer load.Done(); agents = s.repo.Agents() }()
	go func() { defer load.Done(); rules = s.repo.Rules() }()
	go func() { defer load.Done(); skills = s.repo.Skills() }()
	go func() { defer load.Done(); mcpServers = s.repo.MCPServers() }()
	go func() { defer load.Done(); executors = repo.BuiltInExecutors() }()
	go func() { defer load.Done(); dockerfiles = s.repo.Dockerfiles() }()
	go func() { defer load.Done(); environments = s.repo.Environments() }()
	go func() { defer load.Done(); sandboxes = s.repo.Sandboxes() }()
	go func() { defer load.Done(); workflows = s.repo.Workflows() }()
	go func() { defer load.Done(); syncConfig = s.repo.SyncConfig() }()
	go func() { defer load.Done(); scheduleJobs = s.repo.ScheduleJobs() }()
	go func() { defer load.Done(); users = s.repo.Users() }()
	load.Wait()
	businessDomains, err := s.repo.BusinessDomains()
	if err != nil {
		respond(c, err)
		return
	}
	var currentUserID int64
	if current, ok := s.requestUser(c.Request, true); ok {
		currentUserID = current.ID
	}
	mcpServers = domain.PublicMCPServers(withMarketIcons(visibleMCPServersForUser(mcpServers, currentUserID)))
	c.JSON(200, map[string]any{
		"businessDomains": businessDomains, "projects": domain.PublicProjects(projects), "repositories": domain.PublicRepositories(repositories),
		"agents": catalogAgents(agents), "rules": rules, "skills": skills, "mcpServers": mcpServers,
		"executors": executors, "dockerfiles": dockerfiles, "environments": environments, "sandboxes": sandboxes,
		"workflows": workflows, "syncConfig": syncConfig, "scheduleJobs": scheduleJobs, "schedulerEnabled": schedulerEnabled(),
		"users":        domain.PublicUsers(users),
		"storage":      s.repo.StorageInfo(),
		"capabilities": map[string]bool{"docker": runtime.DockerReady()},
	})
}

func (s *Server) observabilitySpec(c *gin.Context) {
	c.JSON(200, runtime.ObservabilitySpec())
}

func (s *Server) otel(c *gin.Context) {
	if !strings.HasSuffix(strings.TrimRight(c.Param("path"), "/"), "/v1/traces") {
		http.NotFound(c.Writer, c.Request)
		return
	}
	if !isLoopbackAddr(c.Request.RemoteAddr) {
		c.JSON(http.StatusForbidden, map[string]string{"error": "otel collector is loopback-only"})
		return
	}
	body := io.Reader(c.Request.Body)
	if strings.EqualFold(c.GetHeader("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(c.Request.Body)
		if err != nil {
			c.JSON(400, map[string]string{"error": "invalid gzip body"})
			return
		}
		defer gz.Close()
		body = gz
	}
	raw, err := io.ReadAll(io.LimitReader(body, 16<<20))
	if err != nil {
		c.JSON(400, map[string]string{"error": "read otlp body"})
		return
	}
	accepted := s.engine.IngestOTLPTraces(observability.ParseOTLPTraces(raw))
	_ = accepted
	c.JSON(200, map[string]any{})
}

func isLoopbackAddr(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func catalogAgents(agents []domain.AgentDefinition) []domain.AgentDefinition {
	out := make([]domain.AgentDefinition, len(agents))
	for i, agent := range agents {
		out[i] = domain.AgentDefinition{
			ID: agent.ID, Name: agent.Name, Version: agent.Version, ProjectID: agent.ProjectID, Shared: agent.Shared,
			Description:     clipRunText(agent.Description, 160),
			ExecutorID:      agent.ExecutorID,
			Model:           agent.Model,
			Effort:          agent.Effort,
			Skills:          agent.Skills,
			Rules:           agent.Rules,
			MCPServers:      agent.MCPServers,
			PlatformMCP:     agent.PlatformMCP,
			KnowledgeBase:   agent.KnowledgeBase,
			ArtifactOutputs: agent.ArtifactOutputs,
			Verification:    agent.Verification,
			Loop:            domain.AgentLoopPolicy{Enabled: agent.Loop.Enabled, MaxIterations: agent.Loop.MaxIterations, OnExhausted: agent.Loop.OnExhausted},
			UpdatedAt:       agent.UpdatedAt,
		}
	}
	return out
}

// logging 记录每个 HTTP 请求（method/path/status/bytes/耗时），用于排查上传等问题。
func logging() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Printf("[HTTP] %s %s -> %d (%d bytes, %s)",
			c.Request.Method, redactAgentToolPath(c.Request.URL.Path),
			c.Writer.Status(), c.Writer.Size(), time.Since(start).Truncate(time.Millisecond))
	}
}

// basicAuth 挂 runner 外网访问的 Basic 鉴权。
func basicAuth(user, pass string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// The runner machine plane has its own bind/session authentication and
		// must remain usable behind a gateway login or preview Basic Auth.
		if strings.HasPrefix(c.Request.URL.Path, "/runners/") || strings.HasPrefix(c.Request.URL.Path, "/internal/agent-tools/") {
			c.Next()
			return
		}
		gotUser, gotPass, ok := c.Request.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(gotUser), []byte(user)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(gotPass), []byte(pass)) == 1
		if !ok || !userOK || !passOK {
			if strings.HasPrefix(c.Request.URL.Path, "/api/otel/") && isLoopbackAddr(c.Request.RemoteAddr) {
				c.Next()
				return
			}
			c.Header("WWW-Authenticate", `Basic realm="AgentFlow"`)
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			c.Abort()
			return
		}
		c.Next()
	}
}
