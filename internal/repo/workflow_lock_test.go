package repo

import (
	"testing"
	"time"
)

// A lease belongs to one editor until it expires; takeover is explicit and
// renewal keeps the same owner.
func TestWorkflowLockLifecycle(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	holder, owned, err := s.AcquireWorkflowLock("wf-1", 1, "alice", time.Minute, false)
	if err != nil || !owned || holder.UserLogin != "alice" {
		t.Fatalf("acquire = %+v %v %v", holder, owned, err)
	}
	// Another user cannot take it without takeover.
	other, owned, err := s.AcquireWorkflowLock("wf-1", 2, "bob", time.Minute, false)
	if err != nil || owned || other.UserLogin != "alice" {
		t.Fatalf("second acquire = %+v %v %v", other, owned, err)
	}
	// The owner renews without stealing.
	if _, owned, err := s.AcquireWorkflowLock("wf-1", 1, "alice", time.Minute, false); err != nil || !owned {
		t.Fatalf("renew = %v %v", owned, err)
	}
	// Takeover moves the lease.
	taken, owned, err := s.AcquireWorkflowLock("wf-1", 2, "bob", time.Minute, true)
	if err != nil || !owned || taken.UserLogin != "bob" {
		t.Fatalf("takeover = %+v %v %v", taken, owned, err)
	}
	// Release only affects the given owner.
	if err := s.ReleaseWorkflowLock("wf-1", 1); err != nil {
		t.Fatal(err)
	}
	if _, held := s.WorkflowLock("wf-1"); !held {
		t.Fatal("alice must not release bob's lease")
	}
	if err := s.ReleaseWorkflowLock("wf-1", 2); err != nil {
		t.Fatal(err)
	}
	if _, held := s.WorkflowLock("wf-1"); held {
		t.Fatal("lease should be free after release")
	}
}

// An expired lease never blocks the next editor.
func TestWorkflowLockExpires(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, err := s.AcquireWorkflowLock("wf-2", 1, "alice", time.Millisecond, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, held := s.WorkflowLock("wf-2"); held {
		t.Fatal("expired lease must read as free")
	}
	holder, owned, err := s.AcquireWorkflowLock("wf-2", 2, "bob", time.Minute, false)
	if err != nil || !owned || holder.UserLogin != "bob" {
		t.Fatalf("acquire after expiry = %+v %v %v", holder, owned, err)
	}
}
