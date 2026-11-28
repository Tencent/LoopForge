package repo

import (
	"errors"
	"testing"

	"agentflow/internal/domain"
)

func TestSaveMCPServersRejectsStaleCreateAndRollsBackBatch(t *testing.T) {
	store, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	original := domain.MCPServerDefinition{
		ID: "personal-mcp", Name: "Personal", Scope: "personal", OwnerUserID: 1,
		Transport: "http", URL: "https://example.test/mcp",
	}
	if _, err := store.SaveMCPServers(MCPServerChange{Definition: original}); err != nil {
		t.Fatal(err)
	}
	newServer := original
	newServer.ID = "another-mcp"
	if _, err := store.SaveMCPServers(
		MCPServerChange{Definition: newServer},
		MCPServerChange{Definition: original},
	); !errors.Is(err, ErrMCPServerChanged) {
		t.Fatalf("stale create returned %v", err)
	}
	if _, found := store.MCPServer(newServer.ID); found {
		t.Fatal("failed import left a partial write")
	}
	edited := original
	edited.Name = "Updated personal"
	if _, err := store.SaveMCPServers(MCPServerChange{Definition: edited, Previous: original}); err != nil {
		t.Fatal(err)
	}
	if saved, found := store.MCPServer(original.ID); !found || saved.OwnerUserID != original.OwnerUserID || saved.Name != edited.Name {
		t.Fatalf("saved definition = %+v", saved)
	}
}

// TestSaveMCPServersRejectsDuplicateRowInBatch verifies the repo-level guard:
// if two entries in one batch resolve to the same stored row (identical ID or,
// on a collating DB, collation-equivalent IDs) the transaction must be rolled
// back and ErrMCPImportDuplicateID returned — not a silent overwrite.
// The SQLite test store uses binary comparison, so this exercises the path via
// literally equal IDs that bypass the handler's ToLower precheck.
func TestSaveMCPServersRejectsDuplicateRowInBatch(t *testing.T) {
	store, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	original := domain.MCPServerDefinition{
		ID: "dup-row-mcp", Name: "Original", Scope: "personal", OwnerUserID: 2,
		Transport: "http", URL: "https://example.test/original",
	}
	if _, err := store.SaveMCPServers(MCPServerChange{Definition: original}); err != nil {
		t.Fatal(err)
	}
	firstUpdate := original
	firstUpdate.Name = "First Update"
	firstUpdate.URL = "https://example.test/first"
	secondUpdate := original
	secondUpdate.Name = "Second Update"
	secondUpdate.URL = "https://example.test/second"
	_, err = store.SaveMCPServers(
		MCPServerChange{Definition: firstUpdate, Previous: original},
		MCPServerChange{Definition: secondUpdate, Previous: original},
	)
	if !errors.Is(err, ErrMCPImportDuplicateID) {
		t.Fatalf("two entries targeting same row: got %v, want ErrMCPImportDuplicateID", err)
	}
	// Transaction must have rolled back; stored row must still be the original.
	if saved, ok := store.MCPServer(original.ID); !ok || saved.Name != "Original" || saved.URL != original.URL {
		t.Fatalf("rolled-back batch changed the stored MCP: %+v", saved)
	}
}

func TestRunnerRenewalRequiresExistingOwnerScope(t *testing.T) {
	store, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	runner := domain.Runner{ID: "personal-runner", OwnerUserID: 1, SessionTokenHash: "initial"}
	if err := store.RegisterRunner(runner, false); err != nil {
		t.Fatal(err)
	}
	stale := runner
	stale.OwnerUserID, stale.SessionTokenHash = 0, "stale"
	if err := store.RegisterRunner(stale, true); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("renewal with stale owner returned %v", err)
	}
	if saved, found := store.Runner(runner.ID); !found || saved.OwnerUserID != runner.OwnerUserID || saved.SessionTokenHash != "initial" {
		t.Fatalf("stored binding changed: %+v", saved)
	}
	runner.SessionTokenHash = "renewed"
	if err := store.RegisterRunner(runner, true); err != nil {
		t.Fatal(err)
	}
	if saved, found := store.Runner(runner.ID); !found || saved.OwnerUserID != runner.OwnerUserID || saved.SessionTokenHash != "renewed" {
		t.Fatalf("session was not renewed: %+v", saved)
	}
}
