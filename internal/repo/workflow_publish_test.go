package repo

import (
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestMySQLConcurrentWorkflowPublish(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("requires disposable MySQL")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	prefix := "publish-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := s.PutSandbox(domain.SandboxProfile{ID: prefix, Name: "Local test", Driver: "local"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Table("sandboxes").Where("id = ?", prefix).Delete(nil) })
	for _, generic := range []bool{false, true} {
		id := prefix + "-project"
		wf := domain.WorkflowDefinition{ID: id, Name: "Concurrent publish", Status: "draft", SandboxProfileID: prefix, Version: 1, Revision: 1}
		publish := s.PublishWorkflow
		table := "workflows"
		if generic {
			id = prefix + "-generic"
			table = "generic_templates"
			wf.ID = id
			wf.Generic = true
			if err := s.PutGenericTemplate(GenericTemplate{Workflow: wf}); err != nil {
				t.Fatal(err)
			}
			publish = s.PublishGenericWorkflow
		} else if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			s.DB.Table(table).Where("id = ?", id).Delete(nil)
			s.DB.Table("definition_revisions").Where("entity_id = ?", id).Delete(nil)
		})
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				published, err := publish(id)
				if err != nil || published.Version != 2 || published.PublishedRevision != published.Revision {
					t.Errorf("concurrent publish %s: version=%d revision=%d marker=%d err=%v", id, published.Version, published.Revision, published.PublishedRevision, err)
				}
			}()
		}
		close(start)
		wg.Wait()
	}
}

func TestWorkflowPublishOnlyAfterChanges(t *testing.T) {
	for _, generic := range []bool{false, true} {
		t.Run(map[bool]string{false: "project", true: "generic"}[generic], func(t *testing.T) {
			s := testStoreWithGenericTemplates(t)
			id := "publish-check"
			save := func(v domain.WorkflowDefinition) error { return s.PutWorkflow(v) }
			publish := s.PublishWorkflow
			if generic {
				id = GenericBackendDeliveryWorkflowID
				save = func(v domain.WorkflowDefinition) error { _, err := s.UpdateGenericWorkflow(v); return err }
				publish = s.PublishGenericWorkflow
			} else if err := save(domain.WorkflowDefinition{ID: id, Name: "Publish check", Status: "published"}); err != nil {
				t.Fatal(err)
			}
			before, _ := s.Workflow(id)
			unchanged, err := publish(id)
			if err != nil || unchanged.Version != before.Version || unchanged.Revision != before.Revision {
				t.Fatalf("legacy repeat publication = %+v, %v", unchanged, err)
			}
			if err := save(unchanged); err != nil {
				t.Fatal(err)
			}
			unchanged, _ = s.Workflow(id)
			if unchanged.Revision != before.Revision {
				t.Fatal("saving unchanged content created a revision")
			}
			unchanged.Description = "edited script configuration"
			if err := save(unchanged); err != nil {
				t.Fatal(err)
			}
			changed, _ := s.Workflow(id)
			if !workflowNeedsPublish(changed) {
				t.Fatal("saved edits must be publishable")
			}
			published, err := publish(id)
			if err != nil || published.Version != before.Version+1 || published.PublishedRevision != published.Revision {
				t.Fatalf("changed publication = %+v, %v", published, err)
			}
			for i := 0; i < 3; i++ {
				if err := save(published); err != nil {
					t.Fatal(err)
				}
				again, err := publish(id)
				if err != nil || again.Version != published.Version || again.Revision != published.Revision {
					t.Fatalf("repeat publication = %+v, %v", again, err)
				}
			}
			published.Description = "another edit"
			published.PublishedRevision = published.Revision + 1 // Clients cannot mark edits as published.
			if err := save(published); err != nil {
				t.Fatal(err)
			}
			again, _ := s.Workflow(id)
			if !workflowNeedsPublish(again) {
				t.Fatal("client publication marker was trusted")
			}
		})
	}
}
