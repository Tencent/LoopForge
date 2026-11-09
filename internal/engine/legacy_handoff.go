package engine

// Read-only compatibility for SOPs that already archived a group handoff.
import (
	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const groupSOPHandoffNode = "group-handoff"

type sopHandoffTranscriptPart struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	InputPath string `json:"inputPath"`
	SHA256    string `json:"sha256"`
}
type sopHandoffTranscriptManifest struct {
	Format string                     `json:"format"`
	Bytes  int                        `json:"bytes"`
	SHA256 string                     `json:"sha256"`
	Parts  []sopHandoffTranscriptPart `json:"parts"`
}

const sopHandoffTranscriptFormat = "agentflow.sop-handoff.transcript/v1"

func archivedSOPHandoffArtifacts(run domain.Run) ([]domain.Artifact, error) {
	ref := run.WorkflowSnapshot.GroupSOPHandoff
	if ref == nil {
		return nil, nil
	}
	selected := []domain.Artifact{}
	for _, expected := range []struct{ id, path, name string }{{ref.SummaryArtifactID, ref.SummaryPath, "sop-handoff"}, {ref.TranscriptArtifactID, ref.TranscriptPath, "sop-handoff-conversation"}} {
		found := false
		for _, a := range run.Artifacts {
			if a.ID == expected.id && a.Path == expected.path && a.Name == expected.name && a.NodeID == groupSOPHandoffNode && a.SHA256 == runtime.WorkspaceContentSHA(a.Content) {
				selected = append(selected, a)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("自由协作交接归档缺失或校验失败，不能启动 SOP")
		}
	}
	var manifest sopHandoffTranscriptManifest
	if err := json.Unmarshal([]byte(selected[1].Content), &manifest); err != nil {
		return nil, fmt.Errorf("自由协作交接归档格式无效")
	}
	if manifest.Format == "" {
		return selected, nil
	}
	if manifest.Format != sopHandoffTranscriptFormat || len(manifest.Parts) < 2 || len(manifest.Parts) > 16 || manifest.Bytes > runtime.WorkspaceCheckpointMaxBytes {
		return nil, fmt.Errorf("自由协作交接归档清单无效")
	}
	for i, part := range manifest.Parts {
		if part.Name != fmt.Sprintf("sop-handoff-conversation-part-%03d", i+1) || part.Path != filepath.ToSlash(filepath.Join(filepath.Dir(ref.TranscriptPath), fmt.Sprintf("conversation.part-%03d.txt", i+1))) || part.InputPath != sopHandoffInputPath(ref, part.Name) {
			return nil, fmt.Errorf("自由协作交接归档分片路径无效")
		}
		found := false
		for _, a := range run.Artifacts {
			if a.Name == part.Name && a.Path == part.Path && a.NodeID == groupSOPHandoffNode && a.SHA256 == part.SHA256 && a.SHA256 == runtime.WorkspaceContentSHA(a.Content) && len(a.Content) <= maxArtifactBytes {
				selected = append(selected, a)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("自由协作交接归档分片缺失或校验失败")
		}
	}
	if _, err := readSOPHandoffTranscript(selected); err != nil {
		return nil, err
	}
	return selected, nil
}
func readSOPHandoffTranscript(artifacts []domain.Artifact) ([]byte, error) {
	if len(artifacts) < 2 {
		return nil, fmt.Errorf("自由协作交接归档不完整")
	}
	var manifest sopHandoffTranscriptManifest
	root := []byte(artifacts[1].Content)
	if err := json.Unmarshal(root, &manifest); err != nil {
		return nil, err
	}
	if manifest.Format == "" {
		return root, nil
	}
	if manifest.Format != sopHandoffTranscriptFormat || len(artifacts) != len(manifest.Parts)+2 || manifest.Bytes < 0 || manifest.Bytes > runtime.WorkspaceCheckpointMaxBytes {
		return nil, fmt.Errorf("自由协作交接归档清单无效")
	}
	var full bytes.Buffer
	for _, part := range artifacts[2:] {
		if full.Len()+len(part.Content) > runtime.WorkspaceCheckpointMaxBytes {
			return nil, fmt.Errorf("自由协作交接归档过大")
		}
		full.WriteString(part.Content)
	}
	if full.Len() != manifest.Bytes || runtime.WorkspaceContentSHA(full.String()) != manifest.SHA256 {
		return nil, fmt.Errorf("自由协作交接完整归档校验失败")
	}
	return full.Bytes(), nil
}

// Inputs are independent of graph ancestry, and use archived content on every
// dispatch so retries and reconstructed/remote workspaces retain the handoff.
func sopHandoffInputs(run domain.Run) (string, []domain.RunnerInputFile) {
	ref := run.WorkflowSnapshot.GroupSOPHandoff
	if ref == nil || run.WorkflowSnapshot.CollaborationMode == "group" {
		return "", nil
	}
	var manifest strings.Builder
	var files []domain.RunnerInputFile
	artifacts, err := archivedSOPHandoffArtifacts(run)
	if err != nil {
		return "", nil
	}
	for _, a := range artifacts {
		inputPath := sopHandoffInputPath(ref, a.Name)
		files = append(files, domain.RunnerInputFile{Path: inputPath, Content: a.Content})
		writeInputManifestEntry(&manifest, a, inputPath)
	}
	return manifest.String(), files
}

func sopHandoffInputPath(ref *domain.GroupSOPHandoff, name string) string {
	ext := ".md"
	if name == "sop-handoff-conversation" {
		ext = ".json"
	} else if strings.HasPrefix(name, "sop-handoff-conversation-part-") {
		ext = ".txt"
	}
	return ".agentflow/inputs/group-handoff/" + safePathSegment(ref.ID) + "-" + safePathSegment(name) + ext
}
func validateSOPHandoffArtifacts(run domain.Run) error {
	ref := run.WorkflowSnapshot.GroupSOPHandoff
	if ref == nil || run.WorkflowSnapshot.CollaborationMode == "group" {
		return nil
	}
	if _, err := archivedSOPHandoffArtifacts(run); err != nil {
		return err
	}
	return nil
}
func materializeSOPHandoffInputs(workspace string, run domain.Run) (string, error) {
	if err := validateSOPHandoffArtifacts(run); err != nil {
		return "", err
	}
	manifest, files := sopHandoffInputs(run)
	if len(files) == 0 {
		return "", nil
	}
	dir, err := runtime.EnsureControlDirectory(workspace, "inputs", groupSOPHandoffNode)
	if err != nil {
		return "", err
	}
	for _, f := range files {
		target := filepath.Join(dir, filepath.Base(f.Path))
		if info, err := os.Lstat(target); err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("交接输入不是普通文件：%s", f.Path)
			}
			if err := os.Chmod(target, 0600); err != nil {
				return "", err
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if err := os.WriteFile(target, []byte(f.Content), 0600); err != nil {
			return "", err
		}
		if err := os.Chmod(target, 0444); err != nil {
			return "", err
		}
	}
	return manifest, nil
}

// Child SOPs have independent workspaces on master. Freeze the same handoff
// into the child before scheduling its first node, rather than assuming files
// in the parent's directory are visible there.
func inheritSOPHandoff(parent domain.Run, child *domain.Run) error {
	ref := parent.WorkflowSnapshot.GroupSOPHandoff
	if ref == nil {
		return nil
	}
	if parent.ExecutionContext.ProjectID != child.ExecutionContext.ProjectID {
		return fmt.Errorf("交接上下文不能跨项目继承")
	}
	if err := validateSOPHandoffArtifacts(parent); err != nil {
		return err
	}
	selected, err := archivedSOPHandoffArtifacts(parent)
	if err != nil {
		return err
	}
	copy := *ref
	for _, a := range selected {
		cloned := newArtifact(child.ID, groupSOPHandoffNode, 0, a.Name, a.Path, a.MediaType, []byte(a.Content))
		child.Artifacts = append(child.Artifacts, cloned)
		if a.ID == ref.SummaryArtifactID {
			copy.SummaryArtifactID = cloned.ID
		}
		if a.ID == ref.TranscriptArtifactID {
			copy.TranscriptArtifactID = cloned.ID
		}
	}
	child.WorkflowSnapshot.GroupSOPHandoff = &copy
	return nil
}

// Shipped Runner clients cap a claim response at 8 MiB. Splitting a long
// transcript removes the per-file limit, but must not queue a task that those
// clients can never decode. Reserve 4 KiB for the lease fields added at claim.
func validateSOPHandoffRunnerPayload(run domain.Run, task domain.RunnerTask) error {
	if run.WorkflowSnapshot.GroupSOPHandoff == nil {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"task": task})
	if err != nil {
		return err
	}
	if len(payload) > (8<<20)-(4<<10) {
		return fmt.Errorf("完整交接输入超出执行机器的 8 MiB 任务响应上限；原始归档完整保留，未截断，请查看交接摘要与归档后调整继续任务")
	}
	return nil
}
