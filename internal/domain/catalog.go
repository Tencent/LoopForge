package domain

import "strings"

// CatalogOwned reports whether a catalog item belongs to the current project.
func CatalogOwned(itemProjectID, currentProjectID string) bool {
	item := strings.TrimSpace(itemProjectID)
	current := strings.TrimSpace(currentProjectID)
	return current != "" && item == current
}

// CatalogSharedWith reports whether a catalog item should appear in the
// "shared" tab of the current project. Legacy items with an empty projectId
// stay visible as shared so existing production SOPs and roles do not vanish.
func CatalogSharedWith(itemProjectID string, shared bool, currentProjectID string) bool {
	item := strings.TrimSpace(itemProjectID)
	current := strings.TrimSpace(currentProjectID)
	if item == "" {
		return true
	}
	if current != "" && item == current {
		return false
	}
	return shared
}

// CatalogVisible reports whether a project can see a catalog item: it owns it,
// the item was shared, or the item is a legacy global record.
func CatalogVisible(itemProjectID string, shared bool, currentProjectID string) bool {
	return CatalogOwned(itemProjectID, currentProjectID) || CatalogSharedWith(itemProjectID, shared, currentProjectID)
}

// KeepCatalogProjectID keeps an existing catalog item in its project when a
// write omits projectId (slim bootstrap PUT, old clients).
func KeepCatalogProjectID(incoming, existing string) string {
	if id := strings.TrimSpace(incoming); id != "" {
		return id
	}
	return strings.TrimSpace(existing)
}

// AssetVisible reports whether a run-config asset belongs to the current
// project. Empty projectId means a platform-wide default (executors, sandboxes).
func AssetVisible(itemProjectID, currentProjectID string) bool {
	item := strings.TrimSpace(itemProjectID)
	current := strings.TrimSpace(currentProjectID)
	return item == "" || current == "" || item == current
}

// RunProjectID prefers the stamped execution context, then a linked issue.
func RunProjectID(run Run, issueProjectID string) string {
	if id := strings.TrimSpace(run.ExecutionContext.ProjectID); id != "" {
		return id
	}
	return strings.TrimSpace(issueProjectID)
}
