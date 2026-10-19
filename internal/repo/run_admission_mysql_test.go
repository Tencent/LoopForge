package repo

import (
	"crypto/rand"
	"fmt"
	"os"
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

func TestMySQLProjectRunAdmissionConcurrentAcrossStores(t *testing.T) {
	stores, prefix := admissionMySQLStores(t)
	const total = 24
	project := prefix + "-project"
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
			errs <- stores[i%len(stores)].PutRun(admissionMySQLRun(ids[i], project))
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
	admissionMySQLState(t, stores[0], ids[total], "running")
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
	firstRun := admissionMySQLState(t, stores[0], first, "queued")
	secondRun := admissionMySQLState(t, stores[1], second, "queued")
	if !firstRun.QueueEnteredAt.Before(secondRun.QueueEnteredAt) {
		t.Fatal("test requires distinct persisted queue timestamps")
	}
	if promoted := admissionMySQLPromoteConcurrently(t, stores, ids); len(promoted) != 0 {
		t.Fatalf("started failed/waiting/cancelled runs released slots: %v", promoted)
	}
	run := admissionMySQLState(t, stores[1], ids[0], "failed")
	run.Status = "succeeded"
	if err := stores[1].PutRun(run); err != nil {
		t.Fatal(err)
	}
	if run = admissionMySQLState(t, stores[0], run.ID, "succeeded"); run.AdmissionState != "released" {
		t.Fatalf("successful run did not release its slot: %q", run.AdmissionState)
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
}

// A held bucket lock must block only its own project: another project is
// admitted at once, while a same-project admission waits for the lock.
func TestMySQLAdmissionBucketsDoNotBlockEachOther(t *testing.T) {
	stores, prefix := admissionMySQLStores(t)
	busy, free := prefix+"-busy", prefix+"-free"
	blocked, other := prefix+"-blocked", prefix+"-other"
	admissionMySQLCleanup(t, stores[0], []string{blocked, other})
	t.Cleanup(func() {
		stores[0].DB.Where("id IN ?", []string{"project:" + busy, "project:" + free}).Delete(&runAdmissionLockRow{})
	})

	locked := make(chan struct{})
	release := make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- stores[0].DB.Transaction(func(tx *gorm.DB) error {
			if err := lockAdmissionGate(stores[0].deps, tx, "project:"+busy); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	sameProject := make(chan error, 1)
	go func() { sameProject <- stores[1].PutRun(admissionMySQLRun(blocked, busy)) }()

	start := time.Now()
	if err := stores[1].PutRun(admissionMySQLRun(other, free)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("other project waited %s on an unrelated bucket lock", elapsed)
	}
	admissionMySQLState(t, stores[0], other, "running")

	select {
	case err := <-sameProject:
		t.Fatalf("same-project admission did not wait for its bucket lock (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-holder; err != nil {
		t.Fatal(err)
	}
	if err := <-sameProject; err != nil {
		t.Fatal(err)
	}
	admissionMySQLState(t, stores[0], blocked, "running")
}
