package httpapi

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const (
	assistantSessionJanitorDelay = time.Minute
	assistantSessionJanitorEvery = 24 * time.Hour
	// scheduledSessionRetention keeps a few days of a scheduled task's history;
	// the latest run of every task is always kept, even when it is older.
	scheduledSessionRetention = 3 * 24 * time.Hour
)

// startSessionJanitor trims old scheduled conversations on startup and then
// daily. It only ever tombstones rows that already carry source=scheduled:
// rows written before the source column existed are marked by
// migrations/021_assistant_session_sources.sql, applied manually by the
// operator, because production never migrates data on boot. SOP conversations
// are deliberately left alone.
func (h *assistantHandlers) startSessionJanitor(ctx context.Context) {
	if h == nil || h.repo == nil || ctx == nil {
		return
	}
	if flag.Lookup("test.v") != nil || os.Getenv("AGENTFLOW_ASSISTANT_SESSION_JANITOR") == "0" {
		return
	}
	go func() {
		timer := time.NewTimer(assistantSessionJanitorDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if n := h.pruneScheduledAssistantSessions(time.Now().UTC()); n > 0 {
					log.Printf("assistant session cleanup: removed %d scheduled conversations", n)
				}
				if n := h.pruneOrphanSharedRoots(time.Now()); n > 0 {
					log.Printf("assistant session cleanup: removed %d orphaned shared conversation roots", n)
				}
				timer.Reset(assistantSessionJanitorEvery)
			}
		}
	}()
}

// pruneScheduledAssistantSessions keeps the newest conversation of every
// tracked scheduled task and drops its older runs once they pass the retention
// window. Legacy rows without a task id follow the plain retention rule and are
// grouped by owner, project and title only to keep their ordering stable.
func (h *assistantHandlers) pruneScheduledAssistantSessions(now time.Time) int {
	if h == nil || h.repo == nil {
		return 0
	}
	items := h.repo.ListAssistantSessionsBySource(domain.AssistantSourceScheduled)
	groups := make(map[string][]domain.AssistantSession, len(items))
	for _, item := range items {
		key := strings.TrimSpace(item.TaskID)
		if key == "" {
			key = fmt.Sprintf("legacy:%d:%s:%s", item.UserID, item.ProjectID, item.Title)
		}
		groups[key] = append(groups[key], item)
	}
	removed := 0
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool { return group[i].UpdatedAt.After(group[j].UpdatedAt) })
		// Tracked tasks keep their latest result even when it is older than the
		// window; pre-marker rows have no task to compare with, so they follow
		// the plain retention rule and disappear once they are old enough.
		tracked := strings.TrimSpace(group[0].TaskID) != ""
		for i, item := range group {
			if tracked && i == 0 {
				continue
			}
			if item.Archived {
				continue
			}
			if item.UpdatedAt.IsZero() || now.Sub(item.UpdatedAt) < scheduledSessionRetention {
				continue
			}
			if err := h.deleteConversation(item.UserID, item.ID); err != nil {
				log.Printf("assistant session cleanup id=%s: %v", item.ID, err)
				continue
			}
			removed++
		}
	}
	return removed
}

// sharedRootOrphanAge leaves a root alone while the conversation that owns it
// may still be in the middle of being saved.
const sharedRootOrphanAge = 24 * time.Hour

// pruneOrphanSharedRoots removes shared native roots whose conversation no
// longer exists. Deleting a conversation used to leave its shared root behind,
// and each one holds a full CLI home. An unreadable session list skips the run.
func (h *assistantHandlers) pruneOrphanSharedRoots(now time.Time) int {
	if h == nil || h.repo == nil {
		return 0
	}
	base := filepath.Dir(assistantSharedSessionRoot("-"))
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	ids, err := h.repo.AssistantSessionIDs()
	if err != nil {
		log.Printf("assistant shared root cleanup skipped: %v", err)
		return 0
	}
	live := make(map[string]bool, len(ids))
	for _, id := range ids {
		live[filepath.Base(assistantSharedSessionRoot(id))] = true
	}
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || live[name] || !sharedRootName(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < sharedRootOrphanAge {
			continue
		}
		if err := os.RemoveAll(filepath.Join(base, name)); err != nil {
			log.Printf("assistant shared root cleanup %s: %v", name, err)
			continue
		}
		removed++
	}
	return removed
}

func sharedRootName(name string) bool {
	_, err := hex.DecodeString(name)
	return err == nil && len(name) == 64 && strings.ToLower(name) == name
}

// deleteScheduledSessions removes every conversation a deleted scheduled task
// left behind; keepID preserves the run that is being written right now.
func (h *assistantHandlers) deleteScheduledSessions(taskID, keepID string) {
	taskID = strings.TrimSpace(taskID)
	if h == nil || h.repo == nil || taskID == "" {
		return
	}
	for _, item := range h.repo.ListAssistantSessionsByTask(domain.AssistantSourceScheduled, taskID) {
		if item.ID == keepID {
			continue
		}
		if err := h.deleteConversation(item.UserID, item.ID); err != nil {
			log.Printf("assistant scheduled session cleanup task=%s id=%s: %v", taskID, item.ID, err)
		}
	}
}

func (h *assistantHandlers) deleteScheduledSessionsExcept(taskID, keepID string) {
	h.deleteScheduledSessions(taskID, keepID)
}

func (h *assistantHandlers) deleteScheduledSessionsForTask(taskID string) {
	h.deleteScheduledSessions(taskID, "")
}
