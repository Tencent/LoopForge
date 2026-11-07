package domain

import (
	"fmt"
	"strings"
	"time"
)

// A space is a conversation (a ChatRoom, same ID) with the working state the
// chat needs: members, versioned artifacts, side threads and the SOP runs it
// dispatched. A conversation may become a requirement when someone links or
// creates a platform issue for it; progress then lives on that issue and its
// runs rather than on a second lifecycle.

// RequirementSpace is the state layered on a room.
type RequirementSpace struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	// IssueID is the platform requirement this conversation carries, if any.
	IssueID string `json:"issueId,omitempty"`
	Summary string `json:"summary,omitempty"`
	Pinned  bool   `json:"pinned,omitempty"`
	// LaneInputs records the artifact versions each dispatched run started
	// from, keyed by run ID, so later artifact edits never rewrite history.
	LaneInputs map[string][]ArtifactRef `json:"laneInputs,omitempty"`
	CreatedBy  int64                    `json:"createdBy,omitempty"`
	CreatedAt  time.Time                `json:"createdAt"`
	UpdatedAt  time.Time                `json:"updatedAt"`
}

// ArtifactRef pins one artifact version.
type ArtifactRef struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// SpaceMember is a person working in a space with a visible job title.
type SpaceMember struct {
	SpaceID string    `json:"spaceId"`
	UserID  int64     `json:"userId"`
	Name    string    `json:"name"`
	Title   string    `json:"title,omitempty"`
	AddedBy int64     `json:"addedBy,omitempty"`
	AddedAt time.Time `json:"addedAt"`
}

// Space artifact kinds.
var spaceArtifactKinds = map[string]bool{
	"doc": true, "prototype": true, "api": true, "diff": true, "report": true, "ppt": true, "sop": true, "browser": true, "file": true,
}

// ValidSpaceArtifactKind reports whether kind is supported.
func ValidSpaceArtifactKind(kind string) bool { return spaceArtifactKinds[kind] }

// SpaceArtifactBodyLimit bounds one stored artifact version.
const SpaceArtifactBodyLimit = 1 << 20

// SpaceArtifact is a versioned deliverable inside a space. Body holds the
// source (markdown, HTML prototype, JSON slides); URL points at an external
// object such as a preview. WorkflowID links a saved SOP draft.
type SpaceArtifact struct {
	ID         string    `json:"id"`
	SpaceID    string    `json:"spaceId"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Version    int       `json:"version"`
	Summary    string    `json:"summary,omitempty"`
	Format     string    `json:"format,omitempty"` // markdown, html, json, text
	Body       string    `json:"body,omitempty"`
	URL        string    `json:"url,omitempty"`
	RunID      string    `json:"runId,omitempty"`
	WorkflowID string    `json:"workflowId,omitempty"`
	AuthorID   int64     `json:"authorId,omitempty"`
	AuthorName string    `json:"authorName,omitempty"`
	ByAI       bool      `json:"byAi,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Validate checks an artifact before it is stored.
func (a SpaceArtifact) Validate() error {
	if !ValidSpaceArtifactKind(a.Kind) {
		return fmt.Errorf("不支持的产物类型 %q", a.Kind)
	}
	if strings.TrimSpace(a.Title) == "" {
		return fmt.Errorf("产物标题不能为空")
	}
	if len([]rune(a.Title)) > 120 {
		return fmt.Errorf("产物标题不能超过 120 个字")
	}
	if len(a.Body) > SpaceArtifactBodyLimit {
		return fmt.Errorf("产物内容不能超过 1 MiB")
	}
	if u := strings.TrimSpace(a.URL); u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "/") {
		return fmt.Errorf("产物链接必须是 http(s) 地址或站内路径")
	}
	return nil
}

// SpaceThread groups side discussion or one SOP's process away from the
// main timeline.
type SpaceThread struct {
	ID        string    `json:"id"`
	SpaceID   string    `json:"spaceId"`
	Title     string    `json:"title"`
	RunID     string    `json:"runId,omitempty"`
	MessageID string    `json:"messageId,omitempty"`
	CreatedBy int64     `json:"createdBy,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Extra ChatMessage kinds used by requirement spaces.
const (
	ChatKindAI = "ai"
	// ChatKindMilestone marks a notable event on the timeline, such as a
	// requirement being created for the conversation.
	ChatKindMilestone = "milestone"
	ChatKindArtifact  = "artifact"
	ChatKindFlow      = "flow"
)

// AI reply states on ChatKindAI messages.
const (
	AIStatusRunning   = "running"
	AIStatusCompleted = "completed"
	AIStatusFailed    = "failed"
)
