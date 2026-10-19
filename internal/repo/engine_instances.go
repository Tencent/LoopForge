package repo

import (
	"strings"
	"time"

	"gorm.io/gorm/clause"
)

// EngineInstance is a live api/worker process that can receive run-control
// interrupts. The row expires if heartbeats stop, same idea as a run lease.
type EngineInstance struct {
	InstanceID    string
	Role          string
	ControlURL    string
	WorkspaceHost string
}

// EngineInstancesRepo stores advertised worker interrupt URLs.
type EngineInstancesRepo struct {
	*deps
}

func (r *EngineInstancesRepo) HeartbeatEngineInstance(inst EngineInstance, ttl time.Duration) error {
	id := strings.TrimSpace(inst.InstanceID)
	if id == "" || ttl <= 0 {
		return ErrRunLeaseLost
	}
	now := time.Now().UTC()
	row := engineInstanceRow{
		InstanceID:    id,
		Role:          strings.TrimSpace(inst.Role),
		ControlURL:    strings.TrimRight(strings.TrimSpace(inst.ControlURL), "/"),
		WorkspaceHost: strings.TrimSpace(inst.WorkspaceHost),
		HeartbeatAt:   now,
		LeaseUntil:    now.Add(ttl),
	}
	return r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "instance_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"role", "control_url", "workspace_host", "heartbeat_at", "lease_until"}),
	}).Create(&row).Error
}

func (r *EngineInstancesRepo) DeleteEngineInstance(instanceID string) error {
	id := strings.TrimSpace(instanceID)
	if id == "" {
		return nil
	}
	return r.DB.Where("instance_id = ?", id).Delete(&engineInstanceRow{}).Error
}

// WorkspaceHostAlive reports whether a live worker still advertises host, i.e.
// whether a workspace bound there may still be served by its owner.
func (r *EngineInstancesRepo) WorkspaceHostAlive(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	var count int64
	err := r.DB.Model(&engineInstanceRow{}).
		Where("workspace_host = ? AND lease_until > ? AND role IN ?", host, time.Now().UTC(), []string{"worker", "all"}).
		Count(&count).Error
	if err != nil {
		// Unknown liveness keeps the bind: rebuilding a workspace that is
		// still alive elsewhere would drop its uncommitted work.
		return true
	}
	return count > 0
}

func (r *EngineInstancesRepo) EngineInstanceControlURL(instanceID string) string {
	id := strings.TrimSpace(instanceID)
	if id == "" {
		return ""
	}
	var row engineInstanceRow
	err := r.DB.Where("instance_id = ? AND control_url <> '' AND lease_until > ?", id, time.Now().UTC()).Take(&row).Error
	if err != nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(row.ControlURL), "/")
}

func (r *EngineInstancesRepo) LiveWorkerControlURLs() []string {
	var rows []engineInstanceRow
	if err := r.DB.Where("control_url <> '' AND lease_until > ? AND role IN ?", time.Now().UTC(), []string{"worker", "all"}).Find(&rows).Error; err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		u := strings.TrimRight(strings.TrimSpace(row.ControlURL), "/")
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func (r *EngineInstancesRepo) WorkspaceHostControlURL(host string) string {
	var row engineInstanceRow
	if r.DB.Where("workspace_host = ? AND control_url <> '' AND lease_until > ? AND role IN ?", host, time.Now().UTC(), []string{"worker", "all"}).Order("heartbeat_at DESC").Take(&row).Error != nil {
		return ""
	}
	return strings.TrimRight(row.ControlURL, "/")
}
