package domain

import "time"

const (
	JoinRequestPending   = "pending"
	JoinRequestApproved  = "approved"
	JoinRequestRejected  = "rejected"
	JoinRequestWithdrawn = "withdrawn"
)

// ProjectJoinReasonMaxRunes bounds the applicant's note.
const ProjectJoinReasonMaxRunes = 500

// ProjectJoinRequest asks a project's owner or maintainers to add the
// applicant as a member. Role is the requested role while pending and the
// granted role once approved.
type ProjectJoinRequest struct {
	ID         int64      `json:"id"`
	ProjectID  string     `json:"projectId"`
	UserID     int64      `json:"userId"`
	Role       string     `json:"role"`
	Reason     string     `json:"reason,omitempty"`
	Status     string     `json:"status"`
	ReviewerID int64      `json:"reviewerId,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	ReviewedAt *time.Time `json:"reviewedAt,omitempty"`
}

// ProjectMemberRole returns the member role of userID, or "" when the user
// is not a listed member.
func ProjectMemberRole(p Project, userID int64) string {
	for _, m := range p.Members {
		if m.UserID == userID {
			role, _ := NormalizeProjectMemberRole(m.Role)
			return role
		}
	}
	return ""
}

// CanReviewProjectJoin reports whether userID may approve or reject join
// requests: the owner and maintainers. Platform admins are checked by callers.
func CanReviewProjectJoin(p Project, userID int64) bool {
	if userID <= 0 {
		return false
	}
	return p.OwnerID == userID || ProjectMemberRole(p, userID) == ProjectRoleMaintainer
}
