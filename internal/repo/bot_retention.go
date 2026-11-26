package repo

import (
	"agentflow/internal/domain"
	"context"
	"strings"
	"time"
)

// Retention of chat Bot data (stage H). Two horizons apply to records in a
// terminal state, measured from the time they actually finished:
//
//   - Body: message and reply text, rendered notification content, frozen
//     write drafts and the native CLI logs of Bot turns are removed. The row
//     stays for audit and can no longer be sent or executed again.
//   - Record: the row itself is deleted.
//
// Only terminal states are touched. Interactions in queued, running, ready,
// sending or reply_unknown, notifications that are pending, sending,
// awaiting authorization or send_unknown, and drafts that are executing or
// need review stay until someone handles them. Every statement re-checks
// its conditions on the selected IDs, so concurrent cleanups on several
// replicas are harmless.

// BotRetentionPolicy: a zero duration disables that horizon. Batch bounds
// one statement; Batches bounds the statements per category and run.
type BotRetentionPolicy struct {
	Body, Record time.Duration
	Batch        int
	Batches      int
}

// BotRetentionResult counts rows changed by one run.
type BotRetentionResult struct {
	InteractionBodies, Interactions int64
	DeliveryBodies, Deliveries      int64
	DraftsExpired                   int64
	DraftBodies, Drafts             int64
	NativeMappings                  int64
	// Chat clarifications (answers) and chat origins of runs.
	ClarifyBodies, ClarifySessions int64
	ChatOrigins                    int64
}

func (r BotRetentionResult) Total() int64 {
	return r.InteractionBodies + r.Interactions + r.DeliveryBodies + r.Deliveries + r.DraftsExpired + r.DraftBodies + r.Drafts + r.NativeMappings + r.ClarifyBodies + r.ClarifySessions + r.ChatOrigins
}

var (
	botDraftTerminal        = []string{BotDraftExecuted, BotDraftFailed, BotDraftExpired, BotDraftCancelled}
	botNativeFinished       = []string{"completed", "failed", "interrupted"}
	botClarifyTerminal      = []string{domain.BotClarifySaved, domain.BotClarifyEnded}
	botNativeCleanupSettled = []string{BotNativeCleanupRemoved, BotNativeCleanupMissing}
)

// Native log cleanup states of bot_native_sessions.cleanup_status.
const (
	BotNativeCleanupRemoved     = "removed"
	BotNativeCleanupMissing     = "missing"
	BotNativeCleanupError       = "error"
	BotNativeCleanupUnsupported = "unsupported"
)

func (p BotRetentionPolicy) normalized() BotRetentionPolicy {
	if p.Batch <= 0 {
		p.Batch = 1000
	}
	if p.Batches <= 0 {
		p.Batches = 10
	}
	return p
}

// batched runs one category: select a batch of IDs, apply the change with
// the same conditions, repeat while full batches come back.
func batched(ctx context.Context, p BotRetentionPolicy, pick func(limit int) ([]string, error), apply func(ids []string) (int64, error)) (int64, error) {
	var total int64
	for i := 0; i < p.Batches; i++ {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		ids, err := pick(p.Batch)
		if err != nil || len(ids) == 0 {
			return total, err
		}
		n, err := apply(ids)
		total += n
		if err != nil || len(ids) < p.Batch {
			return total, err
		}
	}
	return total, nil
}

// Add sums the counts of another domain's purge.
func (r *BotRetentionResult) Add(o BotRetentionResult) {
	r.InteractionBodies += o.InteractionBodies
	r.Interactions += o.Interactions
	r.DeliveryBodies += o.DeliveryBodies
	r.Deliveries += o.Deliveries
	r.DraftsExpired += o.DraftsExpired
	r.DraftBodies += o.DraftBodies
	r.Drafts += o.Drafts
	r.NativeMappings += o.NativeMappings
	r.ClarifyBodies += o.ClarifyBodies
	r.ClarifySessions += o.ClarifySessions
	r.ChatOrigins += o.ChatOrigins
}

// retentionRun applies the categories of one domain Repo in order and stops
// at the first error.
type retentionRun struct {
	ctx context.Context
	p   BotRetentionPolicy
	err error
}

func newRetentionRun(ctx context.Context, policy BotRetentionPolicy) *retentionRun {
	return &retentionRun{ctx: ctx, p: policy.normalized()}
}

func (r *retentionRun) step(n *int64, pick func(int) ([]string, error), apply func([]string) (int64, error)) {
	if r.err != nil {
		return
	}
	*n, r.err = batched(r.ctx, r.p, pick, apply)
}

// PurgeBotChatRecords applies the retention policy once to write drafts, chat
// clarifications, chat origins of runs and native session mappings.
func (r *AssistantRepo) PurgeBotChatRecords(ctx context.Context, now time.Time, policy BotRetentionPolicy) (BotRetentionResult, error) {
	run := newRetentionRun(ctx, policy)
	p, db := run.p, r.DB.WithContext(ctx)
	var out BotRetentionResult

	// Drafts that expired without anyone opening them.
	run.step(&out.DraftsExpired, func(limit int) ([]string, error) {
		var ids []string
		return ids, db.Model(&botPendingDraftRow{}).Where("status = ? AND expires_at <= ?", BotDraftPending, now).Order("expires_at").Limit(limit).Pluck("id", &ids).Error
	}, func(ids []string) (int64, error) {
		res := db.Model(&botPendingDraftRow{}).Where("id IN ? AND status = ? AND expires_at <= ?", ids, BotDraftPending, now).UpdateColumns(map[string]any{"status": BotDraftExpired, "reason": "草稿已过期", "updated_at": now})
		return res.RowsAffected, res.Error
	})
	if p.Body > 0 {
		cut := now.Add(-p.Body)
		run.step(&out.DraftBodies, func(limit int) ([]string, error) {
			var ids []string
			return ids, db.Model(&botPendingDraftRow{}).Where("status IN ? AND updated_at < ? AND body_purged_at IS NULL", botDraftTerminal, cut).Order("updated_at").Limit(limit).Pluck("id", &ids).Error
		}, func(ids []string) (int64, error) {
			res := db.Model(&botPendingDraftRow{}).Where("id IN ? AND status IN ? AND updated_at < ? AND body_purged_at IS NULL", ids, botDraftTerminal, cut).UpdateColumns(map[string]any{"drafts": "", "summary": "", "result": "", "sop_options": "", "body_purged_at": now})
			return res.RowsAffected, res.Error
		})
		// Finished chat clarifications keep their state, not the answers.
		run.step(&out.ClarifyBodies, func(limit int) ([]string, error) {
			var ids []string
			return ids, db.Model(&botClarifySessionRow{}).Where("status IN ? AND updated_at < ? AND payload <> ''", botClarifyTerminal, cut).Order("updated_at").Limit(limit).Pluck("id", &ids).Error
		}, func(ids []string) (int64, error) {
			res := db.Model(&botClarifySessionRow{}).Where("id IN ? AND status IN ? AND updated_at < ? AND payload <> ''", ids, botClarifyTerminal, cut).UpdateColumns(map[string]any{"payload": ""})
			return res.RowsAffected, res.Error
		})
	}
	if p.Record > 0 {
		cut := now.Add(-p.Record)
		run.step(&out.Drafts, func(limit int) ([]string, error) {
			var ids []string
			return ids, db.Model(&botPendingDraftRow{}).Where("status IN ? AND updated_at < ?", botDraftTerminal, cut).Order("updated_at").Limit(limit).Pluck("id", &ids).Error
		}, func(ids []string) (int64, error) {
			res := db.Where("id IN ? AND status IN ? AND updated_at < ?", ids, botDraftTerminal, cut).Delete(&botPendingDraftRow{})
			return res.RowsAffected, res.Error
		})
		run.step(&out.ClarifySessions, func(limit int) ([]string, error) {
			var ids []string
			return ids, db.Model(&botClarifySessionRow{}).Where("status IN ? AND updated_at < ?", botClarifyTerminal, cut).Order("updated_at").Limit(limit).Pluck("id", &ids).Error
		}, func(ids []string) (int64, error) {
			res := db.Where("id IN ? AND status IN ? AND updated_at < ?", ids, botClarifyTerminal, cut).Delete(&botClarifySessionRow{})
			return res.RowsAffected, res.Error
		})
		run.step(&out.ChatOrigins, func(limit int) ([]string, error) {
			var ids []string
			return ids, db.Model(&botChatOriginRow{}).Where("created_at < ?", cut).Order("created_at").Limit(limit).Pluck("id", &ids).Error
		}, func(ids []string) (int64, error) {
			res := db.Where("id IN ? AND created_at < ?", ids, cut).Delete(&botChatOriginRow{})
			return res.RowsAffected, res.Error
		})
		// A mapping goes only after its native log is settled (removed or
		// confirmed missing); errors and unsupported engines stay listed.
		run.step(&out.NativeMappings, func(limit int) ([]string, error) {
			var ids []string
			return ids, db.Model(&botNativeSessionRow{}).Where("status IN ? AND cleanup_status IN ? AND finished_at < ?", botNativeFinished, botNativeCleanupSettled, cut).Order("finished_at").Limit(limit).Pluck("id", &ids).Error
		}, func(ids []string) (int64, error) {
			res := db.Where("id IN ? AND status IN ? AND cleanup_status IN ? AND finished_at < ?", ids, botNativeFinished, botNativeCleanupSettled, cut).Delete(&botNativeSessionRow{})
			return res.RowsAffected, res.Error
		})
	}
	return out, run.err
}

// BotNativeCleanup is one finished Bot turn whose native CLI log is due.
type BotNativeCleanup struct {
	ID, Engine, StorageScope     string
	UserID                       int64
	RequestedSessionID, NativeID string
	FinishedAt                   time.Time
}

// BotNativeCleanupResult is the outcome for one mapping. Paths are relative
// to the CLI home the log was resolved in.
type BotNativeCleanupResult struct {
	ID     string
	Status string
	Paths  []string
	Detail string
}

// DueBotNativeCleanups lists finished mappings older than cut whose log was
// not cleaned yet (or failed last time). A mapping is skipped while another
// mapping of the same native session is still running or not yet due, so a
// shared native log is only removed when every turn using it has expired.
func (r *AssistantRepo) DueBotNativeCleanups(cut time.Time, limit int) ([]BotNativeCleanup, error) {
	var rows []botNativeSessionRow
	if err := r.DB.Where("status IN ? AND finished_at < ? AND (cleanup_status = '' OR cleanup_status IS NULL OR cleanup_status = ?)", botNativeFinished, cut, BotNativeCleanupError).Order("finished_at").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]BotNativeCleanup, 0, len(rows))
	for _, row := range rows {
		if shared := sharedNativeIDs(row); len(shared) > 0 {
			var busy int64
			if err := r.DB.Model(&botNativeSessionRow{}).Where("id <> ? AND user_id = ? AND (native_session_id IN ? OR requested_session_id IN ?) AND (status NOT IN ? OR finished_at IS NULL OR finished_at >= ?)", row.ID, row.UserID, shared, shared, botNativeFinished, cut).Count(&busy).Error; err != nil {
				return nil, err
			}
			if busy > 0 {
				continue
			}
		}
		item := BotNativeCleanup{ID: row.ID, Engine: row.Engine, StorageScope: row.StorageScope, UserID: row.UserID, RequestedSessionID: row.RequestedSessionID, NativeID: row.NativeSessionID}
		if row.FinishedAt != nil {
			item.FinishedAt = *row.FinishedAt
		}
		out = append(out, item)
	}
	return out, nil
}

func sharedNativeIDs(row botNativeSessionRow) []string {
	out := []string{}
	for _, id := range []string{row.NativeSessionID, row.RequestedSessionID} {
		if strings.TrimSpace(id) != "" {
			out = append(out, id)
		}
	}
	return out
}

// RecordBotNativeCleanup stores the outcome of one mapping.
func (r *AssistantRepo) RecordBotNativeCleanup(res BotNativeCleanupResult, now time.Time) error {
	paths := strings.Join(res.Paths, "\n")
	if len(paths) > 512 {
		paths = paths[:509] + "..."
	}
	return r.DB.Model(&botNativeSessionRow{}).Where("id = ? AND (cleanup_status = '' OR cleanup_status IS NULL OR cleanup_status = ?)", res.ID, BotNativeCleanupError).UpdateColumns(map[string]any{"cleanup_status": res.Status, "cleanup_detail": truncateBotDetail(res.Detail), "log_path": paths, "cleaned_at": now}).Error
}

// BotNativeCleanupState returns the stored cleanup outcome of a mapping
// (tests, diagnostics).
func (r *AssistantRepo) BotNativeCleanupState(id string) (status, paths, detail string, err error) {
	var row botNativeSessionRow
	if err = r.DB.First(&row, "id = ?", id).Error; err != nil {
		return
	}
	return row.CleanupStatus, row.LogPath, row.CleanupDetail, nil
}
