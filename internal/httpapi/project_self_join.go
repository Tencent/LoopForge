package httpapi

import (
	"strings"

	"agentflow/internal/domain"
)

// maxSignupProjects bounds how many projects one sign-up request may join.
const maxSignupProjects = 50

// joinableProject is the public summary of a project open to self-service
// joining. The sign-up page shows it before the visitor has an account, so
// it carries display fields only.
type joinableProject struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Color       string `json:"color,omitempty"`
}

func projectSelfJoinable(p domain.Project) bool {
	return p.SelfJoin && p.ArchivedAt == nil
}

func projectHasMember(p domain.Project, userID int64) bool {
	if userID <= 0 {
		return false
	}
	if p.OwnerID == userID {
		return true
	}
	for _, m := range p.Members {
		if m.UserID == userID {
			return true
		}
	}
	return false
}

func joinableProjectSummaries(projects []domain.Project) []joinableProject {
	out := []joinableProject{}
	for _, p := range projects {
		if !projectSelfJoinable(p) {
			continue
		}
		out = append(out, joinableProject{ID: p.ID, Key: p.Key, Name: p.Name, Description: p.Description, Color: p.Color})
	}
	return out
}

// normalizeProjectIDs trims and de-duplicates requested project ids,
// keeping the caller's order.
func normalizeProjectIDs(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
