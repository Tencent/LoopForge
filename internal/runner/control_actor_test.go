package runner

import "agentflow/internal/domain"

// engineControlTestActor is the zero (system) actor inserted into engine
// control method calls by runner tests.
func engineControlTestActor() domain.Actor { return domain.Actor{} }
