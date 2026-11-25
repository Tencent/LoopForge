package repo

import (
	"crypto/rand"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

// AGENTFLOW_TEST_MYSQL_DSN must identify a disposable test database. These
// tests never use AGENTFLOW_MYSQL_DSN or delete the shared admission gate.
func admissionMySQLStores(t *testing.T) ([2]*Store, string) {
	t.Helper()
	var stores [2]*Store
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("mysql-admit-%x", suffix)
	for i := range stores {
		store, err := Open(dsn)
		if err != nil {
			// Connection errors can contain credential material from the DSN.
			t.Fatalf("Open MySQL store %d failed (%T); DSN omitted", i, err)
		}
		stores[i] = store
		t.Cleanup(store.Close)
		if store.DB.Dialector.Name() != "mysql" {
			t.Fatal("admission acceptance requires the real MySQL dialect")
		}
	}
	return stores, prefix
}

func admissionMySQLCleanup(t *testing.T, store *Store, ids []string) {
	t.Helper()
	t.Cleanup(func() {
		for _, table := range []string{"run_artifacts", "run_logs", "run_telemetry", "run_events", "run_nodes"} {
			if err := store.DB.Table(table).Where("run_id IN ?", ids).Delete(map[string]any{}).Error; err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
		if err := store.DB.Where("id IN ?", ids).Delete(&runRow{}).Error; err != nil {
			t.Errorf("cleanup runs: %v", err)
		}
	})
}

func admissionMySQLRun(id, project string) domain.Run {
	return domain.Run{
		ID: id, Status: "running", CreatedAt: time.Now().UTC(),
		ExecutionContext: domain.TelemetryContext{ProjectID: project},
		Nodes:            map[string]*domain.NodeRun{},
	}
}

func admissionMySQLState(t *testing.T, store *Store, id, status string) domain.Run {
	t.Helper()
	run, ok := store.RunState(id)
	if !ok || run.Status != status {
		t.Fatalf("run %s: found=%v status=%q, want %q", id, ok, run.Status, status)
	}
	return run
}

func admissionMySQLNoForeignQueue(t *testing.T, store *Store, ids []string) {
	t.Helper()
	var count int64
	if err := store.DB.Model(&runRow{}).Where("status = ? AND id NOT IN ?", "queued", ids).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("disposable database contains unrelated queued runs; refusing global promotion")
	}
}

func admissionMySQLPromoteConcurrently(t *testing.T, stores [2]*Store, ids []string) []string {
	t.Helper()
	admissionMySQLNoForeignQueue(t, stores[0], ids)
	type result struct {
		ids []string
		err error
	}
	start := make(chan struct{})
	results := make(chan result, len(stores))
	var wg sync.WaitGroup
	for _, store := range stores {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			<-start
			promoted, err := store.PromoteQueuedRuns()
			results <- result{ids: promoted, err: err}
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)
	var promoted []string
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		promoted = append(promoted, result.ids...)
	}
	return promoted
}

func TestMySQLPlatformRunAdmissionConcurrentAcrossStores(t *testing.T) {
	stores, prefix := admissionMySQLStores(t)
	const total = 24
	ids := make([]string, total+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-run-%02d", prefix, i)
	}
	admissionMySQLCleanup(t, stores[0], ids)
	start := make(chan struct{})
	errs := make(chan error, total)
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- stores[i%len(stores)].PutRun(admissionMySQLRun(ids[i], fmt.Sprintf("%s-project-%d", prefix, i%3)))
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, store := range stores {
		var rows []runRow
		if err := store.DB.Where("id IN ?", ids[:total]).Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		running, queued := 0, 0
		for _, row := range rows {
			switch row.Status {
			case "running":
				running++
			case "queued":
				queued++
				if row.QueueEntered == nil || row.QueueEntered.IsZero() || row.AdmissionState != "queued" {
					t.Fatalf("queued run %s missing persisted admission metadata", row.ID)
				}
			default:
				t.Fatalf("unexpected status %q for run %s", row.Status, row.ID)
			}
		}
		if len(rows) != total || running != 10 || queued != total-10 {
			t.Fatalf("store %d: rows=%d running=%d queued=%d, want %d/10/%d", i, len(rows), running, queued, total, total-10)
		}
	}
	if err := stores[1].PutRun(admissionMySQLRun(ids[total], prefix+"-other-project")); err != nil {
		t.Fatal(err)
	}
	admissionMySQLState(t, stores[0], ids[total], "queued")
}

func TestMySQLProjectRunAdmissionStartedOutcomesAndFIFO(t *testing.T) {
	stores, prefix := admissionMySQLStores(t)
	project := prefix + "-project"
	ids := make([]string, 10, 13)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-held-%02d", prefix, i)
	}
	first, second, newcomer := prefix+"-q-z", prefix+"-q-a", prefix+"-q-0"
	ids = append(ids, first, second, newcomer)
	admissionMySQLCleanup(t, stores[0], ids)
	admissionMySQLNoForeignQueue(t, stores[0], ids)
	for i, id := range ids[:10] {
		if err := stores[i%2].PutRun(admissionMySQLRun(id, project)); err != nil {
			t.Fatal(err)
		}
		run := admissionMySQLState(t, stores[(i+1)%2], id, "running")
		run.Status = []string{"failed", "waiting", "cancelled"}[i%3]
		if err := stores[(i+1)%2].PutRun(run); err != nil {
			t.Fatal(err)
		}
		run = admissionMySQLState(t, stores[i%2], id, run.Status)
		if run.AdmissionState != "held" {
			t.Fatalf("started %s run did not retain its slot: %q", run.Status, run.AdmissionState)
		}
	}
	for _, id := range []string{first, second} {
		if err := stores[1].PutRun(admissionMySQLRun(id, project)); err != nil {
			t.Fatal(err)
		}
		admissionMySQLState(t, stores[0], id, "queued")
		// MySQL stores millisecond precision; queue order must beat reversed IDs.
		time.Sleep(10 * time.Millisecond)
	}
	if err := stores[0].DB.Model(&runRow{}).Where("id IN ?", []string{ids[0], first}).
		Update("quota_bucket", "project:"+project).Error; err != nil {
		t.Fatal(err)
	}
	firstRun := admissionMySQLState(t, stores[0], first, "queued")
	secondRun := admissionMySQLState(t, stores[1], second, "queued")
	if !firstRun.QueueEnteredAt.Before(secondRun.QueueEnteredAt) {
		t.Fatal("test requires distinct persisted queue timestamps")
	}
	if promoted := admissionMySQLPromoteConcurrently(t, stores, ids); len(promoted) != 0 {
		t.Fatalf("started failed/waiting/cancelled runs released slots: %v", promoted)
	}
	run := admissionMySQLState(t, stores[1], ids[0], "failed")
	run.WorkspaceCleanedAt = time.Now().UTC()
	if err := stores[1].PutRun(run); err != nil {
		t.Fatal(err)
	}
	if released, err := stores[0].ReleaseCleanedRunAdmission(run.ID, "archived"); err != nil || !released {
		t.Fatalf("release failed SOP after cleanup: %v, %v", released, err)
	}
	if run = admissionMySQLState(t, stores[1], run.ID, "failed"); run.AdmissionState != "released" || len(run.Events) != 1 || run.Events[0].Type != "workspace.reclaimed" {
		t.Fatalf("cleaned run release was not visible across stores: admission=%s, events=%+v", run.AdmissionState, run.Events)
	}
	if err := stores[0].PutRun(admissionMySQLRun(newcomer, project)); err != nil {
		t.Fatal(err)
	}
	admissionMySQLState(t, stores[1], newcomer, "queued")
	for i, expected := range []string{first, second, newcomer} {
		promoted := admissionMySQLPromoteConcurrently(t, stores, ids)
		if len(promoted) != 1 || promoted[0] != expected {
			t.Fatalf("FIFO step %d: promoted=%v, want only %s", i, promoted, expected)
		}
		run = admissionMySQLState(t, stores[1], expected, "running")
		if run.AdmissionState != "held" {
			t.Fatalf("promoted run %s did not acquire a slot", expected)
		}
		if i < 2 {
			run.Status = "succeeded"
			if err := stores[0].PutRun(run); err != nil {
				t.Fatal(err)
			}
		}
	}
	run = admissionMySQLState(t, stores[1], ids[0], "failed")
	run.Status = "running"
	if err := stores[1].PutRun(run); err != nil {
		t.Fatal(err)
	}
	run = admissionMySQLState(t, stores[0], run.ID, "queued")
	if run.AdmissionState != "queued" || len(run.Events) != 2 || run.Events[1].Type != "run.queued" {
		t.Fatalf("readmission did not queue with an event: admission=%s, events=%+v", run.AdmissionState, run.Events)
	}
}

// Holding the platform gate blocks admission across projects while a
// personal runner can still admit work immediately.
func TestMySQLAdmissionBucketsDoNotBlockEachOther(t *testing.T) {
	stores, prefix := admissionMySQLStores(t)
	runner := prefix + "-runner"
	if err := stores[0].PutRunner(domain.Runner{ID: runner, OwnerUserID: 1}); err != nil {
		t.Fatal(err)
	}
	blocked, other := prefix+"-blocked", prefix+"-other"
	admissionMySQLCleanup(t, stores[0], []string{blocked, other})
	t.Cleanup(func() {
		_ = stores[0].DeregisterRunner(runner, time.Now())
		stores[0].DB.Where("id = ?", runnerAdmissionBucketKey(runner)).Delete(&runAdmissionLockRow{})
	})

	locked := make(chan struct{})
	release := make(chan struct{})
	holder := make(chan error, 1)
	releaseGate := sync.OnceFunc(func() { close(release) })
	defer releaseGate()
	go func() {
		holder <- stores[0].DB.Transaction(func(tx *gorm.DB) error {
			if err := lockAdmissionGate(stores[0].deps, tx, PlatformAdmissionBucketKey); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	platformClaim := make(chan error, 1)
	go func() { platformClaim <- stores[1].PutRun(admissionMySQLRun(blocked, prefix+"-project")) }()

	start := time.Now()
	if err := stores[1].PutRun(personalRun(other, prefix+"-project", runner)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("personal runner waited %s on an unrelated bucket lock", elapsed)
	}
	admissionMySQLState(t, stores[0], other, "running")

	select {
	case err := <-platformClaim:
		t.Fatalf("platform admission did not wait for its bucket lock (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	releaseGate()
	if err := <-holder; err != nil {
		t.Fatal(err)
	}
	if err := <-platformClaim; err != nil {
		t.Fatal(err)
	}
	admissionMySQLState(t, stores[0], blocked, "running")
}

func TestMySQLConversationWorkspacesDoNotHoldSOPSlots(t *testing.T) {
	stores, prefix := admissionMySQLStores(t)
	ids := checkConversationWorkspaces(t, stores[0], prefix+"-project", prefix+"-")
	admissionMySQLCleanup(t, stores[0], ids)
}

func TestMySQLAutoAssignAdmissionConcurrentAcrossTargets(t *testing.T) {
	stores, prefix := admissionMySQLStores(t)
	s := stores[0]
	const limit, total, owner = 3, 32, 7
	projects := []string{prefix + "-one", prefix + "-two"}
	runner := domain.Runner{ID: prefix + "-shared", OwnerUserID: owner, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionDefaultSelection}
	shared := runnerAdmissionBucketKey(runner.ID)
	t.Cleanup(func() {
		_ = s.DeregisterRunner(runner.ID, time.Now())
		_ = s.DB.Where("id = ?", shared).Delete(&runAdmissionLockRow{}).Error
		_ = s.DB.Where("id = ?", platformRunnerSettingsID).Delete(&platformRunnerSettingsRow{}).Error
		_ = s.DB.Where("id IN ?", projects).Delete(&projectRow{}).Error
	})
	for _, id := range projects {
		if err := s.PutProject(domain.Project{ID: id, Name: id, OwnerID: owner}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunnerSharedProjects(runner.ID, owner, projects); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunnerRunLimit(runner.ID, limit); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlatformRunnerRunLimit(limit, owner); err != nil {
		t.Fatal(err)
	}
	if got := stores[1].PlatformRunnerRunLimit(); got != limit {
		t.Fatalf("limit across stores = %d", got)
	}
	ids := make([]string, total)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%02d", prefix, i)
	}
	admissionMySQLCleanup(t, s, ids)
	concurrently := func(n int, task func(int) error) {
		t.Helper()
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make(chan error, n)
		for i := range n {
			wg.Go(func() {
				<-start
				errs <- task(i)
			})
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	concurrently(total, func(i int) error {
		store := stores[i%len(stores)]
		run := autoAdmissionRun(ids[i], projects[i%len(projects)], owner)
		keys := [2]string{PlatformAdmissionBucketKey, shared}
		for _, key := range []string{keys[i%2], keys[(i+1)%2]} {
			var target *domain.Runner
			if key == shared {
				target = &runner
			}
			plan, _ := autoAdmissionPlan(run, key, target)
			bound := run
			bound.ExecutionContext = plan.ExecutionContext
			if admitted, err := store.TryAdmitRunOnCandidate(bound, key); admitted || err != nil {
				return err
			}
		}
		return store.EnqueueProjectAutoRun(run)
	})
	assertLoad := func(platformLimit, sharedLimit int) {
		t.Helper()
		loads, err := stores[1].LoadSnapshots([]string{runner.ID})
		if err != nil || loads[PlatformAdmissionBucketKey].OccupiedRuns != platformLimit || loads[shared].OccupiedRuns != sharedLimit {
			t.Fatalf("loads = %+v, %v, want %d and %d", loads, err, platformLimit, sharedLimit)
		}
	}
	assertLoad(limit, limit)
	admissionMySQLNoForeignQueue(t, s, ids)
	var running []string
	if err := s.DB.Model(&runRow{}).Where("id IN ? AND status = ?", ids, "running").Pluck("id", &running).Error; err != nil {
		t.Fatal(err)
	}
	if len(running) != 2*limit {
		t.Fatalf("admitted %d runs", len(running))
	}
	for _, id := range running {
		run := admissionMySQLState(t, s, id, "running")
		run.Status = "succeeded"
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	// Give both machines the same pre-evaluated queue so the conditional
	// update must resolve cross-target races after each gate is taken.
	eval := &AutoAssignEvaluator{EvaluateAutoRun: autoAdmissionPlan}
	queueKeys := []string{projectAutoBucketPrefix + projects[0], projectAutoBucketPrefix + projects[1]}
	targetKeys := []string{PlatformAdmissionBucketKey, shared}
	poolPlans, err := s.planAutoRuns(queueKeys, targetKeys[0], nil, eval)
	if err != nil {
		t.Fatal(err)
	}
	runnerPlans, err := s.planAutoRuns(queueKeys, targetKeys[1], &runner, eval)
	if err != nil {
		t.Fatal(err)
	}
	plans := []queuedAutoPlans{poolPlans, runnerPlans}
	promoted := make([][]string, 2)
	concurrently(2, func(i int) error {
		var err error
		promoted[i], err = stores[i].promoteBucket(targetKeys[i], plans[i])
		return err
	})
	all := slices.Concat(promoted...)
	slices.Sort(all)
	if len(all) != 2*limit || len(slices.Compact(all)) != 2*limit {
		t.Fatalf("duplicate or missing promotions: %v", promoted)
	}
	assertLoad(limit, limit)
	for _, id := range all {
		run := admissionMySQLState(t, s, id, "running")
		if len(run.Events) != 3 || run.Events[1].Type != "execution.bound" || run.Events[2].Type != "run.started" {
			t.Fatalf("run %s events: %+v", id, run.Events)
		}
	}
	if err := s.SetPlatformRunnerRunLimit(0, owner); err != nil {
		t.Fatal(err)
	}
	promotedRest, err := stores[1].PromoteQueuedRunsWithAuto(eval)
	if err != nil || len(promotedRest) != total-4*limit {
		t.Fatalf("unlimited platform remaining promotions = %v, %v", promotedRest, err)
	}
	assertLoad(total-3*limit, limit)
}
