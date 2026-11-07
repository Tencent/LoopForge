package managedcli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
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

// Rollback is explicit, never triggered by task/model errors. Existing run pins
// stay immutable and automatic updates never retry the rejected version.
func (m *Manager) Rollback(ctx context.Context, provider string) (Report, error) {
	return m.run(ctx, "rollback", provider)
}

func (m *Manager) run(ctx context.Context, action, provider string) (report Report, err error) {
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
		return m.updateLocked(ctx, &state, &report)
	})
	return report, err
}

func (m *Manager) updateLocked(ctx context.Context, state *State, report *Report) error {
	candidates := Inventory{}
	var failures []error
	toolsReady := false
	for _, provider := range providerNames() {
		result := ProviderResult{Provider: provider, From: state.Current[provider].Version}
		candidate, err := m.candidate(ctx, provider)
		if err == nil {
			result.To = candidate.Version
			switch {
			case candidate.Version == result.From:
				result.Status = "unchanged"
			case result.From != "" && compareVersions(candidate.Version, result.From) < 0:
				result.Status, result.Message = "skipped", "automatic downgrade refused"
			case state.Rejected[provider] == candidate.Version:
				result.Status, result.Message = "skipped", "this release was rolled back; waiting for a newer release"
			case m.Now().Before(candidate.Published.Add(observationWindow)):
				result.Status, result.Message = "skipped", "release is still in the observation window"
			}
		}
		if err == nil && result.Status == "" {
			if !toolsReady {
				err = m.prepareTools(ctx)
				toolsReady = err == nil
			}
			installed := state.Candidates[provider]
			if err == nil && (installed.Version != candidate.Version || installed.Integrity != candidate.Dist.Integrity || validateInventory(m.Root, Inventory{provider: installed}) != nil) {
				installed, err = m.install(ctx, provider, candidate)
				if err == nil {
					// Cache before smoke validation: a failed probe must not cause
					// another download. No active inventory changes here.
					state.Candidates[provider] = installed
					err = m.save(*state)
				}
			}
			if err == nil {
				err = m.validate(ctx, provider, installed)
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

func (m *Manager) rollbackLocked(ctx context.Context, provider string, state *State, report *Report) error {
	if _, ok := providerSpecs[provider]; !ok {
		return errors.New("rollback requires a known provider")
	}
	previous, ok := state.Previous[provider]
	if !ok {
		return errors.New("no previous managed version available; global installations were not modified")
	}
	report.Results = append(report.Results, ProviderResult{Provider: provider, From: state.Current[provider].Version, To: previous.Version, Status: "failed"})
	if err := m.prepareTools(ctx); err != nil {
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
