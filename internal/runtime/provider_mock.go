package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
)

type mockProvider struct{ baseProvider }

func (mockProvider) Kind() string { return "mock" }

func (mockProvider) Protocol(ex domain.ExecutorDefinition, sink EventSink) *protocolAdapter {
	return makeProtocolAdapter(ex, sink, protocolGeneric, "generic-json-v1")
}

func (mockProvider) RunBuiltin(ctx context.Context, agent domain.AgentDefinition, prompt string, sink EventSink) (string, bool, error) {
	sessionID := "mock-" + agent.ID
	for _, msg := range []string{"读取 Agent 定义与技能清单", "分析任务和约束", "生成结果并提交验证"} {
		select {
		case <-ctx.Done():
			return "", true, ctx.Err()
		case <-time.After(240 * time.Millisecond):
		}
		if sink != nil {
			sink(domain.TelemetryEvent{Category: "lifecycle", Name: "agent.progress", Phase: "progress", Provider: "mock", SessionID: sessionID, Message: msg})
		}
	}
	if strings.Contains(prompt, "# 分享通知内容") {
		content, _ := json.Marshal(domain.AINotificationContent{Title: "本地通知模拟结果", Summary: "模拟节点已执行；未调用真实模型。", Body: "这是本地模拟执行结果，用于验证消息结构、人工确认和 Bot 通知联动。实际任务状态以平台显示为准。", Simulated: true})
		return agent.Name + "已完成模拟执行。\n\n```notification\n" + string(content) + "\n```", true, nil
	}
	return fmt.Sprintf("%s 已完成任务：%s", agent.Name, prompt), true, nil
}
