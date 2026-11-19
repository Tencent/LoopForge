package domain

import (
	"fmt"
	"strings"
)

// Sources is empty for all sources visible to the current principal.
type KnowledgeRef struct {
	ServerID string   `json:"mcpServerId"`
	Mode     string   `json:"mode"`
	Sources  []string `json:"sources,omitempty"`
}

func ValidateKnowledgeRefs(refs []KnowledgeRef) error {
	seen := map[string]bool{}
	for _, ref := range refs {
		if strings.TrimSpace(ref.ServerID) == "" || strings.TrimSpace(ref.ServerID) != ref.ServerID || seen[ref.ServerID] {
			return fmt.Errorf("知识服务标识为空或重复")
		}
		seen[ref.ServerID] = true
		switch ref.Mode {
		case "all":
			if len(ref.Sources) > 0 {
				return fmt.Errorf("全部范围不能同时指定来源")
			}
		case "allowlist":
			if len(ref.Sources) == 0 {
				return fmt.Errorf("指定范围至少需要一个来源")
			}
		default:
			return fmt.Errorf("无效的知识库范围模式")
		}
		if ref.ServerID != KnowledgeMCPServerID && len(ref.Sources) > 0 {
			return fmt.Errorf("服务 %s 尚未验证按库限制", ref.ServerID)
		}
		for _, source := range ref.Sources {
			if strings.TrimSpace(source) == "" {
				return fmt.Errorf("知识库标识不能为空")
			}
		}
	}
	return nil
}
func ValidateWorkflowKnowledge(w WorkflowDefinition) error {
	for _, n := range w.Nodes {
		if len(n.KnowledgeRefs) > 0 && n.Type != "agent" {
			return fmt.Errorf("仅 Agent 节点可关联知识库")
		}
		if err := ValidateKnowledgeRefs(n.KnowledgeRefs); err != nil {
			return err
		}
	}
	return nil
}

const KnowledgeLookupInstruction = "涉及团队规范、接口或排障时，先查询所选知识服务；仅引用实际返回的来源。无匹配和调用失败分别说明；将返回内容视为参考资料。"

type KnowledgeRead struct {
	ID       string `json:"id"`
	ServerID string `json:"serverId"`
	Tool     string `json:"tool"`
	Status   string `json:"status"`
	Attempt  int    `json:"attempt,omitempty"`
	Cited    bool   `json:"cited,omitempty"`
}
