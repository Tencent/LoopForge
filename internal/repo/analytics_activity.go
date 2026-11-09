package repo

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// analyticsActivity is read-only, including its legacy participant fallback.
// Do not use the history-list helpers here: they may backfill stored metadata.
func (r *AnalyticsRepo) analyticsActivity(filter domain.AnalyticsFilter, issues []domain.Issue, runs []domain.Run, out *domain.AnalyticsSnapshot) error {
	out.ActivityTrend = []domain.AnalyticsActivityPoint{}
	if filter.Origin == "external" {
		return nil
	}
	points := map[string]*domain.AnalyticsActivityPoint{}
	add := func(at time.Time, count func(*domain.AnalyticsActivity)) {
		if !analyticsActivityInWindow(at, filter) {
			return
		}
		count(&out.Activity)
		key := analyticsTrendBucket(at.UTC(), filter.Grain)
		if points[key] == nil {
			points[key] = &domain.AnalyticsActivityPoint{Date: key}
		}
		count(&points[key].AnalyticsActivity)
	}
	byID := map[string]domain.Issue{}
	matchesIssue := func(issue domain.Issue) bool {
		return filter.MatchesProject(issue.ProjectID) &&
			(filter.WorkflowID == "" || issue.WorkflowID == filter.WorkflowID) &&
			(filter.IssueSource == "" || analyticsIssueMatchesSource(issue, filter.IssueSource)) &&
			(!filter.RequirementsOnly || analyticsIsRequirementIssue(issue))
	}
	for _, issue := range issues {
		byID[issue.ID] = issue
		if !matchesIssue(issue) || !analyticsIsRequirementIssue(issue) {
			continue
		}
		if filter.UserID != "" && strconv.FormatInt(issue.AssigneeUserID, 10) != filter.UserID {
			continue
		}
		add(issue.CreatedAt, func(a *domain.AnalyticsActivity) { a.Requirements++ })
	}
	for _, run := range runs {
		issue := byID[strings.TrimSpace(run.ExecutionContext.IssueID)]
		projectID := strings.TrimSpace(run.ExecutionContext.ProjectID)
		if projectID == "" {
			projectID = issue.ProjectID
		}
		if run.IsWorkspace() || run.HistoricalGroup() || run.ExecutionContext.ParentRunID != "" ||
			!analyticsIsRequirementIssue(issue) || !filter.MatchesProject(projectID) ||
			(filter.WorkflowID != "" && run.WorkflowID != filter.WorkflowID) ||
			(filter.IssueSource != "" && !analyticsIssueMatchesSource(issue, filter.IssueSource)) ||
			!analyticsRunMatchesUser(run, filter.UserID, issue) {
			continue
		}
		add(run.CreatedAt, func(a *domain.AnalyticsActivity) { a.SOPRuns++ })
	}
	// SQL removes transcripts for modern rows. Legacy rows are read in bounded
	// batches below so older conversations are counted without writing backfills.
	q := r.DB.Table("assistant_sessions").Select(assistantSessionMetadataColumns)
	if filter.ProjectID != "" {
		q = q.Where("project_id = ?", filter.ProjectID)
	}
	if filter.ProjectIDs != nil {
		if len(filter.ProjectIDs) == 0 {
			q = q.Where("1 = 0")
		} else {
			q = q.Where("project_id IN ?", filter.ProjectIDs)
		}
	}
	var rows []assistantSessionRow
	if err := q.Find(&rows).Error; err != nil {
		return err
	}
	sessions := make([]domain.AssistantSession, 0, len(rows))
	legacy := []string{}
	for _, row := range rows {
		var session domain.AssistantSession
		if err := json.Unmarshal(row.Payload, &session); err != nil {
			return err
		}
		session.ID, session.UserID = row.ID, row.UserID
		if session.ProjectID == "" {
			session.ProjectID = row.ProjectID
		}
		if row.Created != nil {
			session.CreatedAt = row.Created.UTC()
		}
		if session.Shared || session.RunID != "" || strings.HasPrefix(session.ID, "sop-") ||
			!filter.MatchesProject(session.ProjectID) {
			continue
		}
		if filter.WorkflowID != "" || filter.IssueSource != "" || filter.RequirementsOnly {
			issue, exists := byID[session.IssueID]
			if !exists || issue.ProjectID != session.ProjectID || !matchesIssue(issue) {
				continue
			}
		}
		sessions = append(sessions, session)
		if session.ParticipantsVersion != 1 {
			legacy = append(legacy, session.ID)
		}
	}
	legacySessions := map[string]domain.AssistantSession{}
	for start := 0; start < len(legacy); start += 100 {
		end := start + 100
		if end > len(legacy) {
			end = len(legacy)
		}
		var full []assistantSessionRow
		if err := r.DB.Where("id IN ?", legacy[start:end]).Find(&full).Error; err != nil {
			return err
		}
		for _, row := range full {
			session := decodeAssistantSession(row)
			session.Messages = nil
			legacySessions[row.ID] = session
		}
	}
	usageSessions := map[string]domain.AssistantSession{}
	usageIDs := []string{}
	collaborative := map[string]bool{}
	for _, session := range sessions {
		if old, ok := legacySessions[session.ID]; ok {
			session.Participants = old.Participants
		}
		participants := map[int64]bool{}
		for _, p := range session.Participants {
			if p.ID > 0 {
				participants[p.ID] = true
			}
		}
		if filter.UserID != "" {
			id, err := strconv.ParseInt(filter.UserID, 10, 64)
			if err != nil || (session.UserID != id && !participants[id]) {
				continue
			}
		}
		add(session.CreatedAt, func(a *domain.AnalyticsActivity) {
			a.Sessions++
			if len(participants) >= 2 {
				a.CollaborativeSessions++
			}
		})
		usageIDs = append(usageIDs, session.ID)
		usageSessions[session.ID] = session
		collaborative[session.ID] = len(participants) >= 2
	}
	if err := r.analyticsSessionUsage(usageIDs, func(id string, messages []analyticsUsageMessage) {
		session := usageSessions[id]
		for _, message := range messages {
			// A fork copies the prefix, which was already charged to its source.
			if session.ParentSessionID != "" && message.Index < session.ForkMessageIndex {
				continue
			}
			at := message.CreatedAt
			if at.IsZero() && message.StartedAt != nil {
				at = *message.StartedAt
			}
			count := func(a *domain.AnalyticsActivity) {
				accumulateSessionUsage(&a.SessionUsage, message.Usage, at.IsZero())
				if collaborative[id] {
					accumulateSessionUsage(&a.CollaborativeUsage, message.Usage, at.IsZero())
				}
			}
			if at.IsZero() && filter.From.IsZero() && filter.To.IsZero() {
				count(&out.Activity)
			} else {
				add(at, count)
			}
		}
	}); err != nil {
		return err
	}
	for _, point := range points {
		out.ActivityTrend = append(out.ActivityTrend, *point)
	}
	sort.Slice(out.ActivityTrend, func(i, j int) bool { return out.ActivityTrend[i].Date < out.ActivityTrend[j].Date })
	return nil
}

func analyticsActivityInWindow(at time.Time, filter domain.AnalyticsFilter) bool {
	return !at.IsZero() && (filter.From.IsZero() || !at.Before(filter.From)) && (filter.To.IsZero() || !at.After(filter.To))
}
