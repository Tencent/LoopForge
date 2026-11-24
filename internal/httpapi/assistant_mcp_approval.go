package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
)

var errMCPApprovalExpired = errors.New("本次外部 MCP 调用已结束，审批已失效，操作未执行")

func (h *agentToolGateway) waitForMCPWriteApproval(ctx context.Context, session *AgentToolSession, name string, call assistantMCPCall) error {
	if ctx.Value(legacyRunnerToolRequestKey{}) != nil {
		return errors.New("请先升级执行机器的 Runner，再使用外部 MCP 人工审批")
	}
	if h.deps == nil || h.repo == nil || session.ctx == nil || session.ChatSessionID == "" || session.ChatOwnerID == 0 {
		return errors.New("外部 MCP 写入需要有效的会话审批上下文")
	}
	generation := assistantControlScopeFrom(session.ctx).GenerationID
	if generation == "" || !session.nativeToolPhaseActive(ctx) {
		return errMCPApprovalExpired
	}
	if _, ok := h.lookupServer(session, call.Server); !ok {
		return errors.New("当前会话没有可调用的外部 MCP 服务")
	}

	// Only this waiting request can execute the call. Every transport retry
	// needs its own approval, and the browser never sends execution arguments.
	id, err := newAgentToolSessionToken()
	if err != nil {
		return fmt.Errorf("无法创建外部 MCP 审批标识: %w", err)
	}
	input, err := json.Marshal(call)
	if err != nil {
		return fmt.Errorf("外部 MCP 审批参数无效: %w", err)
	}
	phase := nativeApprovalPhase(session.Token)
	shown, hiddenFields := redactNativeApprovalInputFields(input, session.User)
	summary := mcpServerName(h, session, call.Server) + " · " + name
	if len(hiddenFields) > 0 {
		// The approver must know the card does not show everything that will
		// be sent upstream.
		summary += fmt.Sprintf("（%d 个字段已隐藏：%s）", len(hiddenFields), truncateRunes(strings.Join(hiddenFields, "、"), 120))
	}
	item := domain.AssistantNativeApproval{
		ID:           id,
		GenerationID: generation,
		ActorID:      session.UserID,
		Tool:         name,
		Summary:      redactAgentToolResult(summary, session.User),
		Input:        shown,
	}
	if err := h.repo.CreateNativeApproval(session.ChatOwnerID, session.ChatSessionID, phase, item); err != nil {
		return fmt.Errorf("无法保存外部 MCP 审批请求: %w", err)
	}
	defer func() {
		_ = h.repo.ReleaseNativeApproval(session.ChatOwnerID, session.ChatSessionID, generation, phase, id)
	}()

	wait, cancel := context.WithTimeout(ctx, nativeApprovalWait)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if errors.Is(wait.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return errors.New("等待批准超时，外部 MCP 操作未执行")
		}
		if !session.nativeToolPhaseActive(wait) {
			return errMCPApprovalExpired
		}
		current, ok := h.repo.NativeApproval(session.ChatOwnerID, session.ChatSessionID, id)
		if !ok || current.GenerationID != generation {
			return errMCPApprovalExpired
		}
		switch current.Decision {
		case "allow":
			if !session.nativeToolPhaseActive(wait) {
				return errMCPApprovalExpired
			}
			return nil
		case "deny":
			return fmt.Errorf("用户拒绝了本次外部 MCP 写入 %s，操作未执行。不要换一种方式重试。", name)
		}
		select {
		case <-wait.Done():
		case <-ticker.C:
		}
	}
}
