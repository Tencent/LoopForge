package repo

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A queued payload larger than MySQL's default sort_buffer_size (256 KiB) used
// to fail claim and list queries with error 1038 (Out of sort memory).
func TestMySQLRunnerTasksWithLargePayloadClaimAndList(t *testing.T) {
	stores, prefix := admissionMySQLStores(t)
	st := stores[0]
	provider := prefix + "-provider"
	payload := []byte(`{"prompt":"` + strings.Repeat("x", 1<<20) + `"}`)
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	ids := []string{prefix + "-old", prefix + "-new"}
	t.Cleanup(func() { st.DB.Where("id IN ?", ids).Delete(&runnerTaskRow{}) })
	for i, id := range ids {
		created := base.Add(time.Duration(i) * time.Minute)
		row := runnerTaskRow{ID: id, Provider: provider, Status: domain.RunnerTaskQueued, Payload: payload, Created: &created, Updated: &created}
		if err := st.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	task, ok, err := st.ClaimRunnerTask(prefix+"-runner", []string{provider}, time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !ok || task.ID != ids[0] || task.Attempt != 1 || task.Status != domain.RunnerTaskLeased {
		t.Fatalf("claim = %+v ok=%v, want oldest task leased with attempt 1", task, ok)
	}

	var got []string
	for _, v := range st.RunnerTasks() {
		if strings.HasPrefix(v.ID, prefix) {
			got = append(got, v.ID)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{ids[1], ids[0]}) {
		t.Fatalf("RunnerTasks order = %v, want newest first", got)
	}
}
