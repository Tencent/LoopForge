package runtime

import (
	"agentflow/internal/domain"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type ExecutorRunner struct{}

func NewExecutorRunner() *ExecutorRunner { return &ExecutorRunner{} }

func (r *ExecutorRunner) Run(ctx context.Context, sb Sandbox, invocationID string, ex domain.ExecutorDefinition, agent domain.AgentDefinition, assets domain.AgentAssetBundle, prompt string, options InvocationOptions, sink EventSink) (string, error) {
	provider := providerForKind(ex.Kind)
	if err := ProvisionMCPAssets(ctx, sb, sb.Workspace(), assets, sink); err != nil {
		return "", err
	}
	driver := sandboxDriver(sb)
	if options.BrowserMCP == nil && BrowserWantedForExecutor(ex.Kind) {
		options.BrowserMCP = EnsureSandboxBrowser(ctx, sb, sb.Workspace(), options.RunID, sink)
	}
	compiled, err := materializeInvocationForDriver(sb.Workspace(), invocationID, ex, agent, assets, options, driver)
	if err != nil {
		return "", err
	}
	if result, handled, err := provider.RunBuiltin(ctx, agent, prompt, sink); handled {
		if id := strings.TrimSpace(options.ResumeSessionID); id != "" && strings.Contains(agent.Prompt, "[[mock:session=lost]]") {
			return fmt.Sprintf("{\"type\":\"error\",\"error\":\"No conversation found with session ID: %s\"}", id), nil
		}
		if err == nil && mockShouldSubmitGate(sb.Workspace(), agent, options) {
			if writeErr := writeMockGateResult(sb.Workspace(), options.GateResultPath, agent); writeErr != nil {
				return result, writeErr
			}
			if sink != nil {
				success := true
				sessionID := strings.TrimSpace(options.ResumeSessionID)
				if sessionID == "" {
					sessionID = "mock-" + agent.ID
				}
				sink(domain.TelemetryEvent{Category: "tool", Name: "tool.call", Phase: "finish", Message: "submit_gate_result 完成", SessionID: sessionID, Tool: &domain.ToolObservation{Name: "submit_gate_result", Operation: "tool.result", Success: &success}})
			}
		}
		return result, err
	}
	promptRel, err := writeTaskPromptFile(sb.Workspace(), compiled.RuntimeRoot, prompt)
	if err != nil {
		return "", err
	}
	if len(prompt) > maxArgvPromptBytes {
		if err := appendTaskPromptToSystemFile(sb.Workspace(), compiled.AgentRoot, prompt); err != nil {
			return "", err
		}
	}
	command, err := provider.Prepare(providerInvocation{Executor: ex, Compiled: compiled, Prompt: prompt, PromptFile: promptRel, Driver: driver, Options: options})
	if err != nil {
		return "", err
	}
	if strings.EqualFold(command.Provider, "codex") {
		overlay := map[string]string{}
		for key, value := range sandboxIsolationEnv(sb) {
			overlay[key] = value
		}
		if driver == "local" {
			overlay["GIT_CEILING_DIRECTORIES"] = filepath.Dir(sb.Workspace())
		}
		command.Argv = withCodexIsolationEnv(command.Argv, overlay)
	}
	if compiled.HookOutput != "" {
		command.HookOutput = compiled.HookOutput
	}
	command = WithCommandEnvironment(command, hookExecutionEnvironment(options.Repositories))
	adapter := provider.Protocol(ex, sink)
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopObservation := provider.StartObservation(execCtx, compiled, sink)
	defer stopObservation()
	stopHookObservation := startHookObservation(execCtx, compiled, sink)
	defer stopHookObservation()
	go func() {
		select {
		case <-adapter.Completed():
			// Some CLI hook processes stay alive after the protocol has emitted its
			// terminal result. Give stdout a short drain window, then stop the process.
			timer := time.NewTimer(2500 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
				cancel()
			case <-execCtx.Done():
			}
		case <-execCtx.Done():
		}
	}()
	raw, err := sb.Exec(execCtx, command, adapter.Handle)
	output := strings.TrimSpace(adapter.VisibleOutput())
	if adapter.Failed() {
		if isMaxTurnsBudget(adapter.Result(), raw, err) {
			adapter.emit(domain.TelemetryEvent{
				Category: "lifecycle",
				Name:     "agent.completed",
				Phase:    "finish",
				Level:    "warn",
				Message:  "executor reached max turns; waiting for human retry decision",
			})
			if output == "" {
				output = strings.TrimSpace(raw)
			}
			return clipExecutorOutput(output), ErrMaxTurns
		}
		if output == "" {
			output = raw
		}
		if err == nil {
			err = executorReportedError(adapter.Result(), raw)
		}
		// Only classify the protocol's terminal error, never the raw stream:
		// transcripts can contain unrelated assistant text and tool results.
		if ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			if class := classifyModelFailure(adapter.Result()); class != "" {
				if detail := executorReportedError(adapter.Result(), raw); detail != nil && !strings.Contains(err.Error(), detail.Error()) {
					err = fmt.Errorf("%s: %w", detail, err)
				}
				err = &ModelError{Class: class, Err: err, RetryAfter: parseModelRetryAfter(adapter.Result())}
			}
		}
		return clipExecutorOutput(output), err
	}
	if output != "" {
		return clipExecutorOutput(output), nil
	}
	if !adapter.Finished() {
		if err == nil {
			// CodeBuddy often exits 0 after max-turns or hook drain without emitting
			// a terminal `result` frame even though deliverables are on disk. Gate
			// verification is the source of truth — do not fail the node here.
			adapter.emit(domain.TelemetryEvent{
				Category: "lifecycle",
				Name:     "agent.completed",
				Phase:    "finish",
				Level:    "warn",
				Message:  "executor exited without terminal result; deferring to gate verification",
			})
			return clipExecutorOutput(strings.TrimSpace(raw)), nil
		}
		return clipExecutorOutput(raw), err
	}
	return clipExecutorOutput(raw), err
}

// RemoteInvocation is the self-contained input a CLI Runner daemon needs to
// reconstruct one Agent node invocation in its own task workspace: the same
// executor/agent/asset bundle the control plane materializes locally, plus
// the fully compiled prompt.
type RemoteInvocation struct {
	InvocationID string
	Executor     domain.ExecutorDefinition
	Agent        domain.AgentDefinition
	Assets       domain.AgentAssetBundle
	Prompt       string
	Options      InvocationOptions
	// Driver controls provider flags and paths for the sandbox that will
	// execute this command on the runner machine.
	Driver string
}

// PreparedRemote is the result of preparing a remote invocation. Builtin
// executors (mock) need no process; otherwise Command holds the closed CLI
// argv that the runner machine should spawn in the task workspace.
type PreparedRemote struct {
	Builtin       bool
	BuiltinResult string
	Command       CommandSpec
}

// PrepareRemote materializes the Agent asset view and compiles the CLI
// command for execution on a CLI Runner machine. It performs no process
// execution and requests no credentials: Agent CLI credentials stay on the
// runner machine by design (cli-connect.md).
func (r *ExecutorRunner) PrepareRemote(workspace string, inv RemoteInvocation) (*PreparedRemote, error) {
	provider := providerForKind(inv.Executor.Kind)
	driver := strings.TrimSpace(inv.Driver)
	if driver == "" || driver == "local" {
		// Driver "runner" keeps the CLI's own host login state usable: no
		// private HOME and no user-config isolation flags.
		driver = "runner"
	}
	compiled, err := materializeInvocationForDriver(workspace, inv.InvocationID, inv.Executor, inv.Agent, inv.Assets, inv.Options, driver)
	if err != nil {
		return nil, err
	}
	if result, handled, err := provider.RunBuiltin(context.Background(), inv.Agent, inv.Prompt, nil); handled {
		if err == nil && mockShouldSubmitGate(workspace, inv.Agent, inv.Options) {
			if writeErr := writeMockGateResult(workspace, inv.Options.GateResultPath, inv.Agent); writeErr != nil {
				return nil, writeErr
			}
		}
		return &PreparedRemote{Builtin: true, BuiltinResult: result}, err
	}
	promptRel, err := writeTaskPromptFile(workspace, compiled.RuntimeRoot, inv.Prompt)
	if err != nil {
		return nil, err
	}
	if len(inv.Prompt) > maxArgvPromptBytes {
		if err := appendTaskPromptToSystemFile(workspace, compiled.AgentRoot, inv.Prompt); err != nil {
			return nil, err
		}
	}
	command, err := provider.Prepare(providerInvocation{Executor: inv.Executor, Compiled: compiled, Prompt: inv.Prompt, PromptFile: promptRel, Driver: driver, Options: inv.Options})
	if err != nil {
		return nil, err
	}
	if compiled.HookOutput != "" {
		command.HookOutput = compiled.HookOutput
	}
	command = WithCommandEnvironment(command, hookExecutionEnvironment(inv.Options.Repositories))
	return &PreparedRemote{Command: command}, nil
}

func mockShouldSubmitGate(workspace string, agent domain.AgentDefinition, options InvocationOptions) bool {
	if strings.TrimSpace(options.GateResultPath) == "" {
		return false
	}
	if strings.Contains(agent.Prompt, "[[mock:session=lost]]") {
		// The first call omits the gate file so the engine resumes. That
		// resume reports the session missing. The following fresh start
		// finds the marker and submits.
		marker := filepath.Join(workspace, ".agentflow-mock-lost-session")
		if _, err := os.Stat(marker); err != nil {
			_ = os.MkdirAll(filepath.Dir(marker), 0o700)
			_ = os.WriteFile(marker, []byte("1"), 0o600)
			return false
		}
		return strings.TrimSpace(options.ResumeSessionID) == ""
	}
	// This one fixture intentionally omits the first terminal call so the
	// engine's same-session recovery path remains covered.
	return agent.ID != "submit-gate-agent" || strings.TrimSpace(options.ResumeSessionID) != ""
}

func writeTaskPromptFile(workspace, runtimeRel, prompt string) (string, error) {
	rel := filepath.ToSlash(filepath.Join(runtimeRel, "task-prompt.md"))
	path := filepath.Join(workspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := writeManagedFile(workspace, path, []byte(prompt), 0o600); err != nil {
		return "", err
	}
	return rel, nil
}

func appendTaskPromptToSystemFile(workspace, agentRel, prompt string) error {
	path := filepath.Join(workspace, filepath.FromSlash(agentRel), "agent-system.md")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n\n# 本次节点任务书\n\n%s\n", prompt)
	return err
}

const maxExecutorOutputBytes = 32 << 10

// clipOnRuneBoundary cuts s to at most n bytes without splitting a UTF-8
// sequence. It backs off at most utf8.UTFMax-1 bytes, so malformed input
// cannot shrink the result to nothing.
func clipOnRuneBoundary(s string, n int) string {
	if len(s) <= n {
		return s
	}
	end := n
	for i := 0; i < utf8.UTFMax-1 && end > 0 && !utf8.RuneStart(s[end]); i++ {
		end--
	}
	if !utf8.RuneStart(s[end]) {
		// Not valid UTF-8 here anyway; keep the byte cut.
		end = n
	}
	return s[:end]
}

func clipExecutorOutput(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxExecutorOutputBytes {
		return s
	}
	return clipOnRuneBoundary(s, maxExecutorOutputBytes) + fmt.Sprintf("\n… [output truncated, %d bytes]", len(s))
}

var ErrMaxTurns = errors.New("executor reached max turns")

func isMaxTurnsBudget(adapterResult, raw string, processErr error) bool {
	if processErr != nil {
		return false
	}
	blob := strings.ToLower(adapterResult + "\n" + raw)
	return strings.Contains(blob, "max turns") || strings.Contains(blob, "max-turns") || strings.Contains(blob, "maximum turns")
}

func executorReportedError(adapterResult, raw string) error {
	blob := adapterResult + "\n" + raw
	lower := strings.ToLower(blob)
	if strings.Contains(lower, "401") && strings.Contains(lower, "unauthorized") {
		return fmt.Errorf("executor reported an error: 401 Unauthorized")
	}
	msg := strings.TrimSpace(adapterResult)
	if msg == "" {
		return fmt.Errorf("executor reported an error")
	}
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 240 {
		msg = msg[:240]
	}
	return fmt.Errorf("executor reported an error: %s", msg)
}

func hasArg(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}

// dropArgWithValue removes every occurrence of the given flags together with
// their value, accepting both the "--flag value" and "--flag=value" forms.
func dropArgWithValue(values []string, flags ...string) []string {
	out := make([]string, 0, len(values))
	skip := false
	for _, value := range values {
		if skip {
			skip = false
			continue
		}
		if slices.Contains(flags, value) {
			skip = true
			continue
		}
		if slices.ContainsFunc(flags, func(flag string) bool { return strings.HasPrefix(value, flag+"=") }) {
			continue
		}
		out = append(out, value)
	}
	return out
}

func sandboxDriver(sb Sandbox) string {
	if value, ok := sb.(interface{ Driver() string }); ok {
		if driver := strings.TrimSpace(value.Driver()); driver != "" {
			return driver
		}
	}
	// Sandboxes without a declared driver predate filesystem isolation and must
	// retain the conservative local compatibility policy.
	return "local"
}
