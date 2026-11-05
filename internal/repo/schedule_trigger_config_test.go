package repo

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestProjectTriggerConfigSingleton(t *testing.T) {
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			var st *Store
			var err error
			if backend == "mysql" {
				dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
				}
				st, err = Open(dsn)
			} else {
				st, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			prefix := fmt.Sprintf("single-trigger-%d", time.Now().UnixNano())
			for _, id := range []string{prefix, prefix + "-other"} {
				if err := st.PutProject(domain.Project{ID: id, Name: "Trigger test"}); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				_ = st.DB.Table("schedule_jobs").Where("id LIKE ?", prefix+"%").Delete(nil).Error
				_ = st.DB.Table("projects").Where("id IN ?", []string{prefix, prefix + "-other"}).Delete(nil).Error
			})
			job := domain.ScheduleJob{ID: prefix + "-config", ProjectID: prefix, Name: "Trigger", IntervalSec: 3600}
			if err := st.PutProjectTriggerConfig(job); err != nil {
				t.Fatal(err)
			}
			duplicate := job
			duplicate.ID += "-duplicate"
			if err := st.PutProjectTriggerConfig(duplicate); !errors.Is(err, ErrProjectTriggerConfigExists) {
				t.Fatalf("duplicate config should conflict: %v", err)
			}
			job.MaxDispatch = 3
			if err := st.PutProjectTriggerConfig(job); err != nil {
				t.Fatalf("editing the same config failed: %v", err)
			}
			stored, _ := st.ScheduleJob(job.ID)
			if stored.MaxDispatch != 3 {
				t.Fatal("config changes were not saved")
			}
			other := job
			other.ID, other.ProjectID = prefix+"-other-config", prefix+"-other"
			if err := st.PutProjectTriggerConfig(other); err != nil {
				t.Fatalf("another project's config was blocked: %v", err)
			}
			for i := range 2 {
				ai := domain.ScheduleJob{ID: fmt.Sprintf("%s-ai-%d", prefix, i), ProjectID: prefix, Name: "AI", SkipDispatch: true, AssistantPrompt: "Summarize", IntervalSec: 3600}
				if err := st.PutScheduleJob(ai); err != nil {
					t.Fatal(err)
				}
			}
			// Exercise first creation from distinct API instances/connections.
			if err := st.DeleteScheduleJob(job.ID); err != nil {
				t.Fatal(err)
			}
			peer := st
			if backend == "mysql" {
				peer, err = Open(os.Getenv("AGENTFLOW_TEST_MYSQL_DSN"))
				if err != nil {
					t.Fatal(err)
				}
				defer peer.Close()
			}
			var wg sync.WaitGroup
			results := make(chan error, 8)
			for i := range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					writer := st
					if i%2 != 0 {
						writer = peer
					}
					candidate := job
					candidate.ID = fmt.Sprintf("%s-concurrent-%d", prefix, i)
					results <- writer.PutProjectTriggerConfig(candidate)
				}()
			}
			wg.Wait()
			close(results)
			saved := 0
			for err := range results {
				if err == nil {
					saved++
				} else if !errors.Is(err, ErrProjectTriggerConfigExists) {
					t.Errorf("unexpected concurrent save error: %v", err)
				}
			}
			if saved != 1 {
				t.Fatalf("concurrent first saves created %d configs", saved)
			}
			triggers, assistants := 0, 0
			for _, stored := range st.ScheduleJobs() {
				if stored.ProjectID == prefix {
					if stored.SkipDispatch {
						assistants++
					} else {
						triggers++
					}
				}
			}
			if triggers != 1 || assistants != 2 {
				t.Fatalf("unexpected config counts: triggers=%d assistants=%d", triggers, assistants)
			}
		})
	}
}

func TestProjectTriggerConfigUpdatesExistingJobWithoutProject(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	job := domain.ScheduleJob{ID: "orphan-trigger", ProjectID: "missing-project", Name: "Existing", OwnerUserID: 42, IntervalSec: 60}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	job.IntervalSec = 300
	if err := st.PutProjectTriggerConfig(job); err != nil {
		t.Fatalf("existing job update was rejected: %v", err)
	}
	stored, _ := st.ScheduleJob(job.ID)
	if stored.OwnerUserID != 42 || stored.IntervalSec != 300 {
		t.Fatalf("existing job state was lost: %+v", stored)
	}
	for _, candidate := range []domain.ScheduleJob{
		{ID: "new-trigger", ProjectID: job.ProjectID, Name: "New"},
		{ID: job.ID, ProjectID: "another-missing-project", Name: "Moved"},
	} {
		if err := st.PutProjectTriggerConfig(candidate); err == nil {
			t.Fatalf("creation or relocation into a missing project was accepted: %+v", candidate)
		}
	}
	ai := domain.ScheduleJob{ID: "orphan-ai", ProjectID: job.ProjectID, Name: "AI", SkipDispatch: true}
	if err := st.PutScheduleJob(ai); err != nil {
		t.Fatal(err)
	}
	ai.SkipDispatch = false
	if err := st.PutProjectTriggerConfig(ai); err == nil {
		t.Fatal("AI task conversion created a trigger in a missing project")
	}
}
