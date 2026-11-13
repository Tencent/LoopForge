package managedcli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// stageFixture returns a manager whose registry offers codex 2.0.0 and whose
// tools are prepared, plus the resolved codex candidate.
func stageFixture(t *testing.T) (*updaterFixture, release) {
	t.Helper()
	f := newUpdaterFixture(t)
	f.update(t, false)
	f.setVersion("2.0.0")
	ctx := context.Background()
	must(t, f.manager.prepareTools(ctx))
	candidate, err := f.manager.candidate(ctx, "codex")
	must(t, err)
	return f, candidate
}

func TestStageCandidateInstallsCachesAndReuses(t *testing.T) {
	f, candidate := stageFixture(t)
	ctx := context.Background()
	state := f.state(t)
	installed, err := f.manager.stageCandidate(ctx, &state, "codex", candidate)
	must(t, err)
	if installed.Version != "2.0.0" || f.installs["codex"] != 2 {
		t.Fatalf("installed=%+v installs=%d", installed, f.installs["codex"])
	}
	if state.Candidates["codex"] != installed || f.state(t).Candidates["codex"] != installed {
		t.Fatal("staged candidate was not cached in memory and on disk")
	}
	// A matching, intact cache is validated again but never reinstalled.
	again, err := f.manager.stageCandidate(ctx, &state, "codex", candidate)
	must(t, err)
	if again != installed || f.installs["codex"] != 2 {
		t.Fatalf("cache not reused: %+v installs=%d", again, f.installs["codex"])
	}
	// A different integrity for the same version invalidates the cache.
	changed := candidate
	changed.Dist.Integrity = "sha512-other"
	if _, err := f.manager.stageCandidate(ctx, &state, "codex", changed); err == nil {
		t.Fatal("lockfile integrity mismatch accepted")
	}
	if f.installs["codex"] != 3 {
		t.Fatalf("integrity change did not trigger reinstall: installs=%d", f.installs["codex"])
	}
}

func TestStageCandidateSaveFailureStopsBeforeValidation(t *testing.T) {
	f, candidate := stageFixture(t)
	state := f.state(t)
	statePath := filepath.Join(f.manager.Root, "state.json")
	must(t, os.Remove(statePath))
	must(t, os.Mkdir(statePath, 0700))
	probes := len(f.environments)
	installed, err := f.manager.stageCandidate(context.Background(), &state, "codex", candidate)
	if err == nil {
		t.Fatal("save failure ignored")
	}
	if installed != (Installation{}) {
		t.Fatalf("returned installation on failure: %+v", installed)
	}
	// Only the npm install ran; no --version/--help smoke probe followed.
	if f.installs["codex"] != 2 || len(f.environments)-probes != 2 {
		t.Fatalf("installs=%d commands=%d", f.installs["codex"], len(f.environments)-probes)
	}
}

func TestStageCandidateValidationFailureKeepsCache(t *testing.T) {
	f, candidate := stageFixture(t)
	f.failHelp = "codex"
	state := f.state(t)
	installed, err := f.manager.stageCandidate(context.Background(), &state, "codex", candidate)
	if err == nil || installed != (Installation{}) {
		t.Fatalf("installed=%+v err=%v", installed, err)
	}
	if cached := f.state(t).Candidates["codex"]; cached.Version != "2.0.0" {
		t.Fatalf("failed smoke test dropped the cached download: %+v", cached)
	}
	if f.state(t).Current["codex"].Version != "1.0.0" {
		t.Fatal("staging changed the active inventory")
	}
}
