package repo

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestDisableScheduleJobKeepsEditsSavedMeanwhile(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	job := domain.ScheduleJob{ID: "disable-race", Name: "巡检", ProjectID: "platform", SkipDispatch: true, AssistantPrompt: "p", IntervalSec: 3600, Enabled: true, RunAsUserID: 1}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	// An edit lands after the stop request read the job.
	edited := job
	edited.Name, edited.AssistantPrompt, edited.RunAsUserID = "巡检 v2", "p2", 2
	if err := st.PutScheduleJob(edited); err != nil {
		t.Fatal(err)
	}
	var checked domain.ScheduleJob
	if _, err := st.DisableScheduleJob(job.ID, func(current domain.ScheduleJob) bool { checked = current; return false }); !errors.Is(err, ErrScheduleJobChanged) {
		t.Fatalf("rejected disable err = %v", err)
	}
	if checked.AssistantPrompt != "p2" {
		t.Fatalf("check saw %+v, want the row as stored now", checked)
	}
	if got, _ := st.ScheduleJob(job.ID); !got.Enabled {
		t.Fatal("rejected disable still turned the job off")
	}
	returned, err := st.DisableScheduleJob(job.ID, func(domain.ScheduleJob) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	got, _ := st.ScheduleJob(job.ID)
	if got.Enabled || got.Name != "巡检 v2" || got.AssistantPrompt != "p2" || got.RunAsUserID != 2 || returned.Enabled || returned.Name != "巡检 v2" {
		t.Fatalf("disable = %+v returned %+v", got, returned)
	}
	for _, listed := range st.ScheduleJobs() {
		if listed.ID == job.ID && listed.Enabled {
			t.Fatal("listed job still enabled")
		}
	}
	if _, err := st.DisableScheduleJob("missing", func(domain.ScheduleJob) bool { return true }); !errors.Is(err, ErrScheduleJobChanged) {
		t.Fatalf("disabling a missing job err = %v", err)
	}
}

func TestScheduleJobDispatchLimitPersistence(t *testing.T) {
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
			job := domain.ScheduleJob{ID: fmt.Sprintf("limit-%d", time.Now().UnixNano()), Name: "触发上限", ProjectID: "platform", WorkflowID: "wf"}
			t.Cleanup(func() { _ = st.DeleteScheduleJob(job.ID) })
			for _, limit := range []int{0, 1, 3, 10} {
				job.MaxDispatch = limit
				if err := st.PutScheduleJob(job); err != nil {
					t.Fatal(err)
				}
				got, ok := st.ScheduleJob(job.ID)
				if !ok || got.MaxDispatch != limit {
					t.Fatalf("single job maxDispatch=%d want=%d found=%v", got.MaxDispatch, limit, ok)
				}
				found := false
				for _, listed := range st.ScheduleJobs() {
					if listed.ID == job.ID {
						found = true
						if listed.MaxDispatch != limit {
							t.Fatalf("listed maxDispatch=%d want=%d", listed.MaxDispatch, limit)
						}
					}
				}
				if !found {
					t.Fatal("job missing from list")
				}
			}
			job.LastRunAt = time.Now().UTC()
			job.LastStatus = "running"
			if err := st.PutScheduleJob(job); err != nil {
				t.Fatal(err)
			}
			edited := job
			edited.MaxDispatch = 2
			if err := st.PutScheduleJob(edited); err != nil {
				t.Fatal(err)
			}
			job.LastStatus = "success"
			job.LastDispatched = 10
			if err := st.SaveScheduleJobResult(job); err != nil {
				t.Fatal(err)
			}
			got, _ := st.ScheduleJob(job.ID)
			if got.MaxDispatch != 2 || got.LastDispatched != 10 || got.LastStatus != "success" {
				t.Fatalf("saving result overwrote configuration edited during execution: %+v", got)
			}
		})
	}
}
