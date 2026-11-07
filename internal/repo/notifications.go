package repo

import (
	"agentflow/internal/domain"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type NotificationsRepo struct {
	*deps
	users        *UsersRepo
	projects     *ProjectsRepo
	issues       *IssuesRepo
	interactions *BotInteractionsRepo
}
type notificationBotRow struct {
	ID        string `gorm:"primaryKey;size:191"`
	ProjectID string `gorm:"size:191;index"`
	Payload   []byte `gorm:"type:json"`
}

func (notificationBotRow) TableName() string { return "notification_bots" }
func (r *NotificationsRepo) NotificationBots(projectID string) ([]domain.NotificationBot, error) {
	var rows []notificationBotRow
	if err := r.DB.Where("project_id = ?", projectID).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.NotificationBot, 0, len(rows))
	for _, row := range rows {
		var b domain.NotificationBot
		if err := json.Unmarshal(row.Payload, &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}
func (r *NotificationsRepo) NotificationBot(id string) (domain.NotificationBot, error) {
	var row notificationBotRow
	if err := r.DB.Where("id = ?", id).First(&row).Error; err != nil {
		return domain.NotificationBot{}, err
	}
	var b domain.NotificationBot
	err := json.Unmarshal(row.Payload, &b)
	return b, err
}

// SaveNotificationBot preserves creation metadata and atomically replaces credentials when provided.
func (r *NotificationsRepo) SaveNotificationBot(b domain.NotificationBot, secret string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var old notificationBotRow
		err := r.locking(tx).Where("id = ?", b.ID).First(&old).Error
		if err == nil {
			var prior domain.NotificationBot
			if err = json.Unmarshal(old.Payload, &prior); err != nil {
				return err
			}
			b.CreatedAt = prior.CreatedAt
			b.OwnerID = prior.OwnerID
			b.ProjectID = prior.ProjectID
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		if b.CreatedAt.IsZero() {
			b.CreatedAt = time.Now().UTC()
		}
		if b.Enabled && b.InteractionEnabled {
			if err := r.interactions.lockBotDefinition(tx, b.BotID); err != nil {
				return err
			}
			var peers []notificationBotRow
			if err := tx.Where("id <> ?", b.ID).Find(&peers).Error; err != nil {
				return err
			}
			for _, peer := range peers {
				var other domain.NotificationBot
				if err := json.Unmarshal(peer.Payload, &other); err != nil {
					return err
				}
				if other.Enabled && other.InteractionEnabled && other.BotID == b.BotID {
					return ErrInteractiveBotAlreadyExists
				}
			}
		}
		b.UpdatedAt = time.Now().UTC()
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&notificationBotRow{ID: b.ID, ProjectID: b.ProjectID, Payload: mustJSON(b)}).Error; err != nil {
			return err
		}
		if secret != "" {
			return r.users.putBotCredential(tx, b.ID, secret)
		}
		return nil
	})
}
func (r *NotificationsRepo) SetNotificationBotHealth(id, health string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row notificationBotRow
		if err := r.locking(tx).Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		var b domain.NotificationBot
		if err := json.Unmarshal(row.Payload, &b); err != nil {
			return err
		}
		now := time.Now().UTC()
		b.Health = health
		b.LastCheckedAt = &now
		return tx.Model(&row).Update("payload", mustJSON(b)).Error
	})
}
func (r *NotificationsRepo) DeleteNotificationBot(id string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", id).Delete(&notificationBotRow{}).Error; err != nil {
			return err
		}
		if err := r.users.deleteBotCredential(tx, id); err != nil {
			return err
		}
		return tx.Model(&domain.NotificationDelivery{}).Where("bot_id = ? AND status <> ?", id, "sent").Updates(map[string]any{"status": "cancelled", "lease_token": "", "lease_until": nil}).Error
	})
}
func (r *NotificationsRepo) NotificationDeliveries(projectID string) ([]domain.NotificationDelivery, error) {
	rows := []domain.NotificationDelivery{}
	err := r.DB.Where("project_id = ?", projectID).Order("created_at desc").Limit(100).Find(&rows).Error
	return rows, err
}
func (r *NotificationsRepo) ClaimNotification(now time.Time, token string) (*domain.NotificationDelivery, error) {
	var row domain.NotificationDelivery
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		q := tx.Where("(status = ? AND next_attempt_at <= ?) OR (status = ? AND lease_until < ?)", "pending", now, "sending", now).Order("created_at").Limit(1)
		if r.isMySQL() {
			q = q.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}
		if err := q.First(&row).Error; err != nil {
			return err
		}
		until := now.Add(2 * time.Minute)
		if row.ReportState == "pending" {
			until = now.Add(5 * time.Minute)
		}
		result := tx.Model(&domain.NotificationDelivery{}).Where("id = ? AND status = ? AND attempts = ?", row.ID, row.Status, row.Attempts).Updates(map[string]any{"status": "sending", "lease_until": until, "lease_token": token, "attempts": row.Attempts + 1})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		row.Status = "sending"
		row.LeaseToken = token
		row.LeaseUntil = &until
		row.Attempts++
		return nil
	})
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &row, err
}
func (r *NotificationsRepo) FinishNotification(item domain.NotificationDelivery, status, detail string, next time.Time) error {
	updates := map[string]any{"status": status, "last_error": detail, "next_attempt_at": next, "lease_until": nil, "lease_token": ""}
	if status == "sent" {
		updates["sent_at"] = time.Now().UTC()
	}
	return r.DB.Model(&domain.NotificationDelivery{}).Where("id = ? AND status = ? AND lease_token = ?", item.ID, "sending", item.LeaseToken).Updates(updates).Error
}
func (r *NotificationsRepo) RetryNotification(projectID, id string) error {
	result := r.DB.Model(&domain.NotificationDelivery{}).Where("id = ? AND project_id = ? AND (content_source IS NULL OR content_source <> 'invalid') AND status IN ?", id, projectID, []string{"failed", "awaiting_authorization", "target_unavailable"}).Updates(map[string]any{"status": "pending", "attempts": 0, "next_attempt_at": time.Now().UTC(), "last_error": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// enqueueRunNotifications shares the run write transaction and freezes rendered content.
func (r *NotificationsRepo) enqueueRunNotifications(tx *gorm.DB, run domain.Run, event domain.RunEvent, seq int64) error {
	projectID := notificationRunProject(run)
	nodeTrigger := notificationTrigger(event.Type)
	runTrigger := map[string]string{"run.succeeded": "succeeded", "run.failed": "failed"}[event.Type]
	if nodeTrigger == "" && runTrigger == "" {
		return nil
	}
	nodes := append([]domain.WorkflowNode(nil), run.WorkflowSnapshot.Nodes...)
	nodes = append(nodes, domain.WorkflowNode{Type: "agent", Notifications: domain.WorkflowNotifications(run.WorkflowSnapshot)})
	for _, node := range nodes {
		bindings := domain.NodeReminderBindings(domain.WorkflowNotifications(run.WorkflowSnapshot))
		if node.ID == "" {
			bindings = node.Notifications
			if runTrigger == "failed" {
				// Setup/runtime failures may end a SOP without a node failure event.
				hasNodeFailure := false
				for _, prior := range run.Events {
					if notificationTrigger(prior.Type) == "failed" && prior.NodeID != "" {
						hasNodeFailure = true
						break
					}
				}
				if !hasNodeFailure {
					bindings = domain.NodeReminderBindings(node.Notifications)
					for i := range bindings {
						bindings[i].Scope = "run"
						bindings[i].Events = []string{"failed"}
					}
				}
			}
		}
		for _, binding := range bindings {
			trigger := nodeTrigger
			nodeID := node.ID
			labelPrefix := "节点"
			if binding.Scope == "run" {
				trigger = runTrigger
				nodeID = ""
				labelPrefix = "SOP"
			} else if event.NodeID != node.ID {
				continue
			}
			if trigger == "" || !notificationEventSelected(binding, trigger) {
				continue
			}
			values := map[string]string{"project": r.notificationProjectName(tx, projectID), "workflow": run.WorkflowSnapshot.Name, "node": node.Name, "input": run.Input, "status": notificationStatus(trigger, labelPrefix), "issue.title": "未关联需求"}
			if run.ExecutionContext.IssueID != "" {
				if issue, ok := r.issues.issueInTx(tx, run.ExecutionContext.IssueID); ok && issue.ProjectID == projectID {
					values["issue.title"] = issue.Title
					values["issue.id"] = issue.ID
				}
			}
			values["error"] = event.Message
			result := ""
			attempt, rework := 0, 0
			if nr := run.Nodes[node.ID]; nr != nil {
				result = nr.Output
				if nr.Error != "" {
					values["error"] = nr.Error
				}
				attempt = nr.Attempt
				rework = nr.ReworkCount
			}
			sourceName := node.Name
			if binding.Scope == "run" {
				sourceName = run.WorkflowSnapshot.Name
				result = domain.BuildWorkflowNotificationResult(run)
				values["node"] = "全部节点"
			}
			item := domain.NotificationDelivery{ProjectID: projectID, BotID: binding.BotID, RunID: run.ID, NodeID: nodeID, EventType: trigger, Attempt: attempt, ReworkCount: rework, SourceType: "run", SourceID: run.ID, SourceName: sourceName}
			key := fmt.Sprintf("run:%s:%d:%s", run.ID, seq, binding.BotID)
			context := domain.NotificationContext{Values: values, Result: result, Fallback: sourceName + "：" + values["status"] + "。请在平台查看详情。", SourceType: "run"}
			if err := r.enqueueNotification(tx, key, binding, context, item); err != nil {
				return err
			}
		}
	}
	return nil
}
func (r *NotificationsRepo) enqueueTaskNotifications(tx *gorm.DB, sourceType, id, projectID, name, trigger string, at time.Time, bindings []domain.NotificationBinding, result, errorText string) error {
	for _, binding := range bindings {
		if !notificationEventSelected(binding, trigger) {
			continue
		}
		values := map[string]string{"project": r.notificationProjectName(tx, projectID), "task.name": name, "status": notificationStatus(trigger, "任务"), "error": errorText, "issue.title": "未关联需求"}
		item := domain.NotificationDelivery{ProjectID: projectID, BotID: binding.BotID, EventType: trigger, SourceType: sourceType, SourceID: id, SourceName: name}
		key := fmt.Sprintf("%s:%s:%d:%s:%s", sourceType, id, at.UTC().UnixMilli(), trigger, binding.BotID)
		context := domain.NotificationContext{Values: values, Result: result, Fallback: name + "：" + values["status"] + "。请在平台查看详情。", SourceType: sourceType}
		if err := r.enqueueNotification(tx, key, binding, context, item); err != nil {
			return err
		}
	}
	return nil
}
func (r *NotificationsRepo) notificationProjectName(tx *gorm.DB, id string) string {
	if project, ok := r.projects.projectInTx(tx, id); ok {
		return project.Name
	}
	return id
}
func notificationEventSelected(binding domain.NotificationBinding, event string) bool {
	for _, selected := range binding.Events {
		if selected == event {
			return true
		}
	}
	return false
}
func notificationStatus(trigger, scope string) string {
	switch trigger {
	case "succeeded":
		return scope + "已完成"
	case "failed":
		return scope + "失败"
	default:
		return "需要人工处理"
	}
}
func (r *NotificationsRepo) enqueueNotification(tx *gorm.DB, key string, binding domain.NotificationBinding, context domain.NotificationContext, item domain.NotificationDelivery) error {
	var row notificationBotRow
	if err := tx.Where("id = ?", binding.BotID).First(&row).Error; err == gorm.ErrRecordNotFound {
		return nil
	} else if err != nil {
		return err
	}
	var bot domain.NotificationBot
	if err := json.Unmarshal(row.Payload, &bot); err != nil {
		return err
	}
	if !bot.Enabled || bot.Target == nil || bot.ProjectID != item.ProjectID {
		return nil
	}
	sum := sha256.Sum256([]byte(key))
	item.ID = hex.EncodeToString(sum[:])
	item.TargetID = bot.Target.ID
	item.TargetName = bot.Target.Name
	item.TargetType = bot.Target.Type
	content, source, renderErr := domain.RenderNotification(binding, context)
	item.WaitingPolicy = binding.WaitingPolicy
	if item.WaitingPolicy == "" {
		item.WaitingPolicy = "actionable"
	}
	item.Content = content
	item.ContentSource = source
	item.Status = "pending"
	if renderErr != nil {
		item.Status = "failed"
		item.LastError = renderErr.Error()
		item.ContentSource = "invalid"
	}
	if item.Status == "pending" && item.SourceType == "run" && item.NodeID == "" && item.EventType == "succeeded" && domain.NotificationUsesAI(binding) {
		request, err := json.Marshal(domain.NotificationReportRequest{Binding: binding, Context: context})
		if err != nil {
			return err
		}
		item.ReportRequest = string(request)
		item.ReportState = "pending"
		item.ReportModel = bot.Model
		if item.ReportModel == "" {
			item.ReportModel = domain.DefaultNotificationReportModel
		}
	}
	item.CreatedAt = time.Now().UTC()
	item.NextAttemptAt = item.CreatedAt
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error
}
func (r *NotificationsRepo) ValidateNotificationBindings(projectID string, bindings []domain.NotificationBinding, task bool) error {
	return r.validateNotificationBindings(r.DB, projectID, bindings, task)
}
func (r *NotificationsRepo) validateNotificationBindings(db *gorm.DB, projectID string, bindings []domain.NotificationBinding, task bool) error {
	if len(bindings) > 3 {
		return fmt.Errorf("最多配置 3 个通知 Bot")
	}
	seen := map[string]bool{}
	for _, binding := range bindings {
		if binding.BotID == "" || seen[binding.BotID] {
			return fmt.Errorf("通知 Bot 不能为空或重复")
		}
		seen[binding.BotID] = true
		if err := domain.ValidateNotificationBinding(binding); err != nil {
			return err
		}
		if task && (binding.Scope == "run" || notificationEventSelected(binding, "waiting")) {
			return fmt.Errorf("定时任务通知仅支持成功或失败")
		}
		var row notificationBotRow
		err := db.Where("id = ?", binding.BotID).First(&row).Error
		var bot domain.NotificationBot
		if err == nil {
			err = json.Unmarshal(row.Payload, &bot)
		}
		if err != nil || bot.ProjectID != projectID {
			return fmt.Errorf("通知 Bot 不属于当前项目")
		}
		if bot.Target == nil {
			return fmt.Errorf("请先为通知 Bot 绑定接收会话")
		}
	}
	return nil
}

func notificationRunProject(run domain.Run) string {
	if run.ExecutionContext.ProjectID != "" {
		return run.ExecutionContext.ProjectID
	}
	return run.WorkflowSnapshot.ProjectID
}
func (r *NotificationsRepo) CreateNotificationDelivery(item domain.NotificationDelivery) error {
	return r.DB.Create(&item).Error
}

func notificationTrigger(eventType string) string {
	switch eventType {
	case "node.succeeded", "gate.approved":
		return "succeeded"
	case "node.failed", "agent.turns.declined":
		return "failed"
	case "approval.waiting", "gate.waiting", "agent.turns.exhausted", "agent.loop.exhausted", "node.paused":
		return "waiting"
	default:
		return ""
	}
}

func (r *NotificationsRepo) InteractiveNotificationBots() ([]domain.NotificationBot, error) {
	var rows []notificationBotRow
	if err := r.DB.Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := []domain.NotificationBot{}
	for _, row := range rows {
		var b domain.NotificationBot
		if err := json.Unmarshal(row.Payload, &b); err != nil {
			return nil, err
		}
		if b.Enabled && b.InteractionEnabled {
			out = append(out, b)
		}
	}
	return out, nil
}

func (r *NotificationsRepo) NotificationDelivery(id string) (domain.NotificationDelivery, error) {
	var item domain.NotificationDelivery
	err := r.DB.First(&item, "id = ?", id).Error
	return item, err
}

var ErrInteractiveBotAlreadyExists = fmt.Errorf("同一企业微信 Bot 只能有一个启用的对话配置")
