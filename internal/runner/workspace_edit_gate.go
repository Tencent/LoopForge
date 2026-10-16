package runner

import "sync"

// Runner-side fencing remains valid if the API request times out: a task cannot
// start using this run's workspace until the actual file operation has ended.
func (d *Daemon) workspaceGate(runID string) *sync.RWMutex {
	gate, _ := d.workspaceGates.LoadOrStore(runID, &sync.RWMutex{})
	return gate.(*sync.RWMutex)
}
