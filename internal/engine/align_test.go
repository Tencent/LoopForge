package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestExtractConflictBlocks(t *testing.T) {
	dir := t.TempDir()
	content := "line A\n" +
		"<<<<<<< HEAD\n" +
		"integration side v1\n" +
		"integration side v2\n" +
		"=======\n" +
		"master side v1\n" +
		">>>>>>> origin/master\n" +
		"line B\n"
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// 二进制文件应被跳过
	if err := os.WriteFile(filepath.Join(dir, "bin.bin"), []byte{0x00, 0x01, '<', '<'}, 0o644); err != nil {
		t.Fatal(err)
	}
	blocks := extractConflictBlocks(dir, []string{"a.txt", "bin.bin", "missing.txt"})
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d: %+v", len(blocks), blocks)
	}
	b := blocks[0]
	if b.File != "a.txt" {
		t.Fatalf("block file = %q, want a.txt", b.File)
	}
	if !strings.Contains(b.Ours, "integration side v1") || !strings.Contains(b.Ours, "integration side v2") {
		t.Fatalf("ours side wrong: %q", b.Ours)
	}
	if !strings.Contains(b.Theirs, "master side v1") {
		t.Fatalf("theirs side wrong: %q", b.Theirs)
	}
	if strings.Contains(b.Ours, "<<<<<<<") || strings.Contains(b.Theirs, ">>>>>>>") {
		t.Fatalf("sides must not contain markers: %q / %q", b.Ours, b.Theirs)
	}
}

func TestParseConflictMarkersLongSideClipped(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("<<<<<<< HEAD\n")
	for i := 0; i < 100; i++ {
		sb.WriteString("ours line\n")
	}
	sb.WriteString("=======\ntheirs\n>>>>>>> x\n")
	blocks := parseConflictMarkers("f.txt", []byte(sb.String()), 4)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if !strings.Contains(blocks[0].Ours, "截断") {
		t.Fatalf("long side must be clipped: %q", blocks[0].Ours)
	}
}

func TestAlignConflictErrorRenderBlocks(t *testing.T) {
	e := &AlignConflictError{Integrate: "agentflow/integration", Default: "master", Blocks: []domain.ConflictBlock{{File: "a.txt", Ours: "ours content", Theirs: "theirs content"}}}
	md := e.RenderBlocks()
	for _, want := range []string{"### 冲突文件 a.txt", "ours（集成分支 agentflow/integration）", "theirs（默认分支 master）", "ours content", "theirs content"} {
		if !strings.Contains(md, want) {
			t.Fatalf("rendered blocks missing %q in:\n%s", want, md)
		}
	}
}

func TestAlignOnlyApprovalsAutoSucceed(t *testing.T) {
	run := domain.Run{
		Repository: domain.RepositoryContext{AlignOnly: true},
		WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{
			{ID: "resolve", Type: "agent"},
			{ID: "confirm", Type: "approval"},
		}},
		Nodes: map[string]*domain.NodeRun{
			"resolve": {NodeID: "resolve", Status: "succeeded"},
			"confirm": {NodeID: "confirm", Status: "waiting_approval"},
		},
		Status: "waiting",
	}
	if !alignOnlyRun(run) || !alignRunWaitingApproval(run) {
		t.Fatal("align-only waiting approval should be detected")
	}
	if !autoSucceedAlignApprovals(&run, run.CreatedAt) {
		t.Fatal("expected approval to auto-succeed")
	}
	if run.Nodes["confirm"].Status != "succeeded" || run.Status != "running" {
		t.Fatalf("auto-pass = %+v status=%s", run.Nodes["confirm"], run.Status)
	}
	plain := domain.Run{
		WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "gate", Type: "approval"}}},
		Nodes:            map[string]*domain.NodeRun{"gate": {Status: "waiting_approval"}},
		Status:           "waiting",
	}
	if alignOnlyRun(plain) || autoSucceedAlignApprovals(&plain, plain.CreatedAt) {
		t.Fatal("ordinary approval must not auto-succeed")
	}
}

func TestShouldAlignIntegrate(t *testing.T) {
	if ShouldAlignIntegrate(domain.RepositoryContext{DefaultBranch: "master"}) {
		t.Fatal("empty integrate branch must skip baseline align")
	}
	if !ShouldAlignIntegrate(domain.RepositoryContext{DefaultBranch: "master", IntegrateBranch: "agentflow/integration"}) {
		t.Fatal("configured integrate branch should align")
	}
	if ShouldAlignIntegrate(domain.RepositoryContext{DefaultBranch: "master", IntegrateBranch: "master"}) {
		t.Fatal("same branch must skip align")
	}
}
