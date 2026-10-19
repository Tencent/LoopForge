package domain

import "time"

// MRDeliveryState persists Gongfeng MR merge status for delivery analytics.
// Rows are discovered from run artifacts (work → baseline) and from the
// integrate → default promote MR, then refreshed by a background sync job.
type MRDeliveryState struct {
	MRProject    string    `json:"mrProject"`
	MRIID        int       `json:"mrIid"`
	MRURL        string    `json:"mrUrl,omitempty"`
	State        string    `json:"state"` // Gongfeng raw: merged, opened, closed, reopened, unknown
	TargetBranch string    `json:"targetBranch,omitempty"`
	IssueID      string    `json:"issueId,omitempty"`
	CheckedAt    time.Time `json:"checkedAt,omitempty"`
	CreatedAt    time.Time `json:"createdAt,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt,omitempty"`
}

// MRDeliveryKey identifies a Gongfeng merge request.
type MRDeliveryKey struct {
	Project string
	IID     int
}
