package botinteraction

import (
	"agentflow/internal/repo"
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Data retention of chat Bots (stage H). Defaults: message bodies, rendered
// notification content, write drafts and native CLI logs of Bot turns are
// removed 90 days after the record finished; the records themselves 180
// days after. The cleanup runs hourly with the binding-request cleanup, in
// batches of 1000.
const (
	defaultBodyRetentionDays   = 90
	defaultRecordRetentionDays = 180
	retentionBatch             = 1000
	retentionBatches           = 10
)

// NativeLogCleaner removes the native CLI logs of finished Bot turns. It is
// provided by the HTTP layer, which knows where each engine keeps its logs.
type NativeLogCleaner func(ctx context.Context, due []repo.BotNativeCleanup) []repo.BotNativeCleanupResult

// RetentionPolicyFromEnv reads AGENTFLOW_BOT_BODY_RETENTION_DAYS and
// AGENTFLOW_BOT_RECORD_RETENTION_DAYS. 0 disables that horizon; invalid
// values fall back to the default. A record horizon shorter than the body
// horizon is raised to it.
func RetentionPolicyFromEnv() repo.BotRetentionPolicy {
	days := func(name string, def int) int {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return def
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 36500 {
			log.Printf("bot retention: %s=%q is not a number of days, using %d", name, raw, def)
			return def
		}
		return n
	}
	body := days("AGENTFLOW_BOT_BODY_RETENTION_DAYS", defaultBodyRetentionDays)
	record := days("AGENTFLOW_BOT_RECORD_RETENTION_DAYS", defaultRecordRetentionDays)
	if body > 0 && record > 0 && record < body {
		log.Printf("bot retention: record retention %d days is shorter than body retention %d days, using %d", record, body, body)
		record = body
	}
	return repo.BotRetentionPolicy{Body: time.Duration(body) * 24 * time.Hour, Record: time.Duration(record) * 24 * time.Hour, Batch: retentionBatch, Batches: retentionBatches}
}

// RunRetention applies the policy once: database rows first, then the native
// logs whose body horizon passed. It returns the database counts and the
// number of mappings whose logs were handled.
func (s *Service) RunRetention(ctx context.Context, now time.Time, policy repo.BotRetentionPolicy) (repo.BotRetentionResult, int, error) {
	var res repo.BotRetentionResult
	for _, purge := range []func(context.Context, time.Time, repo.BotRetentionPolicy) (repo.BotRetentionResult, error){s.Store.PurgeBotInteractions, s.Store.PurgeBotNotifications, s.Store.PurgeBotChatRecords} {
		part, err := purge(ctx, now, policy)
		res.Add(part)
		if err != nil {
			return res, 0, err
		}
	}
	if policy.Body <= 0 || s.NativeLogs == nil {
		return res, 0, nil
	}
	batch := policy.Batch
	if batch <= 0 {
		batch = retentionBatch
	}
	due, err := s.Store.DueBotNativeCleanups(now.Add(-policy.Body), batch)
	if err != nil || len(due) == 0 {
		return res, 0, err
	}
	handled := 0
	for _, out := range s.NativeLogs(ctx, due) {
		if err := s.Store.RecordBotNativeCleanup(out, now); err != nil {
			return res, handled, err
		}
		handled++
	}
	return res, handled, nil
}

func (s *Service) retentionTick(ctx context.Context, now time.Time) {
	if s.Retention == nil {
		return
	}
	res, logs, err := s.RunRetention(ctx, now, *s.Retention)
	if err != nil {
		log.Printf("bot retention: cleanup failed: %v", err)
	}
	if res.Total() > 0 || logs > 0 {
		log.Printf("bot retention: interaction bodies=%d interactions=%d notification bodies=%d notifications=%d drafts expired=%d draft bodies=%d drafts=%d native mappings=%d native logs handled=%d", res.InteractionBodies, res.Interactions, res.DeliveryBodies, res.Deliveries, res.DraftsExpired, res.DraftBodies, res.Drafts, res.NativeMappings, logs)
	}
}
