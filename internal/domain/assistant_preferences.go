package domain

import "time"

// AssistantDefaults applies to new selections made with the user's own credentials.
type AssistantDefaults struct {
	Executor string `json:"executor,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Approval string `json:"approval,omitempty"`
}

// AssistantPreferencesInstructionsMax caps the user's own rules for Main Bot.
const AssistantPreferencesInstructionsMax = 4000

// AssistantPreferences is how one user wants Main Bot to work: whether it
// hands work to roles and SOPs more readily (off by default), and extra
// rules added to every turn. SteerOnSend interrupts a running reply when
// sending a message; the default queues it until the reply finishes.
type AssistantPreferences struct {
	ProactiveRoles bool      `json:"proactiveRoles"`
	ProactiveSOPs  bool      `json:"proactiveSops"`
	SteerOnSend    bool      `json:"steerOnSend"`
	Instructions   string    `json:"instructions"`
	UpdatedAt      time.Time `json:"updatedAt,omitempty"`
}
