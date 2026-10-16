package repo

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func admissionRun(id, project string) domain.Run {
	return domain.Run{ID: id, Status: "running", CreatedAt: time.Now().UTC(), ExecutionContext: domain.TelemetryContext{ProjectID: project}, Nodes: map[string]*domain.NodeRun{}}
}

func TestProjectRunAdmissionConcurrentAndIsolated(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- s.PutRun(admissionRun(fmt.Sprintf("parallel-%02d", i), "p"))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	running, queued := 0, 0
	for _, r := range s.Runs() {
		if r.Status == "running" {
			running++
		}
		if r.Status == "queued" {
			queued++
		}
	}
	if running != 10 || queued != 14 {
		t.Fatalf("running=%d queued=%d", running, queued)
	}
	if err := s.PutRun(admissionRun("other", "other")); err != nil {
		t.Fatal(err)
	}
	other, _ := s.RunState("other")
	if other.Status != "running" {
		t.Fatal(other.Status)
	}
}

func TestProjectRunAdmissionRetainsAllStartedOutcomes(t *testing.T) {
	s, _ := OpenTest()
	defer s.Close()
	for i := 0; i < 10; i++ {
		r := admissionRun(fmt.Sprintf("held-%d", i), "p")
		if err := s.PutRun(r); err != nil {
			t.Fatal(err)
		}
		r, _ = s.RunState(r.ID)
		r.Status = []string{"failed", "waiting", "cancelled"}[i%3]
		if err := s.PutRun(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutRun(admissionRun("first-queued", "p")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(admissionRun("second-queued", "p")); err != nil {
		t.Fatal(err)
	}
	promoted, err := s.PromoteQueuedRuns()
	if err != nil || len(promoted) != 0 {
		t.Fatalf("promoted=%v err=%v", promoted, err)
	}
	r, _ := s.RunState("held-0")
	r.Status = "succeeded"
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(admissionRun("no-insert-ahead", "p")); err != nil {
		t.Fatal(err)
	}
	promoted, err = s.PromoteQueuedRuns()
	if err != nil || len(promoted) != 1 || promoted[0] != "first-queued" {
		t.Fatalf("promoted=%v err=%v", promoted, err)
	}
	cancelled, err := s.CancelQueuedRun("second-queued")
	if err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	r, _ = s.RunState("second-queued")
	if r.AdmissionState != "unstarted" {
		t.Fatal(r.AdmissionState)
	}
	r.Status = "running"
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	r, _ = s.RunState(r.ID)
	if r.Status != "queued" {
		t.Fatal("cancelled unstarted replay bypassed quota")
	}
}

func TestProjectRunAdmissionPersonalRunnerChildAndEmptyProject(t *testing.T) {
	s, _ := OpenTest()
	defer s.Close()
	if err := s.PutRunner(domain.Runner{ID: "personal", OwnerUserID: 7}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		r := admissionRun(fmt.Sprintf("personal-%d", i), "")
		r.ExecutionContext.ExecutionRunnerID = "personal"
		if err := s.PutRun(r); err != nil {
			t.Fatal(err)
		}
		got, _ := s.RunState(r.ID)
		if got.Status != "running" || !got.ConcurrencyExempt {
			t.Fatal(got)
		}
	}
	for i := 0; i < 10; i++ {
		if err := s.PutRun(admissionRun(fmt.Sprintf("empty-%d", i), "")); err != nil {
			t.Fatal(err)
		}
	}
	child := admissionRun("child", "")
	child.ExecutionContext.ParentRunID = "empty-0"
	if err := s.PutRun(child); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RunState(child.ID)
	if got.Status != "running" {
		t.Fatal("child queued")
	}
	if err := s.PutRun(admissionRun("empty-eleventh", "")); err != nil {
		t.Fatal(err)
	}
	got, _ = s.RunState("empty-eleventh")
	if got.Status != "queued" {
		t.Fatal("empty project limit bypassed")
	}
}

func TestProjectRunAdmissionLegacyOverLimitAndSuccessfulReplay(t *testing.T) {
	s, _ := OpenTest()
	defer s.Close()
	for i := 0; i < 12; i++ {
		r := admissionRun(fmt.Sprintf("legacy-%d", i), "p")
		r.Status = "failed"
		if err := s.DB.Create(&runRow{ID: r.ID, Status: r.Status, ExecutionContext: jsonBytes(r.ExecutionContext)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutRun(admissionRun("new", "p")); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RunState("new")
	if got.Status != "queued" {
		t.Fatal("legacy occupied rows ignored")
	}
	legacy, _ := s.RunState("legacy-0")
	legacy.Status = "running"
	if err := s.PutRun(legacy); err != nil {
		t.Fatal(err)
	}
	legacy, _ = s.RunState(legacy.ID)
	if legacy.Status != "running" {
		t.Fatal("grandfathered run was interrupted")
	}
	good := admissionRun("successful", "p")
	good.Status = "succeeded"
	if err := s.PutRun(good); err != nil {
		t.Fatal(err)
	}
	good, _ = s.RunState(good.ID)
	good.Status = "running"
	if err := s.PutRun(good); err != nil {
		t.Fatal(err)
	}
	good, _ = s.RunState(good.ID)
	if good.Status != "queued" {
		t.Fatal("successful replay bypassed quota")
	}
}

func personalRun(id, project, runner string) domain.Run {
	r := admissionRun(id, project)
	r.ExecutionContext.ExecutionRunnerID = runner
	return r
}

func TestPersonalRunnerRunLimit(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"laptop", "desktop"} {
		if err := s.PutRunner(domain.Runner{ID: id, OwnerUserID: 7}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetRunnerRunLimit("laptop", 2); err != nil {
		t.Fatal(err)
	}
	// Re-registration and heartbeats rewrite the runner row; the user's limit must survive.
	if err := s.PutRunner(domain.Runner{ID: "laptop", OwnerUserID: 7, MaxConcurrency: 4, Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Runner("laptop"); got.RunLimit != 2 {
		t.Fatalf("run limit reset by PutRunner: %+v", got)
	}
	for _, err := range []error{s.SetRunnerRunLimit("laptop", -1), s.SetRunnerRunLimit("laptop", MaxRunnerRunLimit+1)} {
		if err == nil {
			t.Fatal("out-of-range run limit accepted")
		}
	}
	if err := s.SetRunnerRunLimit("missing", 3); err != ErrRunnerNotFound {
		t.Fatalf("missing runner: %v", err)
	}

	// Fill project p on cloud: personal machine runs must not be blocked by it.
	for i := 0; i < ProjectRunLimit; i++ {
		if err := s.PutRun(admissionRun(fmt.Sprintf("cloud-%d", i), "p")); err != nil {
			t.Fatal(err)
		}
	}
	status := func(id string) string { r, _ := s.RunState(id); return r.Status }
	for _, id := range []string{"lap-1", "lap-2", "lap-3", "lap-4"} {
		if err := s.PutRun(personalRun(id, "p", "laptop")); err != nil {
			t.Fatal(err)
		}
	}
	if status("lap-1") != "running" || status("lap-2") != "running" || status("lap-3") != "queued" || status("lap-4") != "queued" {
		t.Fatalf("laptop limit 2: %s %s %s %s", status("lap-1"), status("lap-2"), status("lap-3"), status("lap-4"))
	}
	if queuedMessage("runner:laptop") != "等待执行机器可用运行名额" || queuedMessage("project:p") != "等待项目可用运行名额" {
		t.Fatal("queued event message does not name the limiting bucket")
	}
	// An unlimited personal machine is unaffected by the full project and the laptop queue.
	if err := s.PutRun(personalRun("desk-1", "p", "desktop")); err != nil {
		t.Fatal(err)
	}
	if status("desk-1") != "running" {
		t.Fatal("unlimited personal machine queued")
	}
	// Personal runs never occupy the project bucket.
	if err := s.PutRun(admissionRun("cloud-extra", "p")); err != nil {
		t.Fatal(err)
	}
	if status("cloud-extra") != "queued" {
		t.Fatal("project limit not enforced")
	}
	cloud, _ := s.RunState("cloud-0")
	cloud.Status = "succeeded"
	if err := s.PutRun(cloud); err != nil {
		t.Fatal(err)
	}
	if promoted, err := s.PromoteQueuedRuns(); err != nil || len(promoted) != 1 || promoted[0] != "cloud-extra" {
		t.Fatalf("project release must promote only the cloud queue: %v %v", promoted, err)
	}
	// Failed personal runs keep their slot; success releases it in FIFO order.
	lap, _ := s.RunState("lap-1")
	lap.Status = "failed"
	if err := s.PutRun(lap); err != nil {
		t.Fatal(err)
	}
	if promoted, _ := s.PromoteQueuedRuns(); len(promoted) != 0 {
		t.Fatalf("failed run released a machine slot: %v", promoted)
	}
	lap, _ = s.RunState("lap-2")
	lap.Status = "succeeded"
	if err := s.PutRun(lap); err != nil {
		t.Fatal(err)
	}
	if promoted, _ := s.PromoteQueuedRuns(); len(promoted) != 1 || promoted[0] != "lap-3" {
		t.Fatalf("machine FIFO: %v", promoted)
	}
	// Clearing the limit releases the remaining queue on the next pass.
	if err := s.SetRunnerRunLimit("laptop", 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Runner("laptop"); got.RunLimit != 0 {
		t.Fatalf("limit not cleared: %+v", got)
	}
	if promoted, _ := s.PromoteQueuedRuns(); len(promoted) != 1 || promoted[0] != "lap-4" {
		t.Fatalf("cleared limit did not release queue: %v", promoted)
	}
}

func TestAdmissionLocksOnlyWhenClaimingAndPersistsBucket(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lockRows := func() []string {
		var ids []string
		if err := s.DB.Model(&runAdmissionLockRow{}).Order("id").Pluck("id", &ids).Error; err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if err := s.PutRunner(domain.Runner{ID: "free", OwnerUserID: 7}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(admissionRun("p1", "alpha")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(personalRun("mine", "alpha", "free")); err != nil {
		t.Fatal(err)
	}
	if got := lockRows(); fmt.Sprint(got) != "[project:alpha]" {
		t.Fatalf("lock rows = %v; only the limited project bucket should be locked", got)
	}
	// Progress/terminal writes of an admitted run never touch a bucket lock.
	if err := s.DB.Where("1 = 1").Delete(&runAdmissionLockRow{}).Error; err != nil {
		t.Fatal(err)
	}
	r, _ := s.RunState("p1")
	for _, status := range []string{"waiting", "failed", "succeeded"} {
		r.Status = status
		if err := s.PutRun(r); err != nil {
			t.Fatal(err)
		}
		r, _ = s.RunState("p1")
	}
	if got := lockRows(); len(got) != 0 {
		t.Fatalf("slot-keeping writes took locks: %v", got)
	}
	var buckets []runRow
	if err := s.DB.Select("id", "quota_bucket").Order("id").Find(&buckets).Error; err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"p1": "project:alpha", "mine": "runner:free"}
	for _, row := range buckets {
		if row.QuotaBucket == nil || *row.QuotaBucket != want[row.ID] {
			t.Fatalf("run %s bucket = %v, want %s", row.ID, row.QuotaBucket, want[row.ID])
		}
	}
}

func TestAdmissionCountsLegacyRowsWithoutBucket(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutIssue(domain.Issue{ID: "legacy-issue", ProjectID: "beta", Title: "t", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	// Rows written by the previous binary: no quota columns at all. Half carry
	// the project only through the linked issue.
	for i := 0; i < ProjectRunLimit; i++ {
		ctx := domain.TelemetryContext{ProjectID: "beta"}
		if i%2 == 1 {
			ctx = domain.TelemetryContext{IssueID: "legacy-issue"}
		}
		if err := s.DB.Create(&runRow{ID: fmt.Sprintf("old-%d", i), Status: "waiting", ExecutionContext: jsonBytes(ctx)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutRun(admissionRun("new-beta", "beta")); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.RunState("new-beta"); r.Status != "queued" {
		t.Fatalf("legacy rows not counted: %s", r.Status)
	}
	if err := s.PutRun(admissionRun("new-gamma", "gamma")); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.RunState("new-gamma"); r.Status != "running" {
		t.Fatalf("legacy rows leaked into another project: %s", r.Status)
	}
}
