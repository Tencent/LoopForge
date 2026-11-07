package repo

import "agentflow/internal/domain"

// FreezeNotificationReport fences stale workers and persists content before any send.
func (r *NotificationsRepo) FreezeNotificationReport(item domain.NotificationDelivery) (bool, error) {
	result := r.DB.Model(&domain.NotificationDelivery{}).Where("id = ? AND status = ? AND lease_token = ? AND report_state = ?", item.ID, "sending", item.LeaseToken, "pending").Updates(map[string]any{
		"content": item.Content, "content_source": item.ContentSource, "report_state": item.ReportState, "report_error": item.ReportError, "report_request": "",
	})
	return result.RowsAffected == 1, result.Error
}
