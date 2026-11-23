package domain

import (
	"strings"
	"time"
)

// AssistantDefaults applies to new selections made with the user's own credentials.
type AssistantDefaults struct {
	Executor string `json:"executor,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Approval string `json:"approval,omitempty"`
}

// AssistantPreferencesInstructionsMax caps the user's own rules for Main Bot.
const AssistantPreferencesInstructionsMax = 4000

// Approval review modes for "帮我批准".
const (
	ApprovalReviewModeFollow = "follow"
	ApprovalReviewModeCLI    = "cli"
	ApprovalReviewModeAPI    = "api"
)

// ApprovalReviewAPIConfig stores an optional standalone HTTP API configuration
// used solely for "帮我批准" AI review without requiring a configured CLI.
type ApprovalReviewAPIConfig struct {
	Protocol         string `json:"protocol,omitempty"`
	BaseURL          string `json:"baseUrl,omitempty"`
	Model            string `json:"model,omitempty"`
	APIKey           string `json:"apiKey,omitempty"`
	APIKeyConfigured bool   `json:"apiKeyConfigured,omitempty"`
	APIKeyClear      bool   `json:"apiKeyClear,omitempty"`
}

// AssistantPreferences is how one user wants Main Bot to work: whether it
// hands work to roles and SOPs more readily (off by default), extra rules
// added to every turn, and optional "帮我批准" review model settings.
// SteerOnSend interrupts a running reply when sending a message; the default
// queues it until the reply finishes.
type AssistantPreferences struct {
	ProactiveRoles      bool                     `json:"proactiveRoles"`
	ProactiveSOPs       bool                     `json:"proactiveSops"`
	SteerOnSend         bool                     `json:"steerOnSend"`
	Instructions        string                   `json:"instructions"`
	ApprovalReviewMode  string                   `json:"approvalReviewMode,omitempty"`
	ApprovalReviewModel string                   `json:"approvalReviewModel,omitempty"`
	ApprovalReviewAPI   *ApprovalReviewAPIConfig `json:"approvalReviewApi,omitempty"`
	UpdatedAt           time.Time                `json:"updatedAt,omitempty"`
}

// EffectiveApprovalReviewMode returns the normalized review mode, preserving
// backward compatibility when ApprovalReviewModel was saved without a mode.
func (p AssistantPreferences) EffectiveApprovalReviewMode() string {
	switch strings.TrimSpace(p.ApprovalReviewMode) {
	case ApprovalReviewModeFollow, ApprovalReviewModeCLI, ApprovalReviewModeAPI:
		return strings.TrimSpace(p.ApprovalReviewMode)
	}
	if strings.TrimSpace(p.ApprovalReviewModel) != "" {
		return ApprovalReviewModeCLI
	}
	return ApprovalReviewModeFollow
}

// Sanitized returns a copy safe to send in API responses: the raw custom
// review API key is stripped and replaced with APIKeyConfigured.
func (p AssistantPreferences) Sanitized() AssistantPreferences {
	out := p
	out.ApprovalReviewMode = p.EffectiveApprovalReviewMode()
	if p.ApprovalReviewAPI != nil {
		cp := *p.ApprovalReviewAPI
		cp.APIKeyConfigured = strings.TrimSpace(cp.APIKey) != "" || cp.APIKeyConfigured
		cp.APIKey = ""
		cp.APIKeyClear = false
		out.ApprovalReviewAPI = &cp
	}
	return out
}
