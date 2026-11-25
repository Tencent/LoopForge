package managedcli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"time"
)

type Manager struct {
	Root       string
	Client     *http.Client
	RunCommand CommandRunner
	Now        func() time.Time
	nodeBinary string // Resolved from the worker process PATH, not local policy.
	npmBinary  string
}

type ProviderResult struct {
	Provider   string `json:"provider"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	Validation string `json:"validation,omitempty"`
}

type Report struct {
	Action     string           `json:"action"`
	StartedAt  time.Time        `json:"startedAt"`
	FinishedAt time.Time        `json:"finishedAt"`
	Results    []ProviderResult `json:"results"`
	Error      string           `json:"error,omitempty"`
}

func NewManager(root string) (*Manager, error) {
	if err := validRoot(root); err != nil {
		return nil, err
	}
	return &Manager{
		Root: root, RunCommand: executeCommand, Now: time.Now,
		Client: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) >= 5 {
				return errors.New("refuse insecure or excessive registry redirects")
			}
			return nil
		}},
	}, nil
}

// Check claims today's Beijing window before any external work. Failed or
// interrupted attempts consume that date; manual Update remains available.
func (m *Manager) Check(ctx context.Context) (Report, error) {
	return m.run(ctx, "check", "")
}

// Update stages independent providers without blocking tasks. Only publication
// of Current enters maintenance; failures retain the previous active version.
func (m *Manager) Update(ctx context.Context) (Report, error) {
	return m.run(ctx, "update", "")
}

// UpdateProvider updates a single CLI already selected by the runner.
func (m *Manager) UpdateProvider(ctx context.Context, provider string) (Report, error) {
	if !Supports(provider) {
		return Report{}, fmt.Errorf("unsupported managed CLI %q", provider)
	}
	return m.run(ctx, "update", provider)
}

// Rollback is explicit, never triggered by task/model errors. Existing run pins
// stay immutable and automatic updates never retry the rejected version.
func (m *Manager) Rollback(ctx context.Context, provider string) (Report, error) {
	return m.run(ctx, "rollback", provider)
}

func (m *Manager) run(ctx context.Context, action, provider string) (report Report, err error) {
	var providers []string
	if action != "rollback" {
		providers, err = EnabledProviders()
		if err != nil {
			return Report{}, err
		}
		if provider != "" {
			if !slices.Contains(providers, provider) {
				return Report{}, fmt.Errorf("managed CLI %q is not enabled by %s", provider, providerAllowlistEnv)
			}
			providers = []string{provider}
		}
	}
	err = WithUpdateLock(ctx, m.Root, func() (operationErr error) {
		now := m.Now().UTC()
		if action == "check" && now.Before(scheduledAt(m.Root, now)) {
			return nil
		}
		state, err := ReadState(m.Root)
		if err != nil {
			return err
		}
		if action == "check" {
			if state.LastCheck.In(beijing).Format("2006-01-02") >= now.In(beijing).Format("2006-01-02") {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			state.LastCheck = now
			if err := m.save(state); err != nil {
				return err
			}
		}
		report = Report{Action: action, StartedAt: now, Results: []ProviderResult{}}
		defer func() {
			report.FinishedAt = m.Now().UTC()
			if operationErr != nil {
				report.Error = operationErr.Error()
			}
			operationErr = errors.Join(operationErr, atomicJSON(filepath.Join(m.Root, "last-report.json"), report))
		}()
		if action == "rollback" {
			return m.rollbackLocked(ctx, provider, &state, &report)
		}
		return m.updateProvidersLocked(ctx, &state, &report, providers)
	})
	return report, err
}

func (m *Manager) updateProvidersLocked(ctx context.Context, state *State, report *Report, providers []string) error {
	candidates := Inventory{}
	var failures []error
	toolsReady := false
	for _, provider := range providers {
		result := ProviderResult{Provider: provider, From: state.Current[provider].Version}
		candidate, err := m.candidate(ctx, provider)
		if err == nil && candidate.Artifact != nil {
			candidate, err = m.observeVendorReleases(state, provider, candidate)
		}
		if err == nil {
			result.To = candidate.Version
			result.Status, result.Message = m.releaseGate(state, provider, result.From, candidate)
		}
		if err == nil && result.Status == "" {
			if candidate.Artifact != nil {
				err = m.prepareCommandDirectories()
			} else if !toolsReady {
				err = m.prepareTools(ctx)
				toolsReady = err == nil
			}
			var installed Installation
			if err == nil {
				installed, err = m.stageCandidate(ctx, state, provider, candidate)
			}
			if err == nil {
				candidates[provider] = installed
				result.Status, result.Validation = "staged", "version-and-required-arguments"
			}
		}
		if err != nil {
			result.Status, result.Message = "failed", err.Error()
			failures = append(failures, fmt.Errorf("%s: %w", provider, err))
		}
		report.Results = append(report.Results, result)
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
	}
	if len(candidates) != 0 {
		for provider, candidate := range candidates {
			if old, ok := state.Current[provider]; ok {
				state.Previous[provider] = old
			}
			state.Current[provider] = candidate
			delete(state.Rejected, provider)
		}
		err := m.activate(ctx, *state)
		for i := range report.Results {
			if report.Results[i].Status == "staged" {
				if err == nil {
					report.Results[i].Status = "updated"
				} else {
					report.Results[i].Message = "candidate retained; activation deferred: " + err.Error()
				}
			}
		}
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// releaseGate reports why a resolved release must not be staged for provider.
// An empty status means the release is eligible for installation.
func (m *Manager) releaseGate(state *State, provider, from string, candidate release) (status, message string) {
	switch {
	case candidate.Version == from:
		return "unchanged", ""
	case from != "" && !newerRelease(provider, candidate, from, state.Current[provider].PublishedAt):
		return "skipped", "automatic downgrade refused"
	case blockedByRollback(state, provider, candidate):
		if state.Rejected[provider] == candidate.Version {
			return "skipped", "this release was rolled back; waiting for a newer release"
		}
		return "skipped", "waiting for a release newer than the rolled-back version"
	case m.Now().Before(candidate.Published.Add(observationWindow)):
		return "skipped", "release is still in the observation window"
	}
	return "", ""
}

func blockedByRollback(state *State, provider string, candidate release) bool {
	rejected := state.Rejected[provider]
	if rejected == "" {
		return false
	}
	// An intermediate release could otherwise activate and clear the
	// rejection, allowing the rolled-back version to return later.
	if !validProviderVersion(provider, rejected) {
		return true
	}
	var published *time.Time
	if previous := state.Previous[provider]; previous.Version == rejected {
		published = previous.PublishedAt
	}
	return !newerRelease(provider, candidate, rejected, published)
}

func newerRelease(provider string, candidate release, from string, published *time.Time) bool {
	if candidate.Version == from {
		return false
	}
	if provider == "cursor" {
		// Build hashes have no ordering within a day. The artifact's
		// timestamp must establish a strictly newer same-day release.
		candidateDate, currentDate := candidate.Version[:10], from[:10]
		if candidateDate != currentDate {
			return candidateDate > currentDate
		}
		return published != nil && candidate.Published.After(*published)
	}
	return compareVersions(candidate.Version, from) > 0
}

// stageCandidate reuses the cached candidate installation when it still matches
// the release, otherwise installs it, then smoke-validates the result.
func (m *Manager) stageCandidate(ctx context.Context, state *State, provider string, candidate release) (Installation, error) {
	installed := state.Candidates[provider]
	if installed.Version != candidate.Version || installed.Integrity != candidate.identity() || validateInventory(m.Root, Inventory{provider: installed}) != nil {
		var err error
		if installed, err = m.install(ctx, provider, candidate); err != nil {
			return Installation{}, err
		}
		// Cache before smoke validation: a failed probe must not cause
		// another download. No active inventory changes here.
		state.Candidates[provider] = installed
		if err := m.save(*state); err != nil {
			return Installation{}, err
		}
	}
	if err := m.validate(ctx, provider, installed); err != nil {
		return Installation{}, err
	}
	return installed, nil
}

func (m *Manager) rollbackLocked(ctx context.Context, provider string, state *State, report *Report) error {
	if !Supports(provider) {
		return errors.New("rollback requires a known provider")
	}
	previous, ok := state.Previous[provider]
	if !ok {
		return errors.New("no previous managed version available; global installations were not modified")
	}
	report.Results = append(report.Results, ProviderResult{Provider: provider, From: state.Current[provider].Version, To: previous.Version, Status: "failed"})
	var err error
	if providerSpecs[provider].Package == "" {
		err = m.prepareCommandDirectories()
	} else {
		err = m.prepareTools(ctx)
	}
	if err != nil {
		return err
	}
	if err := m.validate(ctx, provider, previous); err != nil {
		return fmt.Errorf("rollback candidate validation: %w", err)
	}
	state.Rejected[provider] = state.Current[provider].Version
	state.Previous[provider], state.Current[provider] = state.Current[provider], previous
	if err := m.activate(ctx, *state); err != nil {
		return err
	}
	report.Results[0].Status = "rolled-back"
	return nil
}

// The caller holds updater.lock throughout: no second state read is necessary.
func (m *Manager) activate(ctx context.Context, state State) error {
	ctx, cancel := context.WithTimeout(ctx, maintenanceTimeout)
	defer cancel()
	return WithMaintenance(ctx, m.Root, func() error {
		if err := validateInventory(m.Root, state.Current); err != nil {
			return err
		}
		state.UpdatedAt = m.Now().UTC()
		if err := ctx.Err(); err != nil {
			return err
		}
		return m.save(state)
	})
}

func (m *Manager) save(state State) error {
	return atomicJSON(filepath.Join(m.Root, "state.json"), state)
}
