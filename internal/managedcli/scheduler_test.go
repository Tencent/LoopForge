package managedcli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	must(t, os.WriteFile(path, []byte(data), 0700))
}

func waitUntil(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("condition did not become ready")
		}
	}
}

func TestNextScheduledWindowBoundaries(t *testing.T) {
	for _, root := range []string{"/srv/managed-a", "/srv/managed-b"} {
		for _, date := range []time.Time{
			time.Date(2026, 1, 1, 4, 0, 0, 0, beijing),
			time.Date(2026, 12, 31, 4, 0, 0, 0, beijing),
			time.Date(2028, 2, 29, 4, 0, 0, 0, beijing),
		} {
			at := scheduledAt(root, date)
			if at.Before(date) || at.After(date.Add(10*time.Minute)) || at != scheduledAt(root, date.UTC()) {
				t.Fatal("jitter is not deterministic Beijing 04:00 + 0..600s")
			}
			for _, tc := range []struct{ now, want time.Time }{
				{at.Add(-time.Nanosecond), at},
				{at, scheduledAt(root, date.AddDate(0, 0, 1))},
				{at.Add(time.Nanosecond), scheduledAt(root, date.AddDate(0, 0, 1))},
				{date.Add(19*time.Hour + 59*time.Minute), scheduledAt(root, date.AddDate(0, 0, 1))},
				// A long check ending before the next day's window waits for that
				// day's window, even though the original check began days ago.
				{date.AddDate(0, 0, 3).Add(-4 * time.Hour), scheduledAt(root, date.AddDate(0, 0, 3))},
				{date.AddDate(0, 0, 3).Add(time.Hour), scheduledAt(root, date.AddDate(0, 0, 4))},
			} {
				got := nextScheduledAt(root, tc.now.UTC())
				if !got.Equal(tc.want) || !got.After(tc.now) {
					t.Fatalf("now=%s next=%s want=%s", tc.now, got, tc.want)
				}
			}
		}
	}
}

func TestDailyCheckWindowAndHighWaterClaim(t *testing.T) {
	f := newUpdaterFixture(t)
	m := f.manager
	at := scheduledAt(m.Root, time.Date(2026, 1, 1, 4, 0, 0, 0, beijing))
	for _, tc := range []struct {
		name string
		now  time.Time
		run  bool
	}{
		{"before", at.Add(-time.Nanosecond), false},
		{"window", at, true},
		{"restart", at.Add(12 * time.Hour), false},
		{"catchup-no-backlog", at.AddDate(0, 0, 3).Add(time.Hour), true},
		{"backwards-clock", at.AddDate(0, 0, 2), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.Now = func() time.Time { return tc.now.UTC() }
			report, err := m.Check(context.Background())
			must(t, err)
			if (report.Action != "") != tc.run || !tc.run && !reflect.DeepEqual(report, Report{}) {
				t.Fatalf("unexpected report: %+v", report)
			}
			if tc.name == "before" && !f.state(t).LastCheck.IsZero() {
				t.Fatal("check before window consumed daily claim")
			}
		})
	}
	// Manual Update/Rollback never change the daily claim. Old files are ignored.
	before := f.state(t).LastCheck
	for _, name := range []string{"schedule-state.json", "candidates.json"} {
		writeTestFile(t, filepath.Join(m.Root, name), "legacy-invalid")
	}
	f.setVersion("2.0.0")
	f.update(t, false)
	_, err := m.Rollback(context.Background(), "codex")
	must(t, err)
	if f.state(t).LastCheck != before {
		t.Fatal("manual update/rollback changed daily claim")
	}
}

func TestDailyFailureRetriesNextDayAndReusesCandidate(t *testing.T) {
	f := newUpdaterFixture(t)
	m := f.manager
	f.failHelp = "codex"
	if _, err := m.Check(context.Background()); err == nil {
		t.Fatal("missing validation failure")
	}
	f.failHelp = ""
	report, err := m.Check(context.Background())
	must(t, err)
	if report.Action != "" || f.state(t).Current["codex"].Version != "" {
		t.Fatal("daily failure retried automatically on same day")
	}
	m.Now = func() time.Time { return time.Date(2026, 1, 2, 8, 0, 0, 0, beijing) }
	report, err = m.Check(context.Background())
	must(t, err)
	if report.Action != "check" || f.state(t).Current["codex"].Version != "1.0.0" || f.installs["codex"] != 1 {
		t.Fatal("next-day retry did not reuse failed candidate")
	}
}

func TestLoopWaitsForTimerAndStopIsConcurrentSafe(t *testing.T) {
	f := newUpdaterFixture(t)
	at := scheduledAt(f.manager.Root, f.manager.Now())
	var clockCalls, factoryCalls atomic.Int64
	waiting := make(chan struct{})
	f.manager.Now = func() time.Time {
		if clockCalls.Add(1) == 2 { // Today's target, then the timer's delay.
			close(waiting)
		}
		return at.Add(-time.Hour)
	}
	stop := start(context.Background(), func() (*Manager, error) {
		factoryCalls.Add(1)
		return f.manager, nil
	})
	defer stop()
	<-waiting
	// The window is an hour away: cancellation must interrupt timer waiting,
	// without touching state or invoking network/command work.
	var stopped sync.WaitGroup
	for i := 0; i < 4; i++ {
		stopped.Add(1)
		go func() { defer stopped.Done(); stop() }()
	}
	done := make(chan struct{})
	go func() { stopped.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop did not interrupt daily timer")
	}
	stop()
	if clockCalls.Load() != 2 || factoryCalls.Load() != 1 || len(f.installs) != 0 || !f.state(t).LastCheck.IsZero() {
		t.Fatal("idle scheduler polled or consumed a daily claim")
	}
}

func TestLoopStartupCrossingWindowDoesNotSkipToday(t *testing.T) {
	f := newUpdaterFixture(t)
	at := scheduledAt(f.manager.Root, f.manager.Now())
	var calls atomic.Int64
	f.manager.Now = func() time.Time {
		if calls.Add(1) == 1 {
			return at.Add(-time.Nanosecond)
		}
		return at.Add(time.Nanosecond)
	}
	stop := start(context.Background(), func() (*Manager, error) { return f.manager, nil })
	defer stop()
	waitUntil(t, func() bool { return len(f.state(t).Current) == 3 })
	stop()
	if !f.state(t).LastCheck.Equal(at.Add(time.Nanosecond)) || len(f.installs) != 3 {
		t.Fatal("crossing startup window deferred today's check until tomorrow")
	}
}

func TestLoopTimerStartsNextCheck(t *testing.T) {
	f := newUpdaterFixture(t)
	at := scheduledAt(f.manager.Root, f.manager.Now())
	started := time.Now()
	f.manager.Now = func() time.Time { return at.Add(-50 * time.Millisecond).Add(time.Since(started)) }
	stop := start(context.Background(), func() (*Manager, error) { return f.manager, nil })
	defer stop()
	waitUntil(t, func() bool { return len(f.state(t).Current) == 3 })
	stop()
	state := f.state(t)
	if state.LastCheck.Before(at) || len(f.installs) != 3 {
		t.Fatal("timer failed to run the next daily check")
	}
}

func TestLoopStartupCheckUsesFixtureAndIgnoresLegacyConfig(t *testing.T) {
	f := newUpdaterFixture(t)
	writeTestFile(t, filepath.Join(f.manager.Root, "config.json"), `{"enabled":false}`)
	stop := start(context.Background(), func() (*Manager, error) { return f.manager, nil })
	defer stop()
	waitUntil(t, func() bool { return len(f.state(t).Current) == 3 })
	stop()
	if len(f.installs) != 3 {
		t.Fatal("startup did not check all providers")
	}
}

func TestStopJoinsCanceledCommand(t *testing.T) {
	f := newUpdaterFixture(t)
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.manager.RunCommand = func(ctx context.Context, _ string, _ []string, _ string, _ []string) ([]byte, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, ctx.Err()
	}
	stop := start(context.Background(), func() (*Manager, error) { return f.manager, nil })
	defer stop()
	<-entered
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	<-canceled
	select {
	case <-done:
		t.Fatal("stop returned before command joined")
	default:
	}
	close(release)
	<-done
	stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	must(t, WithUpdateLock(ctx, f.manager.Root, func() error { return nil }))
	if f.state(t).LastCheck.IsZero() {
		t.Fatal("cancellation lost persisted claim")
	}
	report, err := f.manager.Check(ctx)
	must(t, err)
	if report.Action != "" {
		t.Fatal("canceled command's daily claim was retried")
	}
}
