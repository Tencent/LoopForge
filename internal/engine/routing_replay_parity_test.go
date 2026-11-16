package engine

import (
	"agentflow/internal/domain"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

// The UI's retry choices and the engine's replay validation share these cases.
func TestRoutingReplayFrontendParity(t *testing.T) {
	raw, err := os.ReadFile("testdata/routing_replay_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string                     `json:"name"`
		Workflow domain.WorkflowDefinition  `json:"workflow"`
		Nodes    map[string]*domain.NodeRun `json:"nodes"`
		Target   string                     `json:"target"`
		Expected bool                       `json:"expected"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			run := domain.Run{WorkflowSnapshot: tc.Workflow, Nodes: tc.Nodes}
			if got := replayUpstreamSucceeded(run, tc.Target); got != tc.Expected {
				t.Fatalf("backend eligibility = %v, want %v", got, tc.Expected)
			}
		})
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	const script = `
const fs = require('node:fs');
const cases = JSON.parse(fs.readFileSync('testdata/routing_replay_cases.json', 'utf8'));
const code = fs.readFileSync('../../web/app/issues.js', 'utf8');
const state = {data:{workflows:[]}};
eval(code.slice(code.indexOf('function runNodeChoices('), code.indexOf('function runNodeAllowsContext(')));
for (const c of cases) {
  const actual = runNodeChoices({workflowSnapshot:c.workflow,nodes:c.nodes}).some(n=>n.id===c.target);
  if (actual !== c.expected) throw new Error(c.name+': frontend eligibility='+actual+', expected='+c.expected);
}`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("frontend eligibility: %v\n%s", err, out)
	}
}
