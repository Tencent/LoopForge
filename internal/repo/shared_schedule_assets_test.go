package repo

import (
	"errors"
	"reflect"
	"strings"

	"gorm.io/gorm"
	"testing"

	"agentflow/internal/domain"
)

func TestSharedSchedulePinsOnlyDirectReferences(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	role := domain.AgentDefinition{ID: "source-role", Name: "Role", ProjectID: "source", Shared: true, Rules: []string{"source-rule"}, Skills: []string{"source-skill"}}
	skill := domain.SkillDefinition{ID: "source-skill", Name: "Skill", ProjectID: "source"}
	external := domain.SkillDefinition{ID: "external", Name: "Library", ProjectID: "library", Shared: true}
	check(st.PutRule(domain.RuleDefinition{ID: "source-rule", Name: "Rule", ProjectID: "source"}))
	check(st.PutSkill(skill))
	check(st.PutSkill(external))
	check(st.PutAgent(role))
	beforeRole, _ := st.Agent(role.ID)
	job := domain.ScheduleJob{ID: "source-job", Name: "Job", ProjectID: "source", SkipDispatch: true, AssistantPrompt: "inspect", References: []domain.AssistantReference{{Type: "agent", ID: role.ID}, {Type: "skill", ID: external.ID}}}
	check(st.PutScheduleJob(job))
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Published", Prompt: "inspect", References: []domain.AssistantReference{{Type: "skill", ID: "forged"}}}, 0)
	check(err)
	if len(v.References) != 2 || v.References[0].ID != role.ID || !v.HasAsset("agent", role.ID) || !v.HasAsset("skill", external.ID) || v.HasAsset("rule", "source-rule") || v.HasAsset("skill", skill.ID) {
		t.Fatalf("incomplete version: %+v", v)
	}
	check(st.ValidateSharedScheduleAssets(v))
	savedRole, _ := st.Agent(role.ID)
	savedSkill, _ := st.Skill(skill.ID)
	if !reflect.DeepEqual(beforeRole, savedRole) || len(st.Revisions("agent", role.ID)) != 0 || savedSkill.Shared {
		t.Fatal("publication changed nested asset sharing state")
	}

	// Permission is checked again after publication, rather than inferred
	// from the subscriber's membership of the publishing project.
	external.Shared = false
	check(st.PutSkill(external))
	if err := st.ValidateSharedScheduleAssets(v); !errors.Is(err, ErrSharedScheduleReferences) {
		t.Fatalf("revoked external permission: %v", err)
	}
	external.Shared = true
	check(st.PutSkill(external))
	check(st.ValidateSharedScheduleAssets(v))
	check(st.PutSkill(domain.SkillDefinition{ID: "later", Name: "Later", ProjectID: "source"}))
	role.Skills = append(role.Skills, "later")
	check(st.PutAgent(role))
	if err := st.ValidateSharedScheduleAssets(v); err != nil {
		t.Fatalf("nested binding change invalidated direct version: %v", err)
	}
	v2, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Updated", Prompt: "inspect"}, 1)
	check(err)
	check(st.ValidateSharedScheduleAssets(v2))
	if !v2.HasAsset("agent", role.ID) || v2.HasAsset("skill", "later") {
		t.Fatal("version contains nested dependency")
	}
}

func TestSharedScheduleRejectsEntirePublicationForUnshareableReferences(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, typ := range []string{"issue", "repository", "run", "unknown"} {
		t.Run(typ, func(t *testing.T) {
			job := domain.ScheduleJob{ID: "blocked-" + typ, Name: "Job", ProjectID: "source", SkipDispatch: true, AssistantPrompt: "inspect", References: []domain.AssistantReference{{Type: typ, ID: "private"}}}
			if err := st.PutScheduleJob(job); err != nil {
				t.Fatal(err)
			}
			if _, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Blocked", Prompt: "inspect"}, 0); !errors.Is(err, ErrSharedScheduleReferences) {
				t.Fatalf("unshareable reference: %v", err)
			}
			if saved, ok := st.ScheduleJob(job.ID); !ok || len(saved.References) != 1 {
				t.Fatal("failed publication damaged private task")
			}
		})
	}
	versions, err := st.SharedScheduleTasks()
	if err != nil || len(versions) != 0 {
		t.Fatalf("failed publication left versions: %+v %v", versions, err)
	}
}

func TestSharedScheduleSharesPrivateSkillAndPreservesStoredFiles(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	skill := domain.SkillDefinition{ID: "private-skill", Name: "Private", ProjectID: "source", Content: "# Skill", Files: map[string]string{"scripts/check.sh": "echo check"}, Source: &domain.SkillSource{Provider: domain.SkillSourceSkillHub, Namespace: "owner", Slug: "skill", Version: "1.0"}}
	if err := st.PutSkill(skill); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSkillFiles(skill.ID, []SkillFileInput{{Path: "assets/data.bin", Content: []byte{0, 1, 255}, Mode: 0o644}}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.Skill(skill.ID)
	files := st.SkillFileContents(skill.ID)
	job := domain.ScheduleJob{ID: "private-direct", Name: "Private", ProjectID: "source", SkipDispatch: true, AssistantPrompt: "inspect", References: []domain.AssistantReference{{Type: "skill", ID: skill.ID}}}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Published", Prompt: "inspect"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Assets) != 1 || !v.HasAsset("skill", skill.ID) {
		t.Fatalf("direct skill was not published: %+v", v)
	}
	saved, _ := st.Skill(skill.ID)
	before.Shared, before.UpdatedAt = true, saved.UpdatedAt
	if !reflect.DeepEqual(before, saved) || !reflect.DeepEqual(files, st.SkillFileContents(skill.ID)) {
		t.Fatalf("sharing changed skill data: before=%+v after=%+v", before, saved)
	}
	if err := st.ValidateSharedScheduleAssets(v); err != nil {
		t.Fatal(err)
	}
	saved.Shared = false
	if err := st.PutSkill(saved); err != nil {
		t.Fatal(err)
	}
	if err := st.ValidateSharedScheduleAssets(v); !errors.Is(err, ErrSharedScheduleReferences) {
		t.Fatalf("revoked skill permission accepted: %v", err)
	}
	if current, _ := st.Skill(skill.ID); current.Shared {
		t.Fatal("runtime validation re-shared a revoked skill")
	}
}

func TestSharedScheduleCanonicalReferencesAndOwnership(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	skill := domain.SkillDefinition{ID: "canonical-skill", Name: "Actual skill", ProjectID: "source", Shared: true}
	if err := st.PutSkill(skill); err != nil {
		t.Fatal(err)
	}
	job := domain.ScheduleJob{ID: "canonical-job", Name: "Job", ProjectID: "source", SkipDispatch: true, AssistantPrompt: "inspect", References: []domain.AssistantReference{
		{Type: " skill ", ID: " canonical-skill ", Name: "forged", ProjectID: "forged", ProjectName: "forged", Element: &domain.AssistantPageElement{}},
		{Type: "skill", ID: skill.ID},
	}}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Shared", Prompt: "inspect"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.AssistantReference{Type: "skill", ID: skill.ID, Name: skill.Name, ProjectID: skill.ProjectID}
	if len(v.References) != 1 || v.References[0] != want || len(v.Assets) != 1 {
		t.Fatalf("non-canonical publication: %+v", v)
	}
	if err := st.ValidateSharedScheduleAssets(v); err != nil {
		t.Fatal(err)
	}
	skill.ProjectID = "other"
	if err := st.PutSkill(skill); err != nil {
		t.Fatal(err)
	}
	if err := st.ValidateSharedScheduleAssets(v); !errors.Is(err, ErrSharedScheduleReferences) {
		t.Fatalf("ownership change accepted: %v", err)
	}
}

func TestSharedScheduleDoesNotPinSOPDependencies(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(st.PutDockerfile(domain.DockerfileDefinition{ID: "image", Name: "Image", ProjectID: "source", Content: "FROM scratch"}))
	check(st.PutEnvironment(domain.EnvironmentDefinition{ID: "setup", Name: "Setup", ProjectID: "source"}))
	check(st.PutSandbox(domain.SandboxProfile{ID: "sandbox", Name: "Sandbox", ProjectID: "source", Driver: "docker", DockerfileID: "image", EnvironmentID: "setup"}))
	check(st.PutSandbox(domain.SandboxProfile{ID: "native", Name: "Native", ProjectID: "source", Driver: "native", EnvironmentID: "setup"}))
	check(st.PutWorkflow(domain.WorkflowDefinition{ID: "child", Name: "Child", ProjectID: "source", SandboxProfileID: "native", Nodes: []domain.WorkflowNode{{ID: "approve", Type: "approval", Name: "Approve"}}}))
	check(st.PutWorkflow(domain.WorkflowDefinition{ID: "parent", Name: "Parent", ProjectID: "source", Shared: true, SandboxProfileID: "sandbox", Nodes: []domain.WorkflowNode{{ID: "child", Type: "sop", SOPWorkflowID: "child", Name: "Child"}}}))
	job := domain.ScheduleJob{ID: "sop-task", Name: "SOP task", ProjectID: "source", SkipDispatch: true, AssistantPrompt: "start", References: []domain.AssistantReference{{Type: "sop", ID: "parent"}}}
	check(st.PutScheduleJob(job))
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "SOP task", Prompt: "start"}, 0)
	check(err)
	if !v.HasAsset("sop", "parent") || v.HasAsset("sop", "child") || v.HasAsset("sandbox", "sandbox") || v.HasAsset("dockerfile", "image") || v.HasAsset("environment", "setup") {
		t.Fatalf("version contains nested SOP dependencies: %+v", v.Assets)
	}
	profile, _ := st.Sandbox("sandbox")
	profile.ProjectID = "foreign"
	profile.Shared = false
	check(st.PutSandbox(profile))
	if err := st.ValidateSharedScheduleAssets(v); err != nil {
		t.Fatalf("nested sandbox change invalidated direct version: %v", err)
	}
	if _, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Updated", Prompt: "start"}, 1); err != nil {
		t.Fatalf("republishing direct SOP failed: %v", err)
	}
	versions, err := st.SharedScheduleVersions(v.ID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("partial publication: %+v %v", versions, err)
	}
}

func TestSharedScheduleSOPPreservesTemplateFallbacksAndBuiltInPreview(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(SeedTestGenericTemplates(st))
	check(st.PutAgent(domain.AgentDefinition{ID: GenericAgentRequirement, Name: "Project override", ProjectID: "source", Skills: []string{"not-in-template"}}))
	check(st.PutRule(domain.RuleDefinition{ID: GenericRuleCore, Name: "Foreign override", ProjectID: "other"}))
	job := domain.ScheduleJob{ID: "template-task", Name: "Template", ProjectID: "source", SkipDispatch: true, AssistantPrompt: "start", References: []domain.AssistantReference{{Type: "sop", ID: GenericBackendDeliveryWorkflowID}}}
	check(st.PutScheduleJob(job))
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Template", Prompt: "start"}, 0)
	check(err)
	check(st.ValidateSharedScheduleAssets(v))
	if !v.HasAsset("sop", GenericBackendDeliveryWorkflowID) || v.HasAsset("rule", GenericRuleCore) || v.HasAsset("skill", "not-in-template") {
		t.Fatalf("publication stored nested template dependencies: %+v", v.Assets)
	}
	// The preview builder is an engine-generated role, not a catalog asset.
	wf := domain.WorkflowDefinition{ID: "preview-sop", Name: "Preview", ProjectID: "source", Shared: true, SandboxProfileID: "local", Preview: &domain.PreviewContract{Enabled: true}, Nodes: []domain.WorkflowNode{{ID: "approve", Type: "approval", Name: "Approve"}}}
	wf, err = domain.AttachPreviewBuild(wf)
	check(err)
	check(st.PutWorkflow(wf))
	job.ID, job.References = "preview-task", []domain.AssistantReference{{Type: "sop", ID: wf.ID}}
	check(st.PutScheduleJob(job))
	preview, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Preview", Prompt: "start"}, 0)
	check(err)
	check(st.ValidateSharedScheduleAssets(preview))
	if preview.HasAsset("agent", domain.PreviewBuildAgentID) {
		t.Fatal("synthetic preview role was treated as a catalog dependency")
	}
}

func TestSharedScheduleSharesPrivateAgentThroughCatalogBoundary(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(st.PutRule(domain.RuleDefinition{ID: "owned-rule", Name: "Rule", ProjectID: "source"}))
	check(st.PutSkill(domain.SkillDefinition{ID: "owned-skill", Name: "Skill", ProjectID: "source"}))
	role := domain.AgentDefinition{ID: "private-role", Name: "Role", ProjectID: "source", Rules: []string{"owned-rule"}, Skills: []string{"owned-skill"}}
	check(st.PutAgent(role))
	job := domain.ScheduleJob{ID: "agent-task", Name: "Task", ProjectID: "source", SkipDispatch: true, AssistantPrompt: "inspect", References: []domain.AssistantReference{{Type: "agent", ID: role.ID}}}
	check(st.PutScheduleJob(job))
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Shared", Prompt: "inspect"}, 0)
	check(err)
	agent, _ := st.Agent(role.ID)
	rule, _ := st.Rule("owned-rule")
	skill, _ := st.Skill("owned-skill")
	if !agent.Shared || !rule.Shared || !skill.Shared {
		t.Fatalf("asset sharing path was bypassed: agent=%v rule=%v skill=%v", agent.Shared, rule.Shared, skill.Shared)
	}
	if len(v.Assets) != 1 || !v.HasAsset("agent", role.ID) {
		t.Fatalf("nested assets were pinned: %+v", v.Assets)
	}
	if len(st.Revisions("agent", role.ID)) != 1 {
		t.Fatal("sharing bypassed the normal agent revision save")
	}
}

func TestSharedScheduleReusesExternalSharedAgentWithoutCascading(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutSkill(domain.SkillDefinition{ID: "library-skill", Name: "Skill", ProjectID: "library"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAgent(domain.AgentDefinition{ID: "library-role", Name: "Role", ProjectID: "library", Shared: true, Skills: []string{"library-skill"}}); err != nil {
		t.Fatal(err)
	}
	beforeRole, _ := st.Agent("library-role")
	beforeSkill, _ := st.Skill("library-skill")
	job := domain.ScheduleJob{ID: "library-task", Name: "Task", ProjectID: "source", SkipDispatch: true, References: []domain.AssistantReference{{Type: "agent", ID: "library-role"}}}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Shared", Prompt: "inspect"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	afterRole, _ := st.Agent("library-role")
	afterSkill, _ := st.Skill("library-skill")
	if !reflect.DeepEqual(beforeRole, afterRole) || !reflect.DeepEqual(beforeSkill, afterSkill) || len(st.Revisions("agent", beforeRole.ID)) != 0 {
		t.Fatal("reusing a shared asset replayed its internal sharing writes")
	}
	if len(v.Assets) != 1 || v.Assets[0].ProjectID != "library" {
		t.Fatalf("shared reference lost its real owner: %+v", v.Assets)
	}
}

func TestSharedSchedulePrivateSOPUsesExistingSharingChecks(t *testing.T) {
	for _, tc := range []struct {
		name, wantError string
		privateSandbox  bool
		notifications   bool
		missingAgent    bool
	}{
		{name: "shared sandbox"},
		{name: "private sandbox", privateSandbox: true, wantError: "共享 SOP 只能使用已共享的沙箱"},
		{name: "project notifications", notifications: true, wantError: "通知绑定只能保存在本项目流程中"},
		{name: "missing bound agent", missingAgent: true, wantError: "不存在"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			check(st.PutSkill(domain.SkillDefinition{ID: "first-skill", Name: "Skill", ProjectID: "source"}))
			profile := domain.SandboxProfile{ID: "sop-sandbox", Name: "Sandbox", ProjectID: "source", Driver: "native", Shared: !tc.privateSandbox}
			check(st.PutSandbox(profile))
			wf := domain.WorkflowDefinition{ID: "private-sop", Name: "SOP", ProjectID: "source", SandboxProfileID: profile.ID, Status: "draft", Version: 4, Nodes: []domain.WorkflowNode{{ID: "approve", Name: "Approve", Type: "approval"}}}
			if tc.missingAgent {
				wf.Nodes = []domain.WorkflowNode{{ID: "agent", Name: "Missing", Type: "agent", AgentID: "missing"}}
			}
			check(st.PutWorkflow(wf))
			if tc.notifications {
				// Seed an existing project binding without depending on external
				// Bot installation; sharing must reject it before querying a Bot.
				wf, _ = st.Workflow(wf.ID)
				wf.Notifications = []domain.NotificationBinding{{BotID: "project-bot", Scope: "run", Events: []string{"succeeded"}}}
				check(st.DB.Table("workflows").Where("id = ?", wf.ID).Update("payload", mustJSON(wf)).Error)
			}
			before, _ := st.Workflow(wf.ID)
			beforeSkill, _ := st.Skill("first-skill")
			job := domain.ScheduleJob{ID: "sop-job", Name: "Task", ProjectID: "source", SkipDispatch: true, References: []domain.AssistantReference{{Type: "skill", ID: "first-skill"}, {Type: "sop", ID: wf.ID}}}
			check(st.PutScheduleJob(job))
			v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Shared", Prompt: "inspect"}, 0)
			after, _ := st.Workflow(wf.ID)
			afterSkill, _ := st.Skill("first-skill")
			if tc.wantError != "" {
				if !errors.Is(err, ErrSharedScheduleReferences) || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("sharing error = %v, want %q", err, tc.wantError)
				}
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeSkill, afterSkill) || len(st.Revisions("workflow", wf.ID)) != 0 {
					t.Fatal("failed publication left asset mutations or revisions")
				}
				versions, err := st.SharedScheduleTasks()
				check(err)
				if len(versions) != 0 {
					t.Fatal("failed publication left a shared version")
				}
				return
			}
			check(err)
			if !after.Shared || !afterSkill.Shared || after.Status != before.Status || after.Version != before.Version || after.Revision != before.Revision+1 || after.PublishedRevision != before.PublishedRevision {
				t.Fatalf("SOP sharing changed publication semantics: before=%+v after=%+v", before, after)
			}
			currentProfile, _ := st.Sandbox(profile.ID)
			if currentProfile.Shared != profile.Shared || len(v.Assets) != 2 || v.HasAsset("sandbox", profile.ID) {
				t.Fatalf("SOP sharing traversed the sandbox boundary: %+v", v.Assets)
			}
		})
	}
}

func TestSharedSchedulePublishingWriteFailureRollsBackAssetsAndRevisions(t *testing.T) {
	for _, table := range []string{"agents", "shared_schedule_versions"} {
		t.Run(table, func(t *testing.T) {
			st, err := OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			check(st.PutRule(domain.RuleDefinition{ID: "rollback-rule", Name: "Rule", ProjectID: "source"}))
			check(st.PutSkill(domain.SkillDefinition{ID: "rollback-skill", Name: "Skill", ProjectID: "source"}))
			check(st.PutAgent(domain.AgentDefinition{ID: "rollback-agent", Name: "Agent", ProjectID: "source", Rules: []string{"rollback-rule"}, Skills: []string{"rollback-skill"}}))
			beforeRole, _ := st.Agent("rollback-agent")
			beforeRule, _ := st.Rule("rollback-rule")
			beforeSkill, _ := st.Skill("rollback-skill")
			job := domain.ScheduleJob{ID: "rollback-job", Name: "Task", ProjectID: "source", SkipDispatch: true, References: []domain.AssistantReference{{Type: "agent", ID: "rollback-agent"}}}
			check(st.PutScheduleJob(job))
			injected := errors.New("injected publish write failure")
			reached := false
			check(st.DB.Callback().Create().Before("gorm:create").Register("test:publish-failure", func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					reached = true
					tx.AddError(injected)
				}
			}))
			_, err = st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Shared", Prompt: "inspect"}, 0)
			if !reached || !errors.Is(err, injected) {
				t.Fatalf("write injection was not exercised: reached=%v error=%v", reached, err)
			}
			afterRole, _ := st.Agent("rollback-agent")
			afterRule, _ := st.Rule("rollback-rule")
			afterSkill, _ := st.Skill("rollback-skill")
			if !reflect.DeepEqual(beforeRole, afterRole) || !reflect.DeepEqual(beforeRule, afterRule) || !reflect.DeepEqual(beforeSkill, afterSkill) || len(st.Revisions("agent", beforeRole.ID)) != 0 {
				t.Fatal("failed transaction left asset states, timestamps or revisions")
			}
			versions, err := st.SharedScheduleTasks()
			check(err)
			if len(versions) != 0 {
				t.Fatal("failed transaction left a shared version")
			}
		})
	}
}

func TestSharedScheduleVersionConflictDoesNotShareNewReference(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	job := domain.ScheduleJob{ID: "version-job", Name: "Task", ProjectID: "source", SkipDispatch: true}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Shared", Prompt: "inspect"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutSkill(domain.SkillDefinition{ID: "new-skill", Name: "Skill", ProjectID: "source"}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.Skill("new-skill")
	job.References = []domain.AssistantReference{{Type: "skill", ID: "new-skill"}}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Stale", Prompt: "inspect"}, 0); !errors.Is(err, ErrSharedScheduleVersionConflict) {
		t.Fatalf("stale publication error = %v", err)
	}
	after, _ := st.Skill("new-skill")
	versions, err := st.SharedScheduleVersions(v.ID)
	if err != nil || len(versions) != 1 || !reflect.DeepEqual(before, after) {
		t.Fatalf("version conflict changed asset or history: %+v %v", versions, err)
	}
}
