package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"testing"
	"time"
)

func TestWorkspaceEditorRequiresPausedRun(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run := domain.Run{ID: "edit", Status: "waiting", PauseRequested: true, Nodes: map[string]*domain.NodeRun{"dev": {NodeID: "dev", Status: "waiting_approval"}}}
	if err = st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err = st.AcquireWorkspaceEditor(run.ID, 10); err != nil {
		t.Fatal(err)
	}
	// No exclusive lease: a second writer may also enable edit once paused.
	if err = st.AcquireWorkspaceEditor(run.ID, 11); err != nil {
		t.Fatal(err)
	}
	wrote := false
	if err = st.WithWorkspaceEditor(run.ID, 11, func(domain.Run) error { wrote = true; return nil }); err != nil || !wrote {
		t.Fatal("writer could not edit paused workspace")
	}
	if err = st.ClearRunControl(run.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _ := st.RunState(run.ID)
	loaded.Status = "running"
	if err = st.PutRun(loaded); err != nil {
		t.Fatal(err)
	}
	if err = st.AcquireWorkspaceEditor(run.ID, 10); err == nil {
		t.Fatal("running run acquired edit")
	}
	if err = st.WithWorkspaceEditor(run.ID, 10, func(domain.Run) error { return nil }); err == nil {
		t.Fatal("running run allowed write")
	}
}

func TestWorkspaceEditorMySQLSerializesWriters(t *testing.T) {
	stores, id := admissionMySQLStores(t)
	admissionMySQLCleanup(t, stores[0], []string{id})
	if err := stores[0].PutRun(domain.Run{ID: id, Status: "waiting", PauseRequested: true, Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	if err := stores[0].AcquireWorkspaceEditor(id, 10); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	finish := make(chan struct{})
	writeDone := make(chan error, 1)
	resumeDone := make(chan error, 1)
	go func() {
		writeDone <- stores[0].WithWorkspaceEditor(id, 10, func(domain.Run) error { close(entered); <-finish; return nil })
	}()
	<-entered
	go func() { resumeDone <- stores[1].ClearRunControl(id) }()
	select {
	case err := <-resumeDone:
		t.Fatalf("resume escaped edit row lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-resumeDone; err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceEditorTerminalRunsAndReleaseFailure(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, status := range []string{"succeeded", "failed", "cancelled"} {
		r := domain.Run{ID: "edit-" + status, Status: status, Nodes: map[string]*domain.NodeRun{}}
		if err = st.PutRun(r); err != nil {
			t.Fatal(err)
		}
		if err = st.AcquireWorkspaceEditor(r.ID, 10); err != nil {
			t.Fatal(status, err)
		}
		if err = st.ReleaseWorkspaceEditor(r.ID, 10, func(domain.Run) error { return fmt.Errorf("terminal disconnect") }); err == nil {
			t.Fatal("failed shell shutdown released")
		}
		if err = st.ReleaseWorkspaceEditor(r.ID, 10); err != nil {
			t.Fatal(err)
		}
	}
}
