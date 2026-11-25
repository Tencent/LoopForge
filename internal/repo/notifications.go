package repo

import (
	"agentflow/internal/domain"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"slices"
	"strings"
	"time"
)

type NotificationsRepo struct {
	*deps
	users        *UsersRepo
	projects     *ProjectsRepo
	issues       *IssuesRepo
	interactions *BotInteractionsRepo
}

// notificationBotRow keeps the Bot entity in a JSON payload. Channel, app
// identity, tenant, receive slot, enable flags and versions are physical
// columns: lease fencing and uniqueness read only these columns, and they are
// written in the same statement as the payload.
type notificationBotRow struct {
	ID                 string  `gorm:"primaryKey;size:191"`
	ProjectID          string  `gorm:"size:191;index"`
	Payload            []byte  `gorm:"type:json"`
	Channel            string  `gorm:"size:16;not null;default:''"`
	ExternalAppID      string  `gorm:"size:191;not null;default:''"`
	TenantKey          string  `gorm:"size:191;not null;default:''"`
	ReceiveSlot        *string `gorm:"size:191;uniqueIndex:uq_notification_bots_receive_slot"`
	Enabled            bool    `gorm:"not null;default:false"`
	InteractionEnabled bool    `gorm:"not null;default:false"`
	ConfigVersion      int64   `gorm:"not null;default:0"`
	CredentialVersion  int64   `gorm:"not null;default:0"`
}

func (notificationBotRow) TableName() string { return "notification_bots" }

var (
	// ErrBotConfigChanged: the expected configVersion no longer matches.
	ErrBotConfigChanged = errors.New("config_changed")
	// ErrBotImmutableField: channel, project or (for new channels) the app
	// identity cannot change; create a new configuration instead.
	ErrBotImmutableField = errors.New("机器人渠道、项目和应用标识创建后不可修改，请新建配置")
)

func (row notificationBotRow) bot() (domain.NotificationBot, error) {
	var b domain.NotificationBot
	if err := json.Unmarshal(row.Payload, &b); err != nil {
		return b, err
	}
	// Versions are authoritative in physical columns.
	b.ConfigVersion, b.CredentialVersion = row.ConfigVersion, row.CredentialVersion
	return b, nil
}

func botRow(b domain.NotificationBot) notificationBotRow {
	var slot *string
	if b.Enabled && b.InteractionEnabled {
		v := b.ReceiveSlot()
		slot = &v
	}
	tenant := ""
	if b.TenantState == domain.BotTenantLocked {
		tenant = b.TenantKey
	}
	b.CredentialFormat = ""
	return notificationBotRow{ID: b.ID, ProjectID: b.ProjectID, Payload: mustJSON(b), Channel: b.ChannelKind(), ExternalAppID: b.BotID, TenantKey: tenant, ReceiveSlot: slot, Enabled: b.Enabled, InteractionEnabled: b.InteractionEnabled, ConfigVersion: b.ConfigVersion, CredentialVersion: b.CredentialVersion}
}

func (r *NotificationsRepo) decodeBots(rows []notificationBotRow) ([]domain.NotificationBot, error) {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	formats := r.users.botCredentialFormats(ids)
	out := make([]domain.NotificationBot, 0, len(rows))
	for _, row := range rows {
		b, err := row.bot()
		if err != nil {
			return nil, err
		}
		b.CredentialFormat = formats[row.ID]
		if b.Target != nil && b.ChannelKind() != domain.BotChannelWeCom {
			// The verification lives on the target's binding: revocation
			// or re-binding clears it, so read it instead of the snapshot.
			target := *b.Target
			target.VerifiedAt = nil
			if binding, err := r.BotPushTarget(b, target.ID); err == nil {
				target.VerifiedAt = binding.TargetVerifiedAt
			}
			b.Target = &target
		}
		out = append(out, b)
	}
	return out, nil
}

func (r *NotificationsRepo) NotificationBots(projectID string) ([]domain.NotificationBot, error) {
	var rows []notificationBotRow
	if err := r.DB.Where("project_id = ?", projectID).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	return r.decodeBots(rows)
}
func (r *NotificationsRepo) NotificationBot(id string) (domain.NotificationBot, error) {
	var row notificationBotRow
	if err := r.DB.Where("id = ?", id).First(&row).Error; err != nil {
		return domain.NotificationBot{}, err
	}
	bots, err := r.decodeBots([]notificationBotRow{row})
	if err != nil {
		return domain.NotificationBot{}, err
	}
	return bots[0], nil
}

// SaveNotificationBot saves without an expected version (internal callers
// and tests). HTTP writers use SaveNotificationBotChecked.
func (r *NotificationsRepo) SaveNotificationBot(b domain.NotificationBot, secret string) error {
	_, err := r.SaveNotificationBotChecked(b, secret, nil)
	return err
}

// SaveNotificationBotChecked preserves creation metadata and tenant state,
// enforces immutable fields and the expected configVersion, bumps versions,
// keeps one receiving configuration per application, and atomically replaces
// the sealed credential when a secret is provided.
func (r *NotificationsRepo) SaveNotificationBotChecked(b domain.NotificationBot, secret string, expectedVersion *int64) (domain.NotificationBot, error) {
	input := b
	err := retryBotDeadlock(func() error { b = input; return r.saveNotificationBot(&b, secret, expectedVersion) })
	if err != nil {
		return domain.NotificationBot{}, err
	}
	return b, nil
}

func (r *NotificationsRepo) saveNotificationBot(out *domain.NotificationBot, secret string, expectedVersion *int64) error {
	b := *out
	defer func() { *out = b }()
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var old notificationBotRow
		err := r.locking(tx).Where("id = ?", b.ID).First(&old).Error
		if err == nil {
			prior, err := old.bot()
			if err != nil {
				return err
			}
			if expectedVersion != nil && *expectedVersion != old.ConfigVersion {
				return ErrBotConfigChanged
			}
			if prior.ChannelKind() != b.ChannelKind() || (prior.ChannelKind() != domain.BotChannelWeCom && prior.BotID != b.BotID) {
				return ErrBotImmutableField
			}
			b.CreatedAt = prior.CreatedAt
			b.OwnerID = prior.OwnerID
			b.ProjectID = prior.ProjectID
			b.Channel = prior.Channel
			expected, expectedBy := b.TenantExpected, b.TenantExpectedBy
			b.TenantKey, b.TenantName, b.TenantState, b.TenantCandidate = prior.TenantKey, prior.TenantName, prior.TenantState, prior.TenantCandidate
			b.TenantConfirmedBy, b.TenantConfirmedAt = prior.TenantConfirmedBy, prior.TenantConfirmedAt
			b.TenantExpected, b.TenantExpectedBy = prior.TenantExpected, prior.TenantExpectedBy
			// An administrator may correct the expected enterprise (for
			// example after a conflict) until the scope is locked; the
			// next trusted event is then checked afresh.
			if expected != "" && expected != prior.TenantExpected && prior.TenantState != domain.BotTenantLocked {
				b.TenantExpected, b.TenantExpectedBy = expected, expectedBy
				b.TenantCandidate, b.TenantState = nil, domain.BotTenantAwaiting
			}
			b.ConfigVersion = old.ConfigVersion + 1
			b.CredentialVersion = old.CredentialVersion
			if prior.Enabled && prior.InteractionEnabled && !(b.Enabled && b.InteractionEnabled) {
				if err := r.interactions.stopBotInteractions(tx, b.ID, prior.LeaseKey(), false); err != nil {
					return err
				}
			}
			if prior.Enabled && !b.Enabled {
				if err := r.stopNotificationDeliveries(tx, b.ID, false); err != nil {
					return err
				}
			}
			if !(b.Enabled && b.InteractionEnabled) || b.EffectiveWriteApproval() != domain.BotWriteConfirm {
				if err := cancelBotDraftsWhere(tx, "机器人已停用对话或不再使用网页确认", "config_id = ?", b.ID); err != nil {
					return err
				}
			}
		} else if err != gorm.ErrRecordNotFound {
			return err
		} else {
			b.ConfigVersion = 1
			b.CredentialVersion = 0
			if b.ChannelKind() != domain.BotChannelWeCom && b.TenantState == "" {
				b.TenantState = domain.BotTenantAwaiting
			}
		}
		if secret != "" {
			b.CredentialVersion++
		}
		if b.CreatedAt.IsZero() {
			b.CreatedAt = time.Now().UTC()
		}
		if b.Enabled && b.InteractionEnabled {
			// Serializes registering the same application; the receiver
			// later claims this connection row.
			if err := r.interactions.lockBotDefinition(tx, b.LeaseKey()); err != nil {
				return err
			}
			var taken int64
			if err := tx.Model(&notificationBotRow{}).Where("receive_slot = ? AND id <> ?", b.ReceiveSlot(), b.ID).Count(&taken).Error; err != nil {
				return err
			}
			if taken > 0 || r.legacyInteractivePeer(tx, b) {
				return ErrInteractiveBotAlreadyExists
			}
		}
		b.UpdatedAt = time.Now().UTC()
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(ptr(botRow(b))).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrInteractiveBotAlreadyExists
			}
			return err
		}
		if secret != "" {
			return r.users.putBotCredential(tx, b.ID, b.ChannelKind(), secret)
		}
		return nil
	})
}

// legacyInteractivePeer covers rows not yet backfilled by migration 036
// (receive_slot NULL, config_version 0) so the uniqueness rule holds during
// the upgrade window as well.
func (r *NotificationsRepo) legacyInteractivePeer(tx *gorm.DB, b domain.NotificationBot) bool {
	var peers []notificationBotRow
	tx.Where("id <> ? AND config_version = 0", b.ID).Find(&peers)
	for _, peer := range peers {
		other, err := peer.bot()
		if err == nil && other.Enabled && other.InteractionEnabled && other.ReceiveSlot() == b.ReceiveSlot() {
			return true
		}
	}
	return false
}

func ptr[T any](v T) *T { return &v }

// ErrBotTenantCandidate: the confirmation does not match the recorded
// candidate, its credential version, or the current state.
var ErrBotTenantCandidate = errors.New("tenant_candidate_mismatch")

// RecordBotTenantCandidate stores the enterprise scope seen on a trusted
// event of an unlocked configuration. It never changes config_version, so an
// administrator page holding the version stays valid and the receiver does
// not reconnect. A different second candidate moves the state to conflict.
func (r *NotificationsRepo) RecordBotTenantCandidate(id, tenantKey, eventID string, credentialVersion int64) (string, error) {
	state := ""
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var row notificationBotRow
		if err := r.locking(tx).Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		b, err := row.bot()
		if err != nil {
			return err
		}
		state = b.TenantState
		if b.TenantState != domain.BotTenantLocked && b.TenantExpected != "" {
			return r.checkExpectedTenant(tx, row, b, tenantKey, eventID, credentialVersion, &state)
		}
		switch {
		case b.TenantState == domain.BotTenantLocked:
			return nil
		case b.TenantCandidate == nil || b.TenantCandidate.CredentialVersion != credentialVersion:
			b.TenantCandidate = &domain.TenantCandidate{TenantKey: tenantKey, EventID: truncateBotDetail(eventID), ReceivedAt: time.Now().UTC(), CredentialVersion: credentialVersion}
			b.TenantState = domain.BotTenantAwaiting
		case b.TenantCandidate.TenantKey == tenantKey:
			return nil
		default:
			b.TenantCandidate.Conflict = tenantKey
			b.TenantState = domain.BotTenantConflict
		}
		state = b.TenantState
		b.CredentialFormat = ""
		return tx.Model(&notificationBotRow{}).Where("id = ?", id).Update("payload", mustJSON(b)).Error
	})
	return state, err
}

// checkExpectedTenant handles a configuration whose enterprise was entered by
// an administrator (DingTalk CorpId): a trusted event from that enterprise
// locks the scope at once, attributed to the administrator who entered it,
// and bumps config_version (not the connection fingerprint); an event from
// another enterprise records a conflict and is not processed.
func (r *NotificationsRepo) checkExpectedTenant(tx *gorm.DB, row notificationBotRow, b domain.NotificationBot, tenantKey, eventID string, credentialVersion int64, state *string) error {
	if b.TenantState == domain.BotTenantConflict {
		// Two enterprises were seen; only an administrator correction or
		// rediscovery reopens the check.
		return nil
	}
	at := time.Now().UTC()
	b.CredentialFormat = ""
	if tenantKey == b.TenantExpected {
		b.TenantCandidate = &domain.TenantCandidate{TenantKey: tenantKey, EventID: truncateBotDetail(eventID), ReceivedAt: at, CredentialVersion: credentialVersion}
		b.TenantKey, b.TenantState, b.TenantConfirmedBy, b.TenantConfirmedAt = tenantKey, domain.BotTenantLocked, b.TenantExpectedBy, &at
		b.ConfigVersion = row.ConfigVersion + 1
		*state = domain.BotTenantLocked
		return tx.Model(&notificationBotRow{}).Where("id = ?", row.ID).Updates(map[string]any{"payload": mustJSON(b), "tenant_key": tenantKey, "config_version": b.ConfigVersion}).Error
	}
	b.TenantCandidate = &domain.TenantCandidate{TenantKey: b.TenantExpected, EventID: truncateBotDetail(eventID), ReceivedAt: at, CredentialVersion: credentialVersion, Conflict: tenantKey}
	b.TenantState = domain.BotTenantConflict
	*state = domain.BotTenantConflict
	return tx.Model(&notificationBotRow{}).Where("id = ?", row.ID).Update("payload", mustJSON(b)).Error
}

// ConfirmBotTenant locks the candidate enterprise scope. The candidate must
// match the posted value and come from the current credential version; the
// expected configVersion guards concurrent edits. Locking bumps
// config_version but not the connection fingerprint.
func (r *NotificationsRepo) ConfirmBotTenant(id, tenantKey string, expectedVersion int64, actor int64) (domain.NotificationBot, error) {
	var b domain.NotificationBot
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var row notificationBotRow
		if err := r.locking(tx).Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		var err error
		if b, err = row.bot(); err != nil {
			return err
		}
		if row.ConfigVersion != expectedVersion {
			return ErrBotConfigChanged
		}
		c := b.TenantCandidate
		if b.TenantState != domain.BotTenantAwaiting || c == nil || c.Conflict != "" || c.TenantKey != tenantKey || c.CredentialVersion != row.CredentialVersion {
			return ErrBotTenantCandidate
		}
		at := time.Now().UTC()
		b.TenantKey, b.TenantState, b.TenantConfirmedBy, b.TenantConfirmedAt = tenantKey, domain.BotTenantLocked, actor, &at
		b.ConfigVersion = row.ConfigVersion + 1
		b.UpdatedAt = at
		return tx.Model(&notificationBotRow{}).Where("id = ?", id).Updates(map[string]any{"payload": mustJSON(botPayload(b)), "tenant_key": tenantKey, "config_version": b.ConfigVersion}).Error
	})
	return b, err
}

// SetBotTenantName stores the display-only enterprise name.
func (r *NotificationsRepo) SetBotTenantName(id, name string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row notificationBotRow
		if err := r.locking(tx).Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		b, err := row.bot()
		if err != nil {
			return err
		}
		b.TenantName = truncateBotDetail(name)
		return tx.Model(&notificationBotRow{}).Where("id = ?", id).Update("payload", mustJSON(botPayload(b))).Error
	})
}

// RediscoverBotTenant clears an unlocked candidate (for example after a
// conflict) so the next trusted event is recorded afresh.
func (r *NotificationsRepo) RediscoverBotTenant(id string, expectedVersion int64) (domain.NotificationBot, error) {
	var b domain.NotificationBot
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var row notificationBotRow
		if err := r.locking(tx).Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		var err error
		if b, err = row.bot(); err != nil {
			return err
		}
		if row.ConfigVersion != expectedVersion {
			return ErrBotConfigChanged
		}
		if b.TenantState == domain.BotTenantLocked || b.ChannelKind() == domain.BotChannelWeCom {
			return ErrBotTenantCandidate
		}
		b.TenantCandidate, b.TenantState = nil, domain.BotTenantAwaiting
		b.ConfigVersion = row.ConfigVersion + 1
		return tx.Model(&notificationBotRow{}).Where("id = ?", id).Updates(map[string]any{"payload": mustJSON(botPayload(b)), "config_version": b.ConfigVersion}).Error
	})
	return b, err
}

// botPayload is the JSON payload of b without read-only derived fields.
func botPayload(b domain.NotificationBot) domain.NotificationBot {
	b.CredentialFormat = ""
	return b
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
		b.CredentialFormat = ""
		return tx.Model(&row).Update("payload", mustJSON(b)).Error
	})
}

// DeleteNotificationBot removes the configuration and its credential, and
// stops its work in the lock order configuration -> connection ->
// interactions/deliveries: unsent work is cancelled; anything in flight
// becomes reply_unknown / send_unknown for manual review.
func (r *NotificationsRepo) DeleteNotificationBot(id string) error {
	return retryBotDeadlock(func() error { return r.deleteNotificationBot(id) })
}

func (r *NotificationsRepo) deleteNotificationBot(id string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row notificationBotRow
		if err := r.locking(tx).Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		b, err := row.bot()
		if err != nil {
			return err
		}
		if err := r.interactions.stopBotInteractions(tx, id, b.LeaseKey(), true); err != nil {
			return err
		}
		if err := r.stopNotificationDeliveries(tx, id, true); err != nil {
			return err
		}
		if err := r.users.revokeBotConfigIdentities(tx, b); err != nil {
			return err
		}
		if err := tx.Where("id = ?", id).Delete(&notificationBotRow{}).Error; err != nil {
			return err
		}
		return r.users.deleteBotCredential(tx, id)
	})
}

// stopNotificationDeliveries: in-flight sends become send_unknown; on delete
// every other unsent delivery is cancelled. Disable leaves pending work for
// the worker, which cancels it when it sees the disabled Bot.
func (r *NotificationsRepo) stopNotificationDeliveries(tx *gorm.DB, botID string, deleted bool) error {
	reason := "机器人在发送过程中被停用，送达结果待核对"
	if deleted {
		reason = "机器人在发送过程中被删除，送达结果待核对"
	}
	if err := tx.Model(&domain.NotificationDelivery{}).Where("bot_id = ? AND status = ?", botID, "sending").Updates(map[string]any{"status": "send_unknown", "last_error": reason, "lease_token": "", "lease_until": nil}).Error; err != nil {
		return err
	}
	if !deleted {
		return nil
	}
	return tx.Model(&domain.NotificationDelivery{}).Where("bot_id = ? AND status IN ?", botID, []string{"pending", "failed", "awaiting_authorization", "target_unavailable"}).Updates(map[string]any{"status": "cancelled", "last_error": "机器人已删除", "lease_token": "", "lease_until": nil}).Error
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
	result := r.DB.Model(&domain.NotificationDelivery{}).Where("id = ? AND project_id = ? AND (content_source IS NULL OR content_source NOT IN ('invalid', 'purged')) AND status IN ?", id, projectID, []string{"failed", "awaiting_authorization", "target_unavailable", "send_unknown"}).Updates(map[string]any{"status": "pending", "attempts": 0, "next_attempt_at": time.Now().UTC(), "last_error": "", "send_key": "", "delivery_generation": gorm.Expr("delivery_generation + 1"), "send_started_at_us": 0})
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
		for _, binding := range runNotificationBindings(run, node, runTrigger) {
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
	return r.enqueueOriginNotification(tx, run, event, seq, nodeTrigger, runTrigger)
}

// Templates of the messages sent back to the chat a run was started from.
var originTemplates = map[string]string{
	"waiting":       "### 需要人工处理\n需求：{{issue.title}}\nSOP：{{workflow}}\n节点：{{node}}\n查看详情：{{url}}",
	"question":      "### 节点需要补充信息\n需求：{{issue.title}}\nSOP：{{workflow}}\n节点：{{node}}\n查看详情：{{url}}",
	"failed":        "### 节点失败\n需求：{{issue.title}}\nSOP：{{workflow}}\n节点：{{node}}\n原因：{{error}}\n查看详情：{{url}}",
	"run.succeeded": "### SOP 已完成\n需求：{{issue.title}}\nSOP：{{workflow}}\n查看详情：{{url}}",
	"run.failed":    "### SOP 失败\n需求：{{issue.title}}\nSOP：{{workflow}}\n原因：{{error}}\n查看详情：{{url}}",
}

// enqueueOriginNotification sends the key events of a run started from a
// chat (Feishu, DingTalk, WeCom) back to that conversation (group or private),
// whatever the SOP's notification bindings are: a waiting or failed node
// (with an action card when the Bot receives messages) and the end of the
// run. A run failure already explained by a node failure is not repeated.
// When the SOP itself notifies the same person through the same Bot the
// delivery key is shared, so only one message goes out.
func (r *NotificationsRepo) enqueueOriginNotification(tx *gorm.DB, run domain.Run, event domain.RunEvent, seq int64, nodeTrigger, runTrigger string) error {
	kind := ""
	switch {
	case event.NodeID != "" && (nodeTrigger == "waiting" || nodeTrigger == "failed"):
		kind = nodeTrigger
	case runTrigger == "succeeded":
		kind = "run.succeeded"
	case runTrigger == "failed" && !runHasNodeFailureEvent(run.Events):
		kind = "run.failed"
	default:
		return nil
	}
	origin, ok := botRunOrigin(tx, run.ID)
	if !ok {
		origin, ok = botStartOrigin(tx, run)
	}
	if !ok {
		return nil
	}
	var row notificationBotRow
	if err := tx.Where("id = ?", origin.ConfigID).Limit(1).Find(&row).Error; err != nil || row.ID == "" {
		return err
	}
	bot, err := row.bot()
	if err != nil {
		return err
	}
	projectID := notificationRunProject(run)
	if !bot.Enabled || bot.ProjectID != projectID {
		return nil
	}
	target, targetType, targetName := origin.SenderID, "single", "发起人（单聊）"
	if bot.ChannelKind() == domain.BotChannelWeCom {
		// WeCom conversations are addressed by their chat ID (the
		// userid of a private chat) over the live connection.
		target = origin.ChatID
	}
	if origin.ChatType == "group" {
		target, targetType, targetName = origin.ChatID, "group", "发起的群聊"
	}
	if target == "" {
		return nil
	}
	node := domain.WorkflowNode{Name: "全部节点"}
	for _, n := range run.WorkflowSnapshot.Nodes {
		if n.ID == event.NodeID {
			node = n
		}
	}
	trigger := kind
	if kind == "run.succeeded" || kind == "run.failed" {
		trigger = runTrigger
	}
	values := map[string]string{"project": r.notificationProjectName(tx, projectID), "workflow": run.WorkflowSnapshot.Name, "node": node.Name, "input": run.Input, "status": notificationStatus(trigger, "节点"), "issue.title": "未关联需求", "error": event.Message}
	if run.ExecutionContext.IssueID != "" {
		if issue, ok := r.issues.issueInTx(tx, run.ExecutionContext.IssueID); ok && issue.ProjectID == projectID {
			values["issue.title"], values["issue.id"] = issue.Title, issue.ID
		}
	}
	item := domain.NotificationDelivery{ProjectID: projectID, BotID: bot.ID, RunID: run.ID, EventType: trigger, SourceType: "run", SourceID: run.ID, SourceName: run.WorkflowSnapshot.Name, Origin: domain.NotificationOriginChat, TargetID: target, TargetType: targetType, TargetName: targetName, WaitingPolicy: "actionable"}
	if targetType == "single" {
		item.OriginUserID = origin.UserID
	}
	template, question := originTemplates[kind], ""
	if event.NodeID != "" {
		item.NodeID, item.SourceName = event.NodeID, node.Name
		if nr := run.Nodes[event.NodeID]; nr != nil {
			item.Attempt, item.ReworkCount = nr.Attempt, nr.ReworkCount
			if nr.Error != "" {
				values["error"] = nr.Error
			}
			if kind == "waiting" && event.Type == "gate.waiting" {
				question = nr.Question()
			}
		}
	}
	if question != "" {
		template = originTemplates["question"]
	}
	if strings.TrimSpace(values["error"]) == "" {
		values["error"] = "请在平台查看运行详情"
	}
	key := fmt.Sprintf("origin:%s:%d:%s", run.ID, seq, bot.ID)
	if bot.Target != nil && bot.Target.ID == target && kind != "run.succeeded" && kind != "run.failed" {
		// Same Bot and receiver as the SOP's node reminders: share the key.
		key = fmt.Sprintf("run:%s:%d:%s", run.ID, seq, bot.ID)
	}
	content, source, renderErr := domain.RenderNotification(domain.NotificationBinding{BotID: bot.ID, Events: []string{trigger}, Template: template}, domain.NotificationContext{Values: values, SourceType: "run"})
	if renderErr != nil {
		return nil
	}
	if question != "" {
		// The question is free text: it is added after rendering, outside
		// the template variables.
		content = strings.Replace(content, "\n查看详情：", "\n问题："+domain.NotificationText(question, 1200)+"\n"+domain.NodeAnswerHint(bot.ChannelKind(), origin.ChatType)+"\n查看详情：", 1)
	}
	item.ID = hexSHA256(key)
	item.Content, item.ContentSource, item.Status = content, source, "pending"
	item.CreatedAt = time.Now().UTC()
	item.NextAttemptAt = item.CreatedAt
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error
}

// runNotificationBindings selects the bindings evaluated for one node of a run.
// Workflow nodes use the SOP reminder bindings; the synthetic SOP-level node
// (empty ID) uses its own bindings, falling back to run-scoped "failed"
// reminders when the run failed without any node failure event.
func runNotificationBindings(run domain.Run, node domain.WorkflowNode, runTrigger string) []domain.NotificationBinding {
	if node.ID != "" {
		return domain.NodeReminderBindings(domain.WorkflowNotifications(run.WorkflowSnapshot))
	}
	// Setup/runtime failures may end a SOP without a node failure event.
	if runTrigger != "failed" || runHasNodeFailureEvent(run.Events) {
		return node.Notifications
	}
	bindings := domain.NodeReminderBindings(node.Notifications)
	for i := range bindings {
		bindings[i].Scope = "run"
		bindings[i].Events = []string{"failed"}
	}
	return bindings
}

// runHasNodeFailureEvent reports whether any recorded event is a node-level failure.
func runHasNodeFailureEvent(events []domain.RunEvent) bool {
	for _, event := range events {
		if notificationTrigger(event.Type) == "failed" && event.NodeID != "" {
			return true
		}
	}
	return false
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
	return slices.Contains(binding.Events, event)
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
	item.ID = hexSHA256(key)
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

// InteractiveNotificationBots lists receiving configurations by the physical
// columns that lease fencing also reads. Rows not yet backfilled by
// migration 036 are skipped; see UnmigratedNotificationBots.
func (r *NotificationsRepo) InteractiveNotificationBots() ([]domain.NotificationBot, error) {
	var rows []notificationBotRow
	if err := r.DB.Where("enabled = ? AND interaction_enabled = ? AND config_version > 0", true, true).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	return r.decodeBots(rows)
}

// UnmigratedNotificationBots counts configurations whose physical columns
// were never backfilled (config_version = 0).
func (r *NotificationsRepo) UnmigratedNotificationBots() (int64, error) {
	var n int64
	err := r.DB.Model(&notificationBotRow{}).Where("config_version = 0").Count(&n).Error
	return n, err
}

func (r *NotificationsRepo) NotificationDelivery(id string) (domain.NotificationDelivery, error) {
	var item domain.NotificationDelivery
	err := r.DB.First(&item, "id = ?", id).Error
	return item, err
}

// ChatWaitingDeliveries lists the waiting-node messages a Bot sent to one
// conversation since a time, oldest first.
func (r *NotificationsRepo) ChatWaitingDeliveries(botID, targetType, targetID string, since time.Time) ([]domain.NotificationDelivery, error) {
	var items []domain.NotificationDelivery
	err := r.DB.Where("bot_id = ? AND event_type = ? AND node_id <> '' AND target_type = ? AND target_id = ? AND created_at >= ? AND status IN ?", botID, "waiting", targetType, targetID, since, []string{"pending", "sending", "sent"}).
		Order("created_at, id").Limit(50).Find(&items).Error
	return items, err
}

// NotificationDeliveryByPlatformMessage finds the delivery a Bot sent as the
// given platform message (a quoted node question).
func (r *NotificationsRepo) NotificationDeliveryByPlatformMessage(botID, platformMessageID string) (domain.NotificationDelivery, bool) {
	var item domain.NotificationDelivery
	if platformMessageID == "" {
		return item, false
	}
	if err := r.DB.Where("bot_id = ? AND platform_message_id = ?", botID, platformMessageID).Limit(1).Find(&item).Error; err != nil || item.ID == "" {
		return item, false
	}
	return item, true
}

var ErrInteractiveBotAlreadyExists = fmt.Errorf("同一企业微信 Bot 只能有一个启用的对话配置")

var notificationTerminal = []string{"sent", "failed", "cancelled", "expired", "target_unavailable"}

// PurgeBotNotifications applies the retention policy once to finished deliveries.
func (r *NotificationsRepo) PurgeBotNotifications(ctx context.Context, now time.Time, policy BotRetentionPolicy) (BotRetentionResult, error) {
	run := newRetentionRun(ctx, policy)
	p, db := run.p, r.DB.WithContext(ctx)
	var out BotRetentionResult
	if p.Body > 0 {
		cut := now.Add(-p.Body)
		run.step(&out.DeliveryBodies, func(limit int) ([]string, error) {
			var ids []string
			return ids, notificationsFinishedBefore(db, cut).Where("(content_source IS NULL OR content_source <> 'purged')").Order("created_at").Limit(limit).Pluck("id", &ids).Error
		}, func(ids []string) (int64, error) {
			res := notificationsFinishedBefore(db, cut).Where("id IN ? AND (content_source IS NULL OR content_source <> 'purged')", ids).UpdateColumns(map[string]any{"content": "", "report_request": "", "content_source": "purged"})
			return res.RowsAffected, res.Error
		})
	}
	if p.Record > 0 {
		cut := now.Add(-p.Record)
		run.step(&out.Deliveries, func(limit int) ([]string, error) {
			var ids []string
			return ids, notificationsFinishedBefore(db, cut).Order("created_at").Limit(limit).Pluck("id", &ids).Error
		}, func(ids []string) (int64, error) {
			res := notificationsFinishedBefore(db, cut).Where("id IN ?", ids).Delete(&domain.NotificationDelivery{})
			return res.RowsAffected, res.Error
		})
	}
	return out, run.err
}

// notificationsFinishedBefore selects terminal deliveries whose creation,
// last attempt and send time are all before cut. Deliveries have no
// completion column; the last attempt time is written when they finish.
func notificationsFinishedBefore(db *gorm.DB, cut time.Time) *gorm.DB {
	return db.Model(&domain.NotificationDelivery{}).Where("status IN ? AND created_at < ? AND next_attempt_at < ? AND (sent_at IS NULL OR sent_at < ?)", notificationTerminal, cut, cut, cut)
}
