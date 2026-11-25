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
