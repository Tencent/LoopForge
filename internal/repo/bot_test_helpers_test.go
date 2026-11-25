package repo

import "testing"

// pinDBClock fixes the database clock used for Bot leases at start (epoch
// microseconds) and returns a function that advances it.
func pinDBClock(t *testing.T, start int64) func(deltaUS int64) {
	t.Helper()
	dbClockOverrideUS.Store(start)
	t.Cleanup(func() { dbClockOverrideUS.Store(0) })
	return func(delta int64) { dbClockOverrideUS.Add(delta) }
}
