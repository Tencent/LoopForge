package httpapi

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// Files a deleted conversation listed are removed in the background, not in
// the delete request: proving nothing else lists a file means scanning the
// conversation, pending-turn and room message tables, so the sweeper scans
// once for every deleted conversation of a batch instead of once per delete.
//
// The queue is a directory of empty marker files named <owner>-<id>, so it
// survives restarts and a crash at worst leaves a file behind until the next
// sweep. A sweep checks references when it runs, after the deletion: a file a
// fork, a pending turn or a room message still lists stays, and its marker is
// dropped; deleting the last conversation listing it queues it again.
const (
	attachmentSweepBatch    = 512
	attachmentSweepDebounce = 10 * time.Second
	attachmentSweepEvery    = 30 * time.Minute
)

func attachmentSweepQueueDir() string {
	return filepath.Join(assistantSessionBase(), "attachment-gc")
}

// queueConversationAttachments queues the files the messages of a deleted
// conversation listed. Each lives in the attachment directory of whoever
// posted it: the message author, else the conversation owner.
func (h *assistantHandlers) queueConversationAttachments(conv domain.AssistantSession) {
	queued := 0
	for _, m := range conv.Messages {
		owner := m.AuthorID
		if owner == 0 {
			owner = conv.UserID
		}
		for _, a := range m.Attachments {
			if owner <= 0 || !validWorkbenchAttachmentID(a.ID) {
				continue
			}
			if queued == 0 {
				if err := os.MkdirAll(attachmentSweepQueueDir(), 0o700); err != nil {
					log.Printf("queue attachments of conversation %s: %v", conv.ID, err)
					return
				}
			}
			name := filepath.Join(attachmentSweepQueueDir(), strconv.FormatInt(owner, 10)+"-"+a.ID)
			if err := os.WriteFile(name, nil, 0o600); err != nil {
				log.Printf("queue attachment %s of conversation %s: %v", a.ID, conv.ID, err)
				continue
			}
			queued++
		}
	}
	if queued > 0 && h.attachmentSweepWake != nil {
		select {
		case h.attachmentSweepWake <- struct{}{}:
		default:
		}
	}
}

type queuedAttachment struct {
	marker string
	owner  int64
	id     string
}

func parseQueuedAttachment(name string) (queuedAttachment, bool) {
	ownerText, id, ok := strings.Cut(name, "-")
	owner, err := strconv.ParseInt(ownerText, 10, 64)
	if !ok || err != nil || owner <= 0 || !validWorkbenchAttachmentID(id) {
		return queuedAttachment{}, false
	}
	return queuedAttachment{marker: name, owner: owner, id: id}, true
}

// sweepQueuedAttachments works through the queue in batches and returns how
// many files it removed. A batch whose reference check fails stays queued for
// the next sweep.
func (h *assistantHandlers) sweepQueuedAttachments() int {
	root, err := os.OpenRoot(attachmentSweepQueueDir())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("attachment sweep: %v", err)
		}
		return 0
	}
	defer root.Close()
	entries, err := os.ReadDir(attachmentSweepQueueDir())
	if err != nil {
		log.Printf("attachment sweep: %v", err)
		return 0
	}
	var queue []queuedAttachment
	for _, entry := range entries {
		if q, ok := parseQueuedAttachment(entry.Name()); ok && entry.Type().IsRegular() {
			queue = append(queue, q)
		} else {
			_ = root.RemoveAll(entry.Name())
		}
	}
	removed := 0
	for start := 0; start < len(queue); start += attachmentSweepBatch {
		batch := queue[start:min(start+attachmentSweepBatch, len(queue))]
		ids := make([]string, len(batch))
		for i, q := range batch {
			ids[i] = q.id
		}
		inUse, err := h.repo.AssistantAttachmentsInUse(ids)
		if err == nil {
			var rooms map[string]bool
			if rooms, err = h.repo.RoomAttachmentsInUse(ids); err == nil {
				for id := range rooms {
					inUse[id] = true
				}
			}
		}
		if err != nil {
			log.Printf("attachment sweep: %v; %d files stay queued", err, len(queue)-start)
			return removed
		}
		for _, q := range batch {
			if !inUse[q.id] && removeWorkbenchAttachment(q.owner, q.id) {
				removed++
			}
			_ = root.Remove(q.marker)
		}
	}
	return removed
}

// startAttachmentSweeper sweeps shortly after deletions queue files, waiting
// a little so a burst of deletions shares one sweep, and periodically for
// files queued before a restart.
func (h *assistantHandlers) startAttachmentSweeper(ctx context.Context) {
	if h == nil || h.repo == nil || ctx == nil || flag.Lookup("test.v") != nil {
		return
	}
	go func() {
		ticker := time.NewTicker(attachmentSweepEvery)
		defer ticker.Stop()
		sweep := func() {
			if n := h.sweepQueuedAttachments(); n > 0 {
				log.Printf("attachment sweep: removed %d files of deleted conversations", n)
			}
		}
		sweep()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			case <-h.attachmentSweepWake:
				select {
				case <-ctx.Done():
					return
				case <-time.After(attachmentSweepDebounce):
				}
				sweep()
			}
		}
	}()
}
