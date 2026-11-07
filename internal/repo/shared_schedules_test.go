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

func TestSharedScheduleImmutableVersions(t *testing.T) {
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			var st, peer *Store
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
			prefix := fmt.Sprintf("shared-test-%d", time.Now().UnixNano())
			job := domain.ScheduleJob{ID: prefix, ProjectID: prefix, Name: "Source", SkipDispatch: true, AssistantPrompt: "Source prompt"}
			if err = st.PutScheduleJob(job); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.DB.Where("id = ?", prefix).Delete(&scheduleJobRow{}).Error })
			v := domain.SharedScheduleVersion{Name: "Shared", Prompt: "Original", SourceProjectName: "Payments"}
			first, err := st.PublishSharedSchedule(job.ID, job.ProjectID, v, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.DB.Where("shared_task_id = ?", first.ID).Delete(&sharedScheduleVersionRow{}).Error })
			if first.Version != 1 || first.SourceJobID != job.ID || first.CreatedAt.IsZero() {
				t.Fatalf("invalid publication %+v", first)
			}
			peer = st
			if backend == "mysql" {
				peer, err = Open(os.Getenv("AGENTFLOW_TEST_MYSQL_DSN"))
				if err != nil {
					t.Fatal(err)
				}
				defer peer.Close()
			}
			// Distinct instances publishing from the same base must yield one new version.
			var wg sync.WaitGroup
			results := make(chan error, 6)
			for i := range 6 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					writer := st
					if i%2 != 0 {
						writer = peer
					}
					_, err := writer.PublishSharedSchedule(job.ID, job.ProjectID, v, 1)
					results <- err
				}()
			}
			wg.Wait()
			close(results)
			count := 0
			for err := range results {
				if err == nil {
					count++
				} else if !errors.Is(err, ErrSharedScheduleVersionConflict) {
					t.Errorf("unexpected publication error: %v", err)
				}
			}
			if count != 1 {
				t.Fatalf("concurrent publications=%d", count)
			}
			v.Prompt = "Latest"
			for base := 2; base < 23; base++ {
				if _, err = st.PublishSharedSchedule(job.ID, job.ProjectID, v, base); err != nil {
					t.Fatal(err)
				}
			}
			versions, err := st.SharedScheduleVersions(first.ID)
			if err != nil || len(versions) != 23 {
				t.Fatalf("history was pruned: %d %v", len(versions), err)
			}
			original, err := st.SharedScheduleVersion(first.ID, 1)
			if err != nil || original.Prompt != "Original" {
				t.Fatalf("old publication mutated %+v %v", original, err)
			}
			latest, err := st.SharedScheduleTasks()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range latest {
				if item.ID == first.ID {
					found = true
					if item.Version != 23 || item.Prompt != "Latest" {
						t.Fatalf("latest=%+v", item)
					}
				}
			}
			if !found {
				t.Fatal("publication missing from catalog")
			}
			job.SharedTaskID = first.ID
			if err = st.PutScheduleJob(job); err != nil {
				t.Fatal(err)
			}
			if _, err = st.PublishSharedSchedule(job.ID, job.ProjectID, v, 23); err == nil {
				t.Fatal("subscription was republished")
			}
			if err = st.DeleteScheduleJob(job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = st.SharedScheduleVersion(first.ID, 1); err != nil {
				t.Fatalf("source deletion removed publication: %v", err)
			}
		})
	}
}
