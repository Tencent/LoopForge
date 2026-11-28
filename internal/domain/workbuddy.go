package domain

import (
	"fmt"
	"regexp"
	"strings"
)

const WorkBuddyExecutorID = "workbuddy"
const WorkflowNodeWorkBuddy = WorkBuddyExecutorID
const DefaultWorkBuddyRegion = "ap-beijing"

var workBuddyRegionPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,49}$`)

type WorkBuddyConfig struct {
	AgentID      string `json:"agentId"`
	VersionID    string `json:"versionId,omitempty"`
	Region       string `json:"region,omitempty"`
	CredentialID string `json:"credentialId"`
}

func (c WorkBuddyConfig) EffectiveRegion() string {
	if region := strings.TrimSpace(c.Region); region != "" {
		return region
	}
	return DefaultWorkBuddyRegion
}

func (c WorkBuddyConfig) ValidateRegion() error {
	if !workBuddyRegionPattern.MatchString(c.EffectiveRegion()) {
		return fmt.Errorf("腾讯云地域无效")
	}
	return nil
}

func (n WorkflowNode) ValidateWorkBuddy() error {
	if n.Type != WorkflowNodeWorkBuddy {
		return nil
	}
	if n.WorkBuddy == nil || strings.TrimSpace(n.WorkBuddy.AgentID) == "" || strings.TrimSpace(n.WorkBuddy.CredentialID) == "" || strings.TrimSpace(n.TaskPrompt) == "" {
		return fmt.Errorf("WorkBuddy 节点 %s 需要填写 Agent ID、项目 AK/SK 凭据和任务提示词", n.ID)
	}
	if err := n.WorkBuddy.ValidateRegion(); err != nil {
		return fmt.Errorf("WorkBuddy 节点 %s：%w", n.ID, err)
	}
	if strings.TrimSpace(n.ExecutionScope) == ExecutionScopeRepositories {
		return fmt.Errorf("WorkBuddy 节点 %s 以运行作为执行范围", n.ID)
	}
	return nil
}

func (a AgentDefinition) ValidateWorkBuddy() error {
	if a.ExecutorID != WorkBuddyExecutorID {
		return nil
	}
	if a.ProjectID == "" || a.Shared {
		return fmt.Errorf("请选择项目内私有角色来绑定 WorkBuddy 的项目凭据")
	}
	if a.WorkBuddy == nil || strings.TrimSpace(a.WorkBuddy.AgentID) == "" || strings.TrimSpace(a.WorkBuddy.CredentialID) == "" {
		return fmt.Errorf("WorkBuddy 角色需要填写 Agent ID 并选择项目 AK/SK 凭据")
	}
	if err := a.WorkBuddy.ValidateRegion(); err != nil {
		return err
	}
	if err := ValidateAgentEffort(a.Effort, a.ExecutorID); err != nil {
		return err
	}
	if len(a.Skills)+len(a.MCPServers)+len(a.Hooks) > 0 || a.PlatformMCP || a.KnowledgeBase {
		return fmt.Errorf("请在 WorkBuddy Agent 版本中配置技能和连接器，并清空角色里的技能、MCP 与 Hook 绑定")
	}
	if a.Loop.Enabled {
		return fmt.Errorf("WorkBuddy 角色使用流程节点的返工与重试策略，请关闭角色自修正")
	}
	for _, gate := range a.Verification {
		if gate.Mode != "manual" {
			return fmt.Errorf("WorkBuddy 角色支持 Gate 回传与人工复核，请移除角色门禁 %q", gate.Name)
		}
	}
	return nil
}

// ProjectAKSK holds decrypted values for server-side integrations only.
type ProjectAKSK struct {
	SecretID  string `json:"-"`
	SecretKey string `json:"-"`
	Token     string `json:"-"`
}
