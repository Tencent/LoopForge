package repo

import (
	"agentflow/internal/domain"
	"fmt"
)

func (r *CatalogRepo) validateWorkflowNotifications(w domain.WorkflowDefinition) error {
	runBots := map[string]bool{}
	if len(w.Notifications) > 1 {
		return fmt.Errorf("每个 SOP 只配置一个通知 Bot")
	}
	if len(w.Notifications) > 0 {
		if w.ProjectID == "" || w.Shared || w.Generic {
			return fmt.Errorf("通知绑定只能保存在本项目流程中，请先复制共享模板")
		}
		if err := r.notifications.ValidateNotificationBindings(w.ProjectID, w.Notifications, false); err != nil {
			return err
		}
		for _, b := range w.Notifications {
			if b.Scope != "run" || len(b.Events) != 1 || b.Events[0] != "succeeded" {
				return fmt.Errorf("SOP 通知只支持整个 SOP 完成")
			}
			runBots[b.BotID] = true
		}
	}
	for _, node := range w.Nodes {
		if len(node.Notifications) == 0 {
			continue
		}
		if node.Type != "agent" {
			return fmt.Errorf("只有 Agent 节点可以配置通知")
		}
		if w.ProjectID == "" || w.Shared || w.Generic {
			return fmt.Errorf("通知绑定只能保存在本项目流程中，请先复制共享模板")
		}
		if err := r.notifications.ValidateNotificationBindings(w.ProjectID, node.Notifications, false); err != nil {
			return err
		}
		for _, binding := range node.Notifications {
			if binding.Scope == "run" {
				if runBots[binding.BotID] {
					return fmt.Errorf("同一 Bot 的流程范围通知只能配置一次")
				}
				runBots[binding.BotID] = true
			}
		}
	}
	return nil
}
