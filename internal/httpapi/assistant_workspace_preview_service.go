package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"agentflow/internal/domain"
)

const workspacePreviewServiceToolName = "workbench_preview_service"

var workspacePreviewServiceTool = agentToolDescriptor{
	Name:        workspacePreviewServiceToolName,
	Kind:        "workspace",
	Description: "管理 Docker 工作区内跨回合运行的开发服务。用 start 和前台命令分别启动前端和后端，同名服务运行中时复用。用 status 查询进程状态，用 stop 停止。重启或更换命令时先 stop 再 start。用浏览器工具验证端口可访问后把 localhost 地址发给用户。",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"operation":{"type":"string","enum":["start","status","stop"]},"name":{"type":"string","pattern":"^[a-z0-9][a-z0-9-]{0,63}$"},"command":{"type":"string","description":"仅 start 使用。相对于工作区根目录执行的前台启动命令，例如 cd frontend && npm run dev"}},"required":["operation","name"],"additionalProperties":false}`),
}

func (h *agentToolGateway) callWorkspacePreviewService(ctx context.Context, s *AgentToolSession, run domain.Run, args map[string]any) (string, error) {
	if s.RuntimeMode != domain.AssistantModeWorkspace || !h.nativeToolTurnActive(ctx, s) {
		return "", errAgentToolForbidden
	}
	if !run.IsWorkspace() && !sopStopped(run) {
		return "", errors.New("SOP 正在执行，请先暂停后接管工作区")
	}
	for key, value := range args {
		if key != "operation" && key != "name" && key != "command" {
			return "", errors.New("预览服务只接受 operation、name 和 command")
		}
		if _, ok := value.(string); !ok {
			return "", fmt.Errorf("预览服务参数 %s 必须是字符串", key)
		}
	}
	var req domain.WorkspacePreviewServiceRequest
	req.Operation, _ = args["operation"].(string)
	req.Name, _ = args["name"].(string)
	req.Command, _ = args["command"].(string)
	if err := req.Validate(); err != nil {
		return "", err
	}
	if req.Operation == "start" {
		input, _ := json.Marshal(req)
		if label, external := nativeExternalWrite(input); external {
			return "", fmt.Errorf("预览工具仅用于启动开发服务，%s 请使用支持审批的工作区原生命令", label)
		}
	}
	state, err := h.engine.WorkspacePreviewService(ctx, run, req)
	if err != nil {
		return "", errors.New(redactAgentToolResult(err.Error(), s.User))
	}
	raw, err := json.Marshal(state)
	return string(raw), err
}
