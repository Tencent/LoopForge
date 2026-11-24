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
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type assistantHelperExecutionKey struct{}
type assistantHelperExecution struct {
	h      *assistantHandlers
	owner  int64
	target domain.AssistantExecutionTarget
}

func (h *assistantHandlers) helperExecutionContext(ctx context.Context, owner int64, target *domain.AssistantExecutionTarget) context.Context {
	if target == nil || target.RunnerID == "" {
		return ctx
	}
	return context.WithValue(ctx, assistantHelperExecutionKey{}, assistantHelperExecution{h: h, owner: owner, target: *target})
}

func helperCLIExecution(ctx context.Context, root string, actorID int64, engine string) (func(context.Context, string, runtime.CommandSpec, runtime.RunCredentials, runtime.EventSink) (string, error), bool) {
	bound, ok := ctx.Value(assistantHelperExecutionKey{}).(assistantHelperExecution)
	if !ok {
		return nil, false
	}
	s := &AgentToolSession{ChatOwnerID: bound.owner, ChatSessionID: "helper-" + newAssistantSessionID(), ExecutionRunnerID: bound.target.RunnerID, RuntimeMode: domain.AssistantModePlatform}
	s.runnerLogin = bound.h.assistantUsesRunnerLogin(actorID, &bound.target, engine)
	return bound.h.conversationExecutor(s, root), s.runnerLogin
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
		defer func() {
			if err != nil && preparing {
				err = fmt.Errorf("%w: %w", runtime.ErrConversationNotStarted, err)
			}
		}()
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d/%s", s.ChatOwnerID, s.ChatSessionID)))
		key := hex.EncodeToString(sum[:])
		request := domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationConversationCLI}
		workspace := ""
		if s.RuntimeMode == domain.AssistantModeWorkspace {
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
			if s.ExecutionRunnerID == "" {
				var reason string
				workspace, reason = h.engine.LocalRunWorkspace(run)
				if workspace == "" {
					return "", fmt.Errorf("工作区暂不可用：%s", reason)
				}
			}
		}
		call := func(callCtx context.Context, req runtime.ConversationProcessRequest) (runtime.ConversationProcessResult, error) {
			preDispatchError := func(err error) (runtime.ConversationProcessResult, error) {
				if req.Operation == "start" {
					err = fmt.Errorf("%w: %w", runtime.ErrConversationNotStarted, err)
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
					err = fmt.Errorf("%w: %w", runtime.ErrConversationNotStarted, err)
				}
				return result, err
			}
			if h.runnerControls == nil {
				return preDispatchError(errAssistantExecutionUnavailable)
			}
			runner, ok := h.repo.Runner(s.ExecutionRunnerID)
			if !ok || runner.Status != domain.RunnerStatusOnline {
				return preDispatchError(errAssistantExecutionUnavailable)
			}
			if !runner.SupportsConversationCLI() {
				return preDispatchError(fmt.Errorf("执行机器需升级至 %s", domain.RunnerVersionConversationCLI))
			}
			if (s.runnerLogin || s.transient) && !runner.SupportsCLISettings() {
				return preDispatchError(fmt.Errorf("执行机器需升级至 %s 才能使用对话本机登录和 CLI 测试", domain.RunnerVersionCLISettings))
			}
			if req.Command != nil && req.Command.Provider == assistantEngineCodeBuddy &&
				req.Command.Credentials.CodeBuddy.BaseURL != "" && !runner.SupportsCodeBuddyBaseURL() {
				return preDispatchError(fmt.Errorf("执行机器需升级至 %s 才能使用 CodeBuddy Base URL", domain.RunnerVersionCodeBuddyBaseURL))
			}
			raw, err := json.Marshal(req)
			if err != nil {
				return preDispatchError(err)
			}
			envelope := request
			envelope.Conversation = raw
			response, err := h.runnerControls.requestWorkspace(callCtx, s.ExecutionRunnerID, envelope)
			// A 429 means the runner's request slots were full and it rejected
			// the request before handling it, so every operation (including a
			// write) can be sent again. Back off instead of failing the turn.
			for delay := 50 * time.Millisecond; err == nil && response.Status == http.StatusTooManyRequests && delay <= conversationProcessBusyMaxDelay; delay *= 2 {
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-callCtx.Done():
					timer.Stop()
					return runtime.ConversationProcessResult{}, callCtx.Err()
				}
				response, err = h.runnerControls.requestWorkspace(callCtx, s.ExecutionRunnerID, envelope)
			}
			if err != nil {
				return runtime.ConversationProcessResult{}, fmt.Errorf("%w: %w", errAssistantExecutionUnavailable, err)
			}
			if response.Error != "" {
				responseErr := errors.New(response.Error)
				if response.Code == domain.WorkspaceErrorConversationState {
					responseErr = &runtime.ConversationStateError{Message: response.Error}
				}
				if req.Operation == "start" {
					return runtime.ConversationProcessResult{}, fmt.Errorf("%w: %w", runtime.ErrConversationNotStarted, responseErr)
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
						return "", fmt.Errorf("%w: %s", runtime.ErrConversationNotStarted, result.Error)
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
	if len(data) <= maxConversationInputChunk {
		return len(data)
	}
	end := maxConversationInputChunk
	for end > maxConversationInputChunk-utf8.UTFMax && !utf8.RuneStart(data[end]) {
		end--
	}
	// The runner remaps paths separately in each write request.
	for _, path := range paths {
		if len(path) > 1 && len(path) < end {
			start := end - len(path) + 1
			windowEnd := min(len(data), end+len(path)-1)
			if i := strings.Index(data[start:windowEnd], path); i >= 0 && start+i < end {
				end = start + i
			}
		}
	}
	return end
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
// the identity of whoever sent the current message. HTTPS tokens never travel
// with the command: the helper fetches them from this turn's session, so a
// removed token stops working at once.
func (h *assistantHandlers) bindWorkspaceGit(ctx context.Context, s *AgentToolSession, command runtime.CommandSpec, credentials runtime.RunCredentials) (runtime.CommandSpec, runtime.RunCredentials) {
	credentials.GitHTTPCredentials = nil
	if s.User.ID <= 0 || h.repo == nil {
		return command, credentials
	}
	credentials.GitSSHHostKeys = h.repo.GitSSHKeysByHost(s.User.ID)
	if hosts := h.repo.GitHTTPWorkspaceHosts(s.User.ID); len(hosts) > 0 {
		if runner, ok := h.repo.Runner(s.ExecutionRunnerID); s.ExecutionRunnerID != "" && (!ok || !runner.SupportsGitHTTP()) {
			assistantDiagnosticsFrom(ctx).platform("git.http_unavailable", fmt.Sprintf("执行机器需升级至 %s 才能在工作区使用 HTTPS Git 访问令牌", domain.RunnerVersionGitHTTP), s.User)
		} else {
			credentials.GitHTTPHosts = hosts
			command = runtime.WithCommandEnvironment(command, map[string]string{runtime.SessionGatewayEnv: agentToolMCPURL(s.Token)})
		}
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
		ChatOwnerID: user.ID, ChatSessionID: newAssistantSessionID(), ExecutionRunnerID: runnerID,
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
