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
	if err := s.PutSandbox(domain.SandboxProfile{ID: prefix, Name: "Native test", Driver: "native"}); err != nil {
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
				if err != nil || published.Version != 1 || published.PublishedRevision != published.Revision {
					t.Errorf("concurrent publish %s: version=%d revision=%d marker=%d err=%v", id, published.Version, published.Revision, published.PublishedRevision, err)
				}
			}()
		}
		close(start)
		wg.Wait()
	}
}

func TestWorkflowFirstPublishUsesVersionOne(t *testing.T) {
	for _, generic := range []bool{false, true} {
		t.Run(map[bool]string{false: "project", true: "generic"}[generic], func(t *testing.T) {
			s := testStoreWithGenericTemplates(t)
			draft := domain.WorkflowDefinition{ID: "first-publish", Name: "First publish", Status: "draft", Version: 1, Revision: 1}
			save := s.PutWorkflow
			publish := s.PublishWorkflow
			if generic {
				if err := s.PutGenericTemplate(GenericTemplate{Workflow: draft}); err != nil {
					t.Fatal(err)
				}
				save = func(v domain.WorkflowDefinition) error { _, err := s.UpdateGenericWorkflow(v); return err }
				publish = s.PublishGenericWorkflow
			} else if err := save(draft); err != nil {
				t.Fatal(err)
			}
			draft, _ = s.Workflow(draft.ID)
			draft.Description = "Saved before publishing"
			if err := save(draft); err != nil {
				t.Fatal(err)
			}
			draft, _ = s.Workflow(draft.ID)
			if draft.Status != "draft" || draft.Version != 1 || draft.PublishedRevision != 0 {
				t.Fatalf("saved draft = %+v", draft)
			}
			first, err := publish(draft.ID)
			if err != nil || first.Status != "published" || first.Version != 1 || first.PublishedRevision != first.Revision || first.Revision <= draft.Revision {
				t.Fatalf("first publication = %+v, %v", first, err)
			}
			again, err := publish(draft.ID)
			if err != nil || again.Version != first.Version || again.Revision != first.Revision {
				t.Fatalf("repeated publication = %+v, %v", again, err)
			}
			first.Status = "draft"
			first.Description = "Saved after publishing v1"
			if err := save(first); err != nil {
				t.Fatal(err)
			}
			second, err := publish(draft.ID)
			if err != nil || second.Version != 2 || second.PublishedRevision != second.Revision {
				t.Fatalf("second publication = %+v, %v", second, err)
			}
		})
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
