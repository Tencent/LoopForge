package engine

import "agentflow/internal/domain"

// controlTestActor is the zero (system) actor inserted into Engine control
// method calls by tests: audit fields record it as system-initiated.
var controlTestActor = domain.Actor{}
