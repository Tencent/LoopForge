package repo

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The production catalog must pass the user's task to every executable role.
// Instructions alone describe a role but do not populate the node taskbook.
func TestPublicCatalogPassesRunInputToEveryRole(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/seed/001_public_catalog.sql")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`^INSERT IGNORE INTO generic_templates .*VALUES \('([^']*)','[^']*','(.*)',UTC_TIMESTAMP\(\)\);$`)
	count := 0
	for _, line := range strings.Split(string(raw), "\n") {
		match := pattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		var template GenericTemplate
		payload := strings.NewReplacer(`\\`, `\`, `''`, `'`).Replace(match[2])
		if err := json.Unmarshal([]byte(payload), &template); err != nil {
			t.Fatal(err)
		}
		count++
		for _, agent := range template.Agents {
			if agent.TaskPrompt == nil || !strings.Contains(strings.Join(agent.TaskPrompt.Inputs, "\n"), "{{input}}") {
				t.Errorf("%s role %s drops the user's run input", match[1], agent.ID)
			}
		}
	}
	if count != 4 {
		t.Fatalf("expected four public templates, got %d", count)
	}
}
