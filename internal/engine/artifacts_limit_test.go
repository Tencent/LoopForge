package engine

import "testing"

// The platform diagnostics page compares this limit with the database column
// capacity, so it must stay aligned with the runner upload cap (1 MiB).
func TestArtifactSizeLimit(t *testing.T) {
	if got := ArtifactSizeLimit(); got != 1<<20 {
		t.Fatalf("ArtifactSizeLimit() = %d, want %d", got, 1<<20)
	}
}
