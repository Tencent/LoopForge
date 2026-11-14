package engine

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// fakeNodeOutputs serves files and patches from maps; errs/patchErrs inject
// read failures keyed by relative path or repository dir.
type fakeNodeOutputs struct {
	files     map[string][]byte
	errs      map[string]error
	patches   map[string][]byte
	patchErrs map[string]error
}

func (f fakeNodeOutputs) readFile(rel string) ([]byte, error) {
	if err := f.errs[rel]; err != nil {
		return nil, err
	}
	if content, ok := f.files[rel]; ok {
		return content, nil
	}
	return nil, fs.ErrNotExist
}

func (f fakeNodeOutputs) repositoryPatch(dir string) ([]byte, error) {
	if err := f.patchErrs[dir]; err != nil {
		return nil, err
	}
	return f.patches[dir], nil
}

func TestContractArtifactReadsDeclaredOutput(t *testing.T) {
	t.Parallel()
	outputs := fakeNodeOutputs{files: map[string][]byte{"svc/docs/report.md": []byte("hello")}}
	rc := domain.RepositoryContext{WorkspaceDir: "svc"}
	artifact, ok, err := contractArtifact(outputs, "run-1", "impl", 2, rc, domain.ArtifactContract{Name: "report", Path: "docs/./report.md"}, false)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if artifact.Name != "report" || artifact.Path != "docs/report.md" || artifact.MediaType != "text/markdown" || artifact.Content != "hello" || artifact.RepositoryDir != "svc" || artifact.Attempt != 2 || artifact.RunID != "run-1" || artifact.NodeID != "impl" {
		t.Fatalf("artifact = %+v", artifact)
	}
	artifact, ok, err = contractArtifact(fakeNodeOutputs{files: map[string][]byte{"out.json": []byte("{}")}}, "run-1", "impl", 0, domain.RepositoryContext{}, domain.ArtifactContract{Name: "data", Path: "out.json", MediaType: "application/json"}, false)
	if err != nil || !ok || artifact.MediaType != "application/json" || artifact.RepositoryDir != "" {
		t.Fatalf("ok=%v err=%v artifact=%+v", ok, err, artifact)
	}
}

func TestContractArtifactSkipsAndErrors(t *testing.T) {
	t.Parallel()
	big := make([]byte, maxArtifactBytes+1)
	outputs := fakeNodeOutputs{
		files: map[string][]byte{"big.md": big},
		errs:  map[string]error{"huge.md": errOutputTooLarge, "broken.md": errors.New("boom")},
	}
	cases := []struct {
		name       string
		contract   domain.ArtifactContract
		bestEffort bool
		wantErr    string
	}{
		{name: "missing name", contract: domain.ArtifactContract{Path: "a.md"}, wantErr: "invalid artifact"},
		{name: "escaping path", contract: domain.ArtifactContract{Name: "x", Path: "../x.md"}, wantErr: "invalid artifact"},
		{name: "missing optional", contract: domain.ArtifactContract{Name: "x", Path: "absent.md"}},
		{name: "missing required", contract: domain.ArtifactContract{Name: "x", Path: "absent.md", Required: true}, wantErr: "x ():"},
		{name: "reader too large", contract: domain.ArtifactContract{Name: "x", Path: "huge.md"}, wantErr: "exceeds"},
		{name: "read error", contract: domain.ArtifactContract{Name: "x", Path: "broken.md"}, wantErr: "boom"},
		{name: "content too large", contract: domain.ArtifactContract{Name: "x", Path: "big.md"}, wantErr: "exceeds"},
		{name: "best effort invalid", contract: domain.ArtifactContract{Path: "a.md"}, bestEffort: true},
		{name: "best effort required missing", contract: domain.ArtifactContract{Name: "x", Path: "absent.md", Required: true}, bestEffort: true},
		{name: "best effort read error", contract: domain.ArtifactContract{Name: "x", Path: "broken.md"}, bestEffort: true},
		{name: "best effort too large", contract: domain.ArtifactContract{Name: "x", Path: "big.md"}, bestEffort: true},
	}
	for _, tc := range cases {
		_, ok, err := contractArtifact(outputs, "run", "node", 0, domain.RepositoryContext{}, tc.contract, tc.bestEffort)
		if ok {
			t.Fatalf("%s: expected skip or error", tc.name)
		}
		if tc.wantErr == "" {
			if err != nil {
				t.Fatalf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s: err = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestRepositoryPatchArtifacts(t *testing.T) {
	t.Parallel()
	root, err := repositoryPatchArtifacts(fakeNodeOutputs{patches: map[string][]byte{"": []byte("diff")}}, domain.Run{ID: "run"}, "node", 1, false)
	if err != nil || len(root) != 1 || root[0].Path != "repository.patch" || root[0].Name != "repository-patch" || root[0].MediaType != "text/plain" || root[0].RepositoryDir != "" {
		t.Fatalf("root patch = %+v err=%v", root, err)
	}
	run := domain.Run{ID: "run", Repositories: []domain.RepositoryContext{
		{RepositoryID: "a", Name: "a", WorkspaceDir: "svc-a"},
		{RepositoryID: "b", Name: "b", WorkspaceDir: "svc-b"},
		{RepositoryID: "c", Name: "c", WorkspaceDir: "svc-c"},
	}}
	outputs := fakeNodeOutputs{
		patches:   map[string][]byte{"svc-a": []byte("diff a")},
		patchErrs: map[string]error{"svc-c": errors.New("git failed")},
	}
	if _, err := repositoryPatchArtifacts(outputs, run, "node", 1, false); err == nil || err.Error() != "git failed" {
		t.Fatalf("strict patch error = %v", err)
	}
	patches, err := repositoryPatchArtifacts(outputs, run, "node", 1, true)
	if err != nil || len(patches) != 1 || patches[0].Path != "repository-svc-a.patch" || patches[0].RepositoryDir != "svc-a" || patches[0].Content != "diff a" {
		t.Fatalf("best-effort patches = %+v err=%v", patches, err)
	}
}
