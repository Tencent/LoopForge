package engine

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

// NodeBudget bounds how much a single node, including its retries and upstream
// reworks, may consume before the engine stops paying for more attempts. A
// zero threshold disables that limit; the warn thresholds only emit a run event
// so operators notice expensive nodes before they exhaust a limit.
type NodeBudget struct {
	ToolWarn   int
	ToolLimit  int
	TokenWarn  int64
	TokenLimit int64
}

const (
	defaultNodeToolWarn   = 300
	defaultNodeToolLimit  = 800
	defaultNodeTokenWarn  = int64(500_000)
	defaultNodeTokenLimit = int64(1_000_000)
)

// DefaultNodeBudget returns the shipping defaults: warn at 300 tool calls or
// 500k tokens, stop and ask a human at 800 tool calls or 1M tokens.
func DefaultNodeBudget() NodeBudget {
	return NodeBudget{
		ToolWarn:   defaultNodeToolWarn,
		ToolLimit:  defaultNodeToolLimit,
		TokenWarn:  defaultNodeTokenWarn,
		TokenLimit: defaultNodeTokenLimit,
	}
}

// NodeBudgetFromEnv starts from DefaultNodeBudget and applies the
// AGENTFLOW_NODE_TOOL_WARN / _TOOL_LIMIT / _TOKEN_WARN / _TOKEN_LIMIT overrides.
// Empty or non-numeric values keep their default; a negative value disables the
// limit, and zero disables it as well.
func NodeBudgetFromEnv() NodeBudget {
	budget := DefaultNodeBudget()
	budget.ToolWarn = envInt("AGENTFLOW_NODE_TOOL_WARN", budget.ToolWarn)
	budget.ToolLimit = envInt("AGENTFLOW_NODE_TOOL_LIMIT", budget.ToolLimit)
	budget.TokenWarn = envInt64("AGENTFLOW_NODE_TOKEN_WARN", budget.TokenWarn)
	budget.TokenLimit = envInt64("AGENTFLOW_NODE_TOKEN_LIMIT", budget.TokenLimit)
	return budget
}

// SetNodeBudget replaces the budget, for tests or when an embedding process
// configures the engine directly.
func (e *Engine) SetNodeBudget(budget NodeBudget) {
	e.nodeBudget = budget
}

// nodeBudgetState reports whether a node crossed the warn/limit thresholds.
type nodeBudgetState struct {
	Warn     bool
	Exceeded bool
	Reasons  []string
}

func (b NodeBudget) evaluate(toolCalls int, tokens int64) nodeBudgetState {
	var state nodeBudgetState
	if b.ToolWarn > 0 && toolCalls >= b.ToolWarn {
		state.Warn = true
		state.Reasons = append(state.Reasons, fmt.Sprintf("工具调用 %d ≥ 提醒阈值 %d", toolCalls, b.ToolWarn))
	}
	if b.TokenWarn > 0 && tokens >= b.TokenWarn {
		state.Warn = true
		state.Reasons = append(state.Reasons, fmt.Sprintf("Token %d ≥ 提醒阈值 %d", tokens, b.TokenWarn))
	}
	if b.ToolLimit > 0 && toolCalls >= b.ToolLimit {
		state.Exceeded = true
		state.Reasons = append(state.Reasons, fmt.Sprintf("工具调用 %d ≥ 上限 %d", toolCalls, b.ToolLimit))
	}
	if b.TokenLimit > 0 && tokens >= b.TokenLimit {
		state.Exceeded = true
		state.Reasons = append(state.Reasons, fmt.Sprintf("Token %d ≥ 上限 %d", tokens, b.TokenLimit))
	}
	return state
}

// reason joins the crossed thresholds for the node error / run event message.
func (s nodeBudgetState) reason() string {
	if len(s.Reasons) == 0 {
		return "未知预算"
	}
	return strings.Join(s.Reasons, "；")
}

// nodeBudgetState evaluates the budget against the node's cumulative counters.
func (e *Engine) nodeBudgetState(nr *domain.NodeRun) nodeBudgetState {
	if nr == nil {
		return nodeBudgetState{}
	}
	return e.nodeBudget.evaluate(nr.ToolCalls, nr.Usage.TotalTokens)
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func envInt64(name string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return value
}
