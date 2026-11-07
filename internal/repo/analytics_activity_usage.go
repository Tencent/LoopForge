package repo

import (
	"encoding/json"
	"sync/atomic"
	"time"

	"agentflow/internal/domain"
)

type analyticsUsageMessage struct {
	Index     int                `json:"index"`
	CreatedAt time.Time          `json:"createdAt"`
	StartedAt *time.Time         `json:"startedAt"`
	Usage     *domain.TokenUsage `json:"usage"`
}

type analyticsUsageRow struct {
	ID       string
	Messages []byte
}

var noAnalyticsUsageProjection atomic.Bool

// Project only reply timestamps and usage, never content or tool output.
// MySQL 5.7 lacks JSON_TABLE; its read-only fallback reads bounded batches.
func (r *AnalyticsRepo) analyticsSessionUsage(ids []string, visit func(string, []analyticsUsageMessage)) error {
	for start := 0; start < len(ids); start += 100 {
		batch := ids[start:min(start+100, len(ids))]
		var rows []analyticsUsageRow
		projected := !noAnalyticsUsageProjection.Load()
		if projected {
			expr := `(SELECT json_group_array(json_object('index',CAST(m.key AS INTEGER),'createdAt',json_extract(m.value,'$.createdAt'),'startedAt',json_extract(m.value,'$.startedAt'),'usage',json_extract(m.value,'$.usage'))) FROM json_each(s.payload,'$.messages') m WHERE json_extract(m.value,'$.role')='assistant')`
			if r.isMySQL() {
				expr = `(SELECT COALESCE(JSON_ARRAYAGG(JSON_OBJECT('index',m.pos-1,'createdAt',JSON_EXTRACT(s.payload,CONCAT('$.messages[',m.pos-1,'].createdAt')),'startedAt',JSON_EXTRACT(s.payload,CONCAT('$.messages[',m.pos-1,'].startedAt')),'usage',JSON_EXTRACT(s.payload,CONCAT('$.messages[',m.pos-1,'].usage')))),JSON_ARRAY()) FROM JSON_TABLE(s.payload,'$.messages[*]' COLUMNS(pos FOR ORDINALITY,role VARCHAR(32) PATH '$.role')) m WHERE m.role='assistant')`
			}
			err := r.DB.Table("assistant_sessions s").Select("s.id, "+expr+" AS messages").Where("s.id IN ?", batch).Scan(&rows).Error
			if err != nil {
				if !r.isMySQL() || !summaryProjectionUnsupported(err) {
					return err
				}
				noAnalyticsUsageProjection.Store(true)
				projected = false
			}
		}
		if !projected {
			rows = nil
			if err := r.DB.Table("assistant_sessions").Select("id, JSON_EXTRACT(payload,'$.messages') AS messages").Where("id IN ?", batch).Scan(&rows).Error; err != nil {
				return err
			}
		}
		for _, row := range rows {
			var messages []analyticsUsageMessage
			if len(row.Messages) > 0 {
				if projected {
					if err := json.Unmarshal(row.Messages, &messages); err != nil {
						return err
					}
				} else {
					var full []domain.AssistantMessage
					if err := json.Unmarshal(row.Messages, &full); err != nil {
						return err
					}
					for i, m := range full {
						if m.Role == "assistant" {
							messages = append(messages, analyticsUsageMessage{Index: i, CreatedAt: m.CreatedAt, StartedAt: m.StartedAt, Usage: m.Usage})
						}
					}
				}
			}
			visit(row.ID, messages)
		}
	}
	return nil
}

func accumulateSessionUsage(out *domain.AnalyticsSessionUsage, usage *domain.TokenUsage, undated bool) {
	if undated {
		out.UndatedReplies++
	}
	if usage == nil {
		out.MissingReplies++
		return
	}
	out.ReportedReplies++
	total := usage.TotalTokens
	if total == 0 {
		total = usage.InputTokens + usage.OutputTokens
	}
	out.Tokens += total
	out.InputTokens += usage.InputTokens
	out.OutputTokens += usage.OutputTokens
	out.CacheReadTokens += usage.CacheReadTokens
	out.CacheWriteTokens += usage.CacheWriteTokens
}
