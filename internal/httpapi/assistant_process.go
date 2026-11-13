package httpapi

import (
	"agentflow/internal/config"
	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type assistantHelperExecutionKey struct{}
type assistantHelperExecution struct {
	h      *assistantHandlers
	owner  int64
	runner string
}

func (h *assistantHandlers) helperExecutionContext(ctx context.Context, owner int64, target *domain.AssistantExecutionTarget) context.Context {
	if target == nil || target.RunnerID == "" {
		return ctx
	}
	return context.WithValue(ctx, assistantHelperExecutionKey{}, assistantHelperExecution{h: h, owner: owner, runner: target.RunnerID})
}

func helperCLIExecution(ctx context.Context, root string) func(context.Context, string, runtime.CommandSpec, runtime.RunCredentials, runtime.EventSink) (string, error) {
	bound, ok := ctx.Value(assistantHelperExecutionKey{}).(assistantHelperExecution)
	if !ok {
		return nil
	}
	s := &AgentToolSession{ChatOwnerID: bound.owner, ChatSessionID: "helper-" + newAssistantSessionID(), ExecutionRunnerID: bound.runner, RuntimeMode: domain.AssistantModePlatform}
	return bound.h.conversationExecutor(s, root)
}

// Protocol translation stays on the API process; the bound machine owns the
// CLI, its descendants and its durable native HOME in both execution modes.
func (h *assistantHandlers) conversationExecutor(s *AgentToolSession, root string) func(context.Context, string, runtime.CommandSpec, runtime.RunCredentials, runtime.EventSink) (string, error) {
	return func(ctx context.Context, _ string, command runtime.CommandSpec, credentials runtime.RunCredentials, sink runtime.EventSink) (output string, err error) {
		preparing := true
		defer func() {
			if err != nil && preparing {
				err = fmt.Errorf("%w: %w", runtime.ErrConversationNotStarted, err)
			}
		}()
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d/%s", s.ChatOwnerID, s.ChatSessionID)))
		key := hex.EncodeToString(sum[:])
		request := domain.RunnerWorkspaceRequest{Operation: "conversation_process"}
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
			}
			request, err = h.engine.TrustedWorkspaceRequest(runID, request)
			if err != nil {
				return "", err
			}
			if request.Sandbox == nil || request.Sandbox.Profile.Driver != "docker" {
				return "", fmt.Errorf("对话工作区仅支持 Docker")
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
			raw, err := json.Marshal(req)
			if err != nil {
				return preDispatchError(err)
			}
			envelope := request
			envelope.Conversation = raw
			response, err := h.runnerControls.requestWorkspace(callCtx, s.ExecutionRunnerID, envelope)
			if err != nil {
				return runtime.ConversationProcessResult{}, fmt.Errorf("%w: %w", errAssistantExecutionUnavailable, err)
			}
			if response.Error != "" {
				if req.Operation == "start" {
					return runtime.ConversationProcessResult{}, fmt.Errorf("%w: %s", runtime.ErrConversationNotStarted, response.Error)
				}
				return runtime.ConversationProcessResult{}, fmt.Errorf("%s", response.Error)
			}
			var result runtime.ConversationProcessResult
			err = json.Unmarshal(response.Payload, &result)
			return result, err
		}
		if _, err := call(ctx, runtime.ConversationProcessRequest{Operation: "prepare"}); err != nil {
			return "", err
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
		wire := runtime.ConversationCommandFor(command, credentials)
		if s.ExecutionRunnerID != "" {
			wire.Argv[0] = assistantEngineBinary(command.Provider)
		}
		preparing = false
		started, err := call(ctx, runtime.ConversationProcessRequest{Operation: "start", Command: wire, Token: s.Token, Gateway: config.Current().AgentToolGateway})
		if err != nil {
			return "", err
		}
		var stopOnce sync.Once
		stopProcess := func() {
			stopOnce.Do(func() {
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				_, _ = call(stopCtx, runtime.ConversationProcessRequest{Operation: "stop", ID: started.ID})
			})
		}
		defer stopProcess()
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
				// Interactive protocols use newline-delimited JSON. Preserve a
				// complete frame so remote cwd rewriting cannot split a path.
				reader := bufio.NewReaderSize(command.Stdin, 64<<10)
				for {
					data, readErr := reader.ReadString('\n')
					if len(data) > 0 {
						if _, writeErr := call(ioCtx, runtime.ConversationProcessRequest{Operation: "write", ID: started.ID, Data: data}); writeErr != nil {
							fail(fmt.Errorf("原生进程输入失败：%w", writeErr))
							return
						}
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
		for {
			result, pollErr := call(ioCtx, runtime.ConversationProcessRequest{Operation: "poll", ID: started.ID, After: after})
			select {
			case inputErr := <-inputErrors:
				return "", inputErr
			default:
			}
			if pollErr != nil {
				return "", pollErr
			}
			for _, event := range result.Events {
				if sink != nil {
					sink(event)
				}
			}
			after = result.Sequence
			if result.Done {
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
