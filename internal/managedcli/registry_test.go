package managedcli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSelectMatureReleaseWithoutFollowingOtherChannels(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	metadata := packageMetadata{Name: providerSpecs["codex"].Package, Tags: map[string]string{"latest": "1.2.0"}, Versions: map[string]release{}, Times: map[string]json.RawMessage{}}
	for version, age := range map[string]time.Duration{"1.0.0": 100 * time.Hour, "1.1.0": 36 * time.Hour, "1.2.0": time.Hour, "2.0.0": 72 * time.Hour, "2.1.0-beta.1": 72 * time.Hour} {
		entry := release{Name: metadata.Name, Version: version}
		entry.Dist.Integrity = testIntegrity()
		metadata.Versions[version] = entry
		metadata.Times[version], _ = json.Marshal(now.Add(-age))
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return now }
	m.Client = fixtureClient(t, server)
	candidate, err := m.candidate(context.Background(), "codex")
	if err != nil || candidate.Version != "1.1.0" {
		t.Fatalf("must advance to mature stable release, not wait forever or follow another channel: %+v %v", candidate, err)
	}
}
