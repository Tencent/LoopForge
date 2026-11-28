package repo

import (
	"testing"

	"agentflow/internal/domain"
)

// The SOP detection sweep asks the database for the projects that have SOP
// detection enabled. sqlite's json_extract answers a JSON boolean with the
// integer 1, so the query binds the numeric form as well as the text one.
func TestSOPDetectionProjectsFindsTheEnabledProjectOnSQLite(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatalf("open the test store: %v", err)
	}
	if err := s.PutProject(domain.Project{ID: "sop-on", Name: "开启"}); err != nil {
		t.Fatalf("put the enabled project: %v", err)
	}
	if err := s.PutSOPDetection("sop-on", domain.SOPDetectionConfig{Enabled: true, Model: "gpt"}); err != nil {
		t.Fatalf("enable the SOP detection: %v", err)
	}
	if err := s.PutProject(domain.Project{ID: "sop-off", Name: "关闭"}); err != nil {
		t.Fatalf("put the disabled project: %v", err)
	}
	if err := s.PutSOPDetection("sop-off", domain.SOPDetectionConfig{Enabled: false}); err != nil {
		t.Fatalf("disable the SOP detection: %v", err)
	}
	projects, err := s.SOPDetectionProjects()
	if err != nil {
		t.Fatalf("list the SOP detection projects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != "sop-on" {
		t.Fatalf("the SOP detection projects = %+v, want only the enabled project", projects)
	}
	if projects[0].Config.Model != "gpt" {
		t.Fatalf("the SOP detection config = %+v, want the stored one", projects[0].Config)
	}
}
