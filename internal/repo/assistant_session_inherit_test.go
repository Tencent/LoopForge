package repo

import (
	"agentflow/internal/domain"
	"reflect"
	"testing"
)

func TestInheritStoredAssistantSessionKeepsStoredFields(t *testing.T) {
	old := domain.AssistantSession{
		Title:              "stored",
		HistoryRevision:    3,
		RootSessionID:      "root",
		ForkMessageIndex:   2,
		IssueID:            "issue-1",
		Archived:           true,
		SandboxProfileID:   "profile",
		WorkspaceRunID:     "run-old",
		OwnWorkspaceRunIDs: []string{"run-old"},
		Source:             domain.AssistantSourceScheduled,
		TaskID:             "task-old",
	}
	v := domain.AssistantSession{
		Title:              "incoming",
		HistoryRevision:    9,
		ForkMessageIndex:   7,
		IssueID:            "issue-2",
		SandboxProfileID:   "other",
		OwnWorkspaceRunIDs: []string{"run-new"},
	}
	inheritStoredAssistantSession(&v, old)

	if v.Title != "incoming" {
		t.Fatalf("title = %q, want incoming title kept", v.Title)
	}
	if v.HistoryRevision != 3 || v.RootSessionID != "root" || v.ForkMessageIndex != 2 {
		t.Fatalf("lineage = %d/%q/%d, want stored lineage", v.HistoryRevision, v.RootSessionID, v.ForkMessageIndex)
	}
	if v.IssueID != "issue-1" || !v.Archived || v.SandboxProfileID != "profile" {
		t.Fatalf("issue/archived/sandbox = %q/%v/%q, want stored values", v.IssueID, v.Archived, v.SandboxProfileID)
	}
	if v.WorkspaceRunID != "run-old" || !reflect.DeepEqual(v.OwnWorkspaceRunIDs, []string{"run-old"}) {
		t.Fatalf("workspace = %q/%v, want stored workspace runs", v.WorkspaceRunID, v.OwnWorkspaceRunIDs)
	}
	if v.Source != domain.AssistantSourceScheduled || v.TaskID != "task-old" {
		t.Fatalf("source/task = %q/%q, want stored values", v.Source, v.TaskID)
	}
}

func TestInheritStoredAssistantSessionKeepsIncomingWorkspaceSourceAndTask(t *testing.T) {
	old := domain.AssistantSession{WorkspaceRunID: "run-old", Source: domain.AssistantSourceSOP, TaskID: "task-old"}
	v := domain.AssistantSession{Title: "t", WorkspaceRunID: "run-new", Source: domain.AssistantSourceChat, TaskID: "task-new"}
	inheritStoredAssistantSession(&v, old)
	if v.WorkspaceRunID != "run-new" || v.Source != domain.AssistantSourceChat || v.TaskID != "task-new" {
		t.Fatalf("workspace/source/task = %q/%q/%q, want incoming values", v.WorkspaceRunID, v.Source, v.TaskID)
	}
}

func TestInheritStoredAssistantSessionInfersMissingSource(t *testing.T) {
	old := domain.AssistantSession{Shared: true, RunID: "run-1"}
	v := domain.AssistantSession{Title: "t"}
	inheritStoredAssistantSession(&v, old)
	if v.Source != domain.AssistantSourceSOP {
		t.Fatalf("source = %q, want inferred %q", v.Source, domain.AssistantSourceSOP)
	}
}

func TestInheritStoredAssistantSessionTitles(t *testing.T) {
	cases := []struct {
		name          string
		old, v        domain.AssistantSession
		wantTitle     string
		wantEdited    bool
		wantGenerated bool
	}{
		{
			name:      "blank incoming title keeps stored",
			old:       domain.AssistantSession{Title: "stored"},
			v:         domain.AssistantSession{Title: "  "},
			wantTitle: "stored",
		},
		{
			name:       "stored user rename wins over unedited save",
			old:        domain.AssistantSession{Title: "renamed", TitleEdited: true},
			v:          domain.AssistantSession{Title: "prompt"},
			wantTitle:  "renamed",
			wantEdited: true,
		},
		{
			name:          "stored generated title is not replaced by prompt",
			old:           domain.AssistantSession{Title: "summary", TitleGenerated: true},
			v:             domain.AssistantSession{Title: "prompt"},
			wantTitle:     "summary",
			wantGenerated: true,
		},
		{
			name:       "user rename clears generated flag",
			old:        domain.AssistantSession{Title: "summary", TitleGenerated: true},
			v:          domain.AssistantSession{Title: "renamed", TitleEdited: true},
			wantTitle:  "renamed",
			wantEdited: true,
		},
		{
			name:      "fork keeps stored title until renamed",
			old:       domain.AssistantSession{Title: "fork", ParentSessionID: "parent"},
			v:         domain.AssistantSession{Title: "prompt"},
			wantTitle: "fork",
		},
		{
			name:          "shared session accepts first generated title",
			old:           domain.AssistantSession{Title: "SOP label", Shared: true},
			v:             domain.AssistantSession{Title: "summary", TitleGenerated: true},
			wantTitle:     "summary",
			wantGenerated: true,
		},
		{
			name:      "shared session keeps stored title otherwise",
			old:       domain.AssistantSession{Title: "SOP label", Shared: true},
			v:         domain.AssistantSession{Title: "reply"},
			wantTitle: "SOP label",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.v
			inheritStoredAssistantSession(&v, tc.old)
			if v.Title != tc.wantTitle || v.TitleEdited != tc.wantEdited || v.TitleGenerated != tc.wantGenerated {
				t.Fatalf("title = %q edited=%v generated=%v, want %q edited=%v generated=%v",
					v.Title, v.TitleEdited, v.TitleGenerated, tc.wantTitle, tc.wantEdited, tc.wantGenerated)
			}
		})
	}
}

func TestInheritStoredAssistantSessionSharedKeepsRunAndProject(t *testing.T) {
	old := domain.AssistantSession{Title: "SOP", Shared: true, RunID: "run-1", ProjectID: "p1", ParentSessionID: "parent"}
	v := domain.AssistantSession{Title: "SOP", RunID: "run-2", ProjectID: "p2", ParentSessionID: "other"}
	inheritStoredAssistantSession(&v, old)
	if !v.Shared || v.RunID != "run-1" || v.ProjectID != "p1" || v.ParentSessionID != "parent" {
		t.Fatalf("shared/run/project/parent = %v/%q/%q/%q, want stored values", v.Shared, v.RunID, v.ProjectID, v.ParentSessionID)
	}
}
