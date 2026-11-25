package httpapi

import (
	"agentflow/internal/config"
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/runtime"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type assistantHelperExecutionKey struct{}
type assistantHelperExecution struct {
	h         *assistantHandlers
	owner     int64
	projectID string
	target    domain.AssistantExecutionTarget
}

func (h *assistantHandlers) helperExecutionContext(ctx context.Context, owner int64, projectID string, target *domain.AssistantExecutionTarget) context.Context {
	if target == nil || target.RunnerID == "" {
		return ctx
	}
	return context.WithValue(ctx, assistantHelperExecutionKey{}, assistantHelperExecution{
		h: h, owner: owner, projectID: projectID, target: *target,
	})
}

func helperCLIExecution(ctx context.Context, root string, actorID int64, engine string) (func(context.Context, string, runtime.CommandSpec, runtime.RunCredentials, runtime.EventSink) (string, error), bool) {
	bound, ok := ctx.Value(assistantHelperExecutionKey{}).(assistantHelperExecution)
	if !ok {
		return nil, false
	}
	s := &AgentToolSession{
		ChatOwnerID: bound.owner, ChatSessionID: "helper-" + newAssistantSessionID(),
		UserID: actorID, ProjectID: bound.projectID, ExecutionRunnerID: bound.target.RunnerID,
		RuntimeMode: domain.AssistantModePlatform,
	}
	s.runnerLogin = bound.h.assistantUsesRunnerLogin(actorID, &bound.target, engine)
	return bound.h.conversationExecutor(s, root), s.runnerLogin
}

func (h *assistantHandlers) conversationRunner(s *AgentToolSession, checkAccess bool) (domain.Runner, error) {
	if h.runnerControls == nil {
		return domain.Runner{}, errAssistantExecutionUnavailable
	}
	runner, ok := h.repo.Runner(s.ExecutionRunnerID)
	if !ok {
		return domain.Runner{}, errAssistantExecutionUnavailable
	}
	if checkAccess && !h.canUseBoundRunner(runner, s.ProjectID, s.UserID) {
		return domain.Runner{}, errAssistantRunnerAccessRevoked
	}
	if runner.Status != domain.RunnerStatusOnline {
		return domain.Runner{}, errAssistantExecutionUnavailable
	}
	if !runner.SupportsConversationCLI() {
		return domain.Runner{}, fmt.Errorf("执行机器需升级至 %s", domain.RunnerVersionConversationCLI)
	}
	if (s.runnerLogin || s.transient) && !runner.SupportsCLISettings() {
		return domain.Runner{}, fmt.Errorf("执行机器需升级至 %s 才能使用对话本机登录和 CLI 测试", domain.RunnerVersionCLISettings)
	}
	return runner, nil
}

func conversationNotStarted(err error, workspaceSetup bool) error {
	if workspaceSetup {
		err = &runtime.ConversationWorkspaceSetupError{Cause: err}
	}
	return fmt.Errorf("%w: %w", runtime.ErrConversationNotStarted, err)
}

// Protocol translation stays on the API process; the bound machine owns the
// CLI, its descendants and its durable native HOME in both execution modes.
func (h *assistantHandlers) conversationExecutor(s *AgentToolSession, root string) func(context.Context, string, runtime.CommandSpec, runtime.RunCredentials, runtime.EventSink) (string, error) {
	return func(ctx context.Context, _ string, command runtime.CommandSpec, credentials runtime.RunCredentials, sink runtime.EventSink) (output string, err error) {
		s.nativePhaseClosed.Store(false)
		phase := nativeApprovalPhase(s.Token)
		generation := ""
		if s.ctx != nil {
			generation = assistantControlScopeFrom(s.ctx).GenerationID
		}
		defer func() {
			s.nativePhaseClosed.Store(true)
			if h.repo != nil && generation != "" {
				_ = h.repo.ReleaseNativeApprovals(s.ChatOwnerID, s.ChatSessionID, generation, phase)
			}
		}()
		preparing := true
		preparingWorkspace := false
		defer func() {
			if err != nil && preparing {
				err = conversationNotStarted(err, preparingWorkspace)
			}
		}()
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d/%s", s.ChatOwnerID, s.ChatSessionID)))
		key := hex.EncodeToString(sum[:])
		request := domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationConversationCLI}
		workspace := ""
		var projectCredentialEnv map[string]string
		if s.RuntimeMode == domain.AssistantModeWorkspace {
			if s.ExecutionRunnerID != "" {
				if _, err := h.conversationRunner(s, true); err != nil {
					return "", err
				}
			}
			preparingWorkspace = true
			runID := s.workspaceRun()
			run, ok := h.repo.RunState(runID)
			if !ok {
				return "", fmt.Errorf("工作区不存在")
			}
			if run.ExecutionContext.ExecutionRunnerID != s.ExecutionRunnerID {
				return "", fmt.Errorf("工作区与会话不在同一执行机器")
			}
			if !run.IsWorkspace() && !sopStopped(run) {
				return "", fmt.Errorf("SOP 正在执行，请先暂停后接管工作区")
			}
			var err error
			if s.workspaceTurn != nil {
				ctx, err = s.workspaceTurn.lease(ctx, runID)
				if err != nil {
					return "", err
				}
				// The lease may rebind a workspace whose previous instance is
				// gone. The run loaded above would still look foreign.
				run, ok = h.repo.RunState(runID)
				if !ok {
					return "", fmt.Errorf("工作区不存在")
				}
			}
			request, err = h.engine.TrustedWorkspaceRequest(runID, request)
			if err != nil {
				return "", err
			}
			if request.Sandbox == nil || !domain.ConversationSandboxDriver(request.Sandbox.Profile.Driver) {
				return "", fmt.Errorf("对话工作区只支持 Docker 或 native 沙箱")
			}
			if len(s.projectCredentialIDs) > 0 {
				project, ok := h.repo.Project(s.ProjectID)
				use, _ := domain.ProjectCredentialAccess(project, s.UserID, h.repo.IsPlatformAdmin(s.User.LoginName))
				if !ok || !use || project.ID != s.ProjectID || run.ExecutionContext.ProjectID != s.ProjectID {
					return "", fmt.Errorf("无权在该工作区使用项目凭据")
				}
				projectCredentialEnv, err = h.repo.ResolveProjectCredentialEnv(ctx, project.ID, s.projectCredentialIDs)
				if err != nil {
					return "", err
				}
				s.recordProjectCredentialValues(projectCredentialEnv)
			}
			if s.ExecutionRunnerID == "" {
				var reason string
				workspace, reason = h.engine.LocalRunWorkspace(run)
				if workspace == "" {
					return "", fmt.Errorf("工作区暂不可用：%s", reason)
				}
			}
			preparingWorkspace = false
		}
		call := func(callCtx context.Context, req runtime.ConversationProcessRequest) (runtime.ConversationProcessResult, error) {
			preDispatchError := func(err error) (runtime.ConversationProcessResult, error) {
				if req.Operation == "start" {
					err = conversationNotStarted(err, false)
				}
				return runtime.ConversationProcessResult{}, err
			}
			req.SessionKey = key
			req.Transient = s.transient
			req.SourceRoot = root
			if s.ExecutionRunnerID == "" {
				forward := func(toolCtx context.Context, token string, body json.RawMessage) (json.RawMessage, error) {
					bound, ok := agentToolSessions.get(token)
					if !ok || bound != s {
						return nil, errAgentToolInvalidSession
					}
					var rpc agentToolRPCRequest
					if err := json.Unmarshal(body, &rpc); err != nil {
						return nil, err
					}
					response := (&agentToolGateway{deps: h.deps}).handleRPC(withAgentToolToken(toolCtx, token), bound, rpc)
					if response == nil {
						return nil, nil
					}
					return json.Marshal(response)
				}
				var factory runtime.ConversationSandboxFactory
				if request.RunID != "" {
					factory = func(envCtx context.Context, nativeRoot string) (runtime.Sandbox, func(), error) {
						return h.engine.AcquireConversationCLI(envCtx, request.RunID, nativeRoot)
					}
				}
				callCtx = gitwork.WithSSHKnownHostsLookup(callCtx, h.repo.PlatformSSHKnownHosts)
				result, err := h.conversationProcesses.Handle(callCtx, root, workspace, request.Sandbox, req, forward, factory)
				if err != nil && req.Operation == "start" {
					err = conversationNotStarted(err, false)
				}
				return result, err
			}
			raw, err := json.Marshal(req)
			if err != nil {
				return preDispatchError(err)
			}
			envelope := request
			envelope.Conversation = raw
			// A revoked turn can finish and release its resources. Preparing a
			// new turn, sending files and starting a process need a live grant.
			checkAccess := false
			switch req.Operation {
			case "prepare", "resetAssets", "sync", "start":
				checkAccess = true
			}
			var response domain.RunnerWorkspaceResponse
			for delay := 50 * time.Millisecond; ; delay *= 2 {
				runner, runnerErr := h.conversationRunner(s, checkAccess)
				if runnerErr != nil {
					return preDispatchError(runnerErr)
				}
				if len(projectCredentialEnv) > 0 && !runner.SupportsProjectCredentials() {
					return preDispatchError(&runtime.ConversationWorkspaceSetupError{Cause: fmt.Errorf("执行机器需升级至 %s 才能使用项目凭据", domain.RunnerVersionProjectCredentials)})
				}
				if req.Command != nil && req.Command.Provider == assistantEngineCodeBuddy &&
					req.Command.Credentials.CodeBuddy.BaseURL != "" && !runner.SupportsCodeBuddyBaseURL() {
					return preDispatchError(fmt.Errorf("执行机器需升级至 %s 才能使用 CodeBuddy Base URL", domain.RunnerVersionCodeBuddyBaseURL))
				}
				response, err = h.runnerControls.requestWorkspace(callCtx, s.ExecutionRunnerID, envelope)
				// The runner returned 429 before handling the request. Retry
				// with backoff and check the machine again before each send.
				if err != nil || response.Status != http.StatusTooManyRequests || delay > conversationProcessBusyMaxDelay {
					break
				}
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-callCtx.Done():
					timer.Stop()
					return runtime.ConversationProcessResult{}, callCtx.Err()
				}
			}
			if err != nil {
				return runtime.ConversationProcessResult{}, fmt.Errorf("%w: %w", errAssistantExecutionUnavailable, err)
			}
			if response.Error != "" {
				responseErr := errors.New(response.Error)
				switch response.Code {
				case domain.WorkspaceErrorConversationState:
					responseErr = &runtime.ConversationStateError{Message: response.Error}
				case domain.WorkspaceErrorConversationSetup:
					responseErr = &runtime.ConversationWorkspaceSetupError{Cause: responseErr}
				}
				if req.Operation == "start" {
					// Older runners return workspace startup failures without a
					// code. Preserve fallback for their confirmed start errors.
					legacyWorkspaceError := request.RunID != "" && response.Code == "" &&
						(response.Status == http.StatusBadRequest || response.Status == http.StatusNotFound)
					return runtime.ConversationProcessResult{}, conversationNotStarted(responseErr, legacyWorkspaceError)
				}
				return runtime.ConversationProcessResult{}, responseErr
			}
			var result runtime.ConversationProcessResult
			err = json.Unmarshal(response.Payload, &result)
			return result, err
		}
		if s.transient {
			defer func() {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				defer cancel()
				_, _ = call(cleanup, runtime.ConversationProcessRequest{Operation: "discard"})
			}()
		}
		prepared, err := call(ctx, runtime.ConversationProcessRequest{Operation: "prepare"})
		if err != nil {
			return "", err
		}
		reportRemovedConversationSkillLinks(ctx, s.User, prepared.RemovedSkillLinks, s.Notify)
		if input, ok := command.Stdin.(*assistantLimitedImageInput); ok {
			command, err = input.fit(command, root, prepared.Root)
			if err != nil {
				return "", err
			}
		}
		if s.ExecutionRunnerID != "" {
			files, err := conversationInputFiles(root)
			if err != nil {
				return "", err
			}
			if !prepared.PromptInputFiles {
				for _, file := range files {
					if runtime.ConversationPromptInputAsset(file.Path) {
						return "", fmt.Errorf("执行机器需要升级以支持该执行器的长对话输入")
					}
				}
			}
			if _, err = call(ctx, runtime.ConversationProcessRequest{Operation: "resetAssets"}); err != nil {
				return "", err
			}
			for len(files) > 0 {
				n, bytes := 0, 0
				for n < len(files) && n < 32 && bytes+len(files[n].Data) <= 10<<20 {
					bytes += len(files[n].Data)
					n++
				}
				if _, err = call(ctx, runtime.ConversationProcessRequest{Operation: "sync", Files: files[:n]}); err != nil {
					return "", err
				}
				files = files[n:]
			}
		}
		command = runtime.WithCommandEnvironment(command, map[string]string{"AGENTFLOW_NATIVE_APPROVAL_URL": agentToolMCPURL(s.Token)})
		if s.RuntimeMode == domain.AssistantModeWorkspace {
			command, credentials = h.bindWorkspaceGit(ctx, s, command, credentials)
		}
		wire := runtime.ConversationCommandFor(command, credentials)
		wire.ProjectCredentialEnv = projectCredentialEnv
		wire.RunnerLogin = s.runnerLogin
		if s.ExecutionRunnerID != "" {
			wire.Argv[0] = assistantEngineBinary(command.Provider)
		}
		id, err := runtime.NewConversationProcessID()
		if err != nil {
			return "", err
		}
		var stopOnce sync.Once
		stopProcess := func() {
			stopOnce.Do(func() {
				// The ID is addressable before start is dispatched. Stop also fences
				// a queued start, so a lost response cannot leave a CLI running.
				// Keep stopping after the turn is cancelled, but not forever: a
				// runner that never returns would otherwise pin this generation
				// and the conversation lock. The runner reaps the process when
				// its control connection or the conversation lease ends.
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), conversationProcessStopTimeout)
				defer cancel()
				if err := waitConversationProcessStop(stopCtx, id, call, nil); err != nil {
					assistantDiagnosticsFrom(ctx).platform("cli.stop_abandoned", fmt.Sprintf("停止原生进程超过 %s 仍未确认（%v），放弃等待", conversationProcessStopTimeout, err), s.User)
				}
			})
		}
		defer stopProcess()
		preparing = false
		started, err := call(ctx, runtime.ConversationProcessRequest{Operation: "start", ID: id, Command: wire, Token: s.Token, Gateway: config.Current().AgentToolGateway, ReportVersion: assistantDiagnosticsFrom(ctx) != nil})
		if err != nil {
			if errors.Is(err, runtime.ErrConversationNotStarted) {
				stopOnce.Do(func() {}) // confirmed failure before process execution
			}
			return "", err
		}
		if acp, ok := command.Stdin.(*assistantACP); ok && started.WorkingDir != "" {
			acp.cwd = started.WorkingDir
		}

		ioCtx, stopIO := context.WithCancel(ctx)
		defer stopIO()
		inputErrors := make(chan error, 1)
		if command.Stdin != nil {
			inputDone := make(chan struct{})
			defer func() {
				stopIO()
				stopProcess()
				if closer, ok := command.Stdin.(io.Closer); ok {
					_ = closer.Close()
				}
				<-inputDone
			}()
			go func() {
				defer close(inputDone)
				fail := func(err error) { inputErrors <- err; stopIO() }
				// Read complete frames, then split oversized writes for runners.
				// The CLI receives the same byte stream, including newlines.
				reader := bufio.NewReaderSize(command.Stdin, 64<<10)
				rootPath := runtime.ConversationStdinPath(root, command.Provider)
				workspacePath := runtime.ConversationStdinPath(filepath.Join(root, "ws"), command.Provider)
				for {
					data, readErr := reader.ReadString('\n')
					for len(data) > 0 {
						end := conversationInputChunkLen(data, workspacePath, rootPath)
						if _, writeErr := call(ioCtx, runtime.ConversationProcessRequest{Operation: "write", ID: started.ID, Data: data[:end]}); writeErr != nil {
							fail(fmt.Errorf("原生进程输入失败：%w", writeErr))
							return
						}
						data = data[end:]
					}
					if readErr != nil {
						if readErr == io.EOF {
							if _, err := call(ioCtx, runtime.ConversationProcessRequest{Operation: "write", ID: started.ID, EOF: true}); err != nil {
								fail(fmt.Errorf("关闭原生进程输入失败：%w", err))
							}
						} else if ioCtx.Err() == nil {
							fail(fmt.Errorf("读取原生进程输入失败：%w", readErr))
						}
						return
					}
				}
			}()
		}
		after := int64(0)
		versionReported := false
		for {
			if err := ioCtx.Err(); err != nil {
				return "", err
			}
			result, pollErr := call(ioCtx, runtime.ConversationProcessRequest{Operation: "poll", ID: started.ID, After: after})
			if pollErr == nil && result.Done {
				// The final poll confirms Exec and descendant cleanup even if
				// the trailing stdin write failed or the runner disconnects.
				stopOnce.Do(func() {})
			}
			select {
			case inputErr := <-inputErrors:
				return "", inputErr
			default:
			}
			if pollErr != nil {
				return "", pollErr
			}
			if sink != nil && !versionReported && result.CLIVersion != "" {
				sink(domain.TelemetryEvent{Name: runtime.CLIVersionEvent, Attributes: map[string]any{
					"version": result.CLIVersion, "source": result.VersionSource,
				}})
				versionReported = true
			}
			for _, event := range result.Events {
				if sink != nil {
					sink(event)
				}
			}
			after = result.Sequence
			if result.Done {
				s.noteGitScrubbed(result.GitScrubbed)
				if result.Error != "" {
					if result.NotStarted {
						workspaceSetup := request.RunID != "" && (result.ErrorCode == domain.WorkspaceErrorConversationSetup || result.ErrorCode == "")
						return "", conversationNotStarted(errors.New(result.Error), workspaceSetup)
					}
					return "", fmt.Errorf("%s", result.Error)
				}
				return "", nil
			}
		}
	}
}

const maxConversationInputChunk = 1 << 20

func conversationInputChunkLen(data string, paths ...string) int {
	return runtime.ConversationTextChunkLen(data, maxConversationInputChunk, paths...)
}

func assistantEngineBinary(kind string) string {
	switch kind {
	case assistantEngineCodeBuddy:
		return "codebuddy"
	case assistantEngineCodex:
		return "codex"
	case assistantEngineClaude:
		return "claude"
	default:
		return runtime.AgentCLIBinary(kind)
	}
}

func reportRemovedConversationSkillLinks(ctx context.Context, user domain.User, paths []string, notify func(string, any)) {
	if len(paths) == 0 {
		return
	}
	const maxDisplayedPaths = 5
	names := make([]string, 0, min(len(paths), maxDisplayedPaths))
	for _, path := range paths[:min(len(paths), maxDisplayedPaths)] {
		names = append(names, fmt.Sprintf("%q", truncateRunes(path, 250)))
	}
	summary := strings.Join(names, "、")
	if len(paths) > maxDisplayedPaths {
		summary += fmt.Sprintf(" 等 %d 项", len(paths))
	}
	message := fmt.Sprintf("技能目录不支持符号链接。已移除链接并继续执行（%s）。", summary)
	assistantDiagnosticsFrom(ctx).platform("conversation.skill_links_removed", message, user)
	if notify != nil {
		notify("thought", assistantStreamEvent{Kind: "status", Text: sanitizeAssistantDiagnostic(message, user, 2048)})
	}
}

// Synchronize generated configuration and selected assets, excluding native
// transcripts and the credentials written by the execution host.
func conversationInputFiles(root string) ([]runtime.ConversationFile, error) {
	var out []runtime.ConversationFile
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if !runtime.ConversationAssetDirectory(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		allowed, rewrite := runtime.ConversationAsset(rel)
		if !allowed {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 10<<20 {
			return fmt.Errorf("会话资产超过 10MB：%s", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, runtime.ConversationFile{Path: rel, Data: data, Mode: uint32(info.Mode().Perm()), Rewrite: rewrite})
		return nil
	})
	return out, err
}

func stageConversationToolFiles(root string, files agentToolFiles) (string, string, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", "", err
	}
	config, settings := filepath.Join(root, "current-mcp.json"), filepath.Join(root, "current-settings.json")
	for source, target := range map[string]string{files.MCPConfig: config, files.Settings: settings} {
		data, err := os.ReadFile(source)
		if err != nil {
			return "", "", err
		}
		if err = os.WriteFile(target, data, 0600); err != nil {
			return "", "", err
		}
	}
	return config, settings, nil
}

// A stop timeout or an idle root cannot rule out a dispatched, queued start.
// Retry the addressable stop across reconnects until the Runner confirms its
// cancellation fence and actual process cleanup.
// conversationProcessStopTimeout bounds how long a finished or cancelled turn
// keeps asking an unreachable execution machine to stop its CLI.
var conversationProcessStopTimeout = 30 * time.Second

// conversationProcessBusyMaxDelay is the longest backoff step while a runner
// reports its request slots as full (about 6s in total).
var conversationProcessBusyMaxDelay = 3200 * time.Millisecond

func waitConversationProcessStop(ctx context.Context, id string, call func(context.Context, runtime.ConversationProcessRequest) (runtime.ConversationProcessResult, error), notify func(bool)) error {
	waiting := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		result, err := call(stopCtx, runtime.ConversationProcessRequest{Operation: "stop", ID: id})
		cancel()
		if err == nil && result.Done {
			break
		}
		if !waiting && notify != nil {
			notify(true)
		}
		waiting = true
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	if waiting && notify != nil {
		notify(false)
	}
	return nil
}

// bindWorkspaceGit gives the workspace CLI this turn's requester's Git
// credentials, so the agent clones, pushes and calls the Git host itself.
// Each turn starts its CLI again, so a shared conversation always runs with
// the identity of whoever sent the current message. Git tokens never travel
// with the command: the helper fetches them from this turn's session, so a
// removed token stops working at once.
func (h *assistantHandlers) bindWorkspaceGit(ctx context.Context, s *AgentToolSession, command runtime.CommandSpec, credentials runtime.RunCredentials) (runtime.CommandSpec, runtime.RunCredentials) {
	credentials.GitHTTPCredentials = nil
	if s.User.ID <= 0 || h.repo == nil {
		return command, credentials
	}
	credentials.GitSSHHostKeys = h.repo.GitSSHKeysByHost(s.User.ID)
	hosts := h.repo.GitHTTPWorkspaceHosts(s.User.ID)
	if len(hosts) > 0 && s.ExecutionRunnerID != "" {
		runner, _ := h.repo.Runner(s.ExecutionRunnerID)
		if !runner.SupportsGitPlainHTTP() {
			before := len(hosts)
			maps.DeleteFunc(hosts, func(host, _ string) bool { return strings.HasPrefix(host, "http://") })
			if len(hosts) < before {
				assistantDiagnosticsFrom(ctx).platform("git.plain_http_unavailable", domain.RunnerGitPlainHTTPUpgradeMessage, s.User)
			}
		}
		if len(hosts) > 0 && !runner.SupportsGitHTTP() {
			assistantDiagnosticsFrom(ctx).platform("git.http_unavailable", fmt.Sprintf("执行机器需升级至 %s 才能在工作区使用 Git 访问令牌", domain.RunnerVersionGitHTTP), s.User)
			hosts = nil
		}
	}
	if len(hosts) > 0 {
		credentials.GitHTTPHosts = hosts
		command = runtime.WithCommandEnvironment(command, map[string]string{runtime.SessionGatewayEnv: agentToolMCPURL(s.Token)})
	}
	if user, ok := h.repo.User(s.User.ID); ok {
		if login := strings.TrimSpace(user.LoginName); login != "" {
			credentials.GitUserName = login
			credentials.GitUserEmail = login + "@users.noreply.loopforge.invalid"
		}
	}
	if !slices.Contains(command.CredentialBindings, runtime.BindGitWorkspace) {
		command.CredentialBindings = append(append([]runtime.CredentialBinding(nil), command.CredentialBindings...), runtime.BindGitWorkspace)
	}
	return command, credentials
}

func (h *assistantHandlers) testRunnerCLI(ctx context.Context, runnerID, provider string, runnerLogin bool, user domain.User) error {
	root, err := os.MkdirTemp("", "loopforge-cli-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	model := provider + ":default"
	if provider == assistantEngineCodeBuddy {
		model = loadAssistantModels(user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey).Default
		if user.CodeBuddyBaseURL != "" {
			model = personalCodeBuddyCatalog(ctx, user).Default
		}
	} else if !runnerLogin {
		models, _ := assistantDiscoveredModelsFor(ctx, user, provider)
		if len(models) == 0 {
			models = assistantEngineBuiltinModels(provider)
		}
		if len(models) > 0 {
			model = provider + ":" + models[0].ID
		}
	}
	session := &AgentToolSession{
		UserID: user.ID, ChatOwnerID: user.ID, ChatSessionID: newAssistantSessionID(), ExecutionRunnerID: runnerID,
		RuntimeMode: domain.AssistantModePlatform, runnerLogin: runnerLogin, transient: true,
	}
	_, _, err = runAssistantCLI(ctx, assistantCLIRequest{
		Executor: provider, Model: model, Prompt: "Respond with OK.",
		SystemPrompt: "You are a connectivity check. Reply in plain text. Do not use tools.",
		SessionRoot:  root, ReviewOnly: true, RemoteBinary: true, RunnerLogin: runnerLogin,
		Execute: h.conversationExecutor(session, root),
	}, user, nil)
	return err
}
