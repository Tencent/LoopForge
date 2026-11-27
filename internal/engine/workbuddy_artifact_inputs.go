package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"agentflow/internal/domain"
)

const maxWorkBuddyArtifactInputBytes = 8 << 10

var workBuddyArtifactReference = regexp.MustCompile(`\{\{artifact\.[^{}\r\n]+\}\}`)

// workBuddyArtifactInputs sends only small upstream text files explicitly
// referenced by the node, using the archived bytes as the source of truth.
func (e *Engine) workBuddyArtifactInputs(ctx context.Context, wf domain.WorkflowDefinition, node domain.WorkflowNode, run domain.Run) (*strings.Replacer, error) {
	refs := workBuddyArtifactReference.FindAllString(node.TaskPrompt, -1)
	if len(refs) == 0 {
		return strings.NewReplacer(), nil
	}
	available := make(map[string]domain.Artifact)
	upstream := ancestors(wf, node.ID)
	for _, item := range run.Artifacts {
		if !upstream[item.NodeID] {
			continue
		}
		ref := "{{artifact." + item.NodeID + "." + item.Name + "}}"
		previous, ok := available[ref]
		if !ok || item.Attempt > previous.Attempt || item.Attempt == previous.Attempt && item.CreatedAt.After(previous.CreatedAt) {
			available[ref] = item
		}
	}
	var values []string
	loaded := make(map[string]bool)
	remaining := int64(maxWorkBuddyArtifactInputBytes)
	for _, ref := range refs {
		if loaded[ref] {
			continue
		}
		artifact, ok := available[ref]
		if !ok {
			return nil, fmt.Errorf("WorkBuddy 未找到上游交接产物 %s", ref)
		}
		stored, body, err := e.repo.OpenArtifactContent(ctx, run.ID, artifact.ID)
		if err != nil {
			return nil, fmt.Errorf("读取 WorkBuddy 交接产物 %s：%w", ref, err)
		}
		content, err := io.ReadAll(io.LimitReader(body, remaining+1))
		body.Close()
		if err != nil {
			return nil, fmt.Errorf("读取 WorkBuddy 交接产物 %s：%w", ref, err)
		}
		if int64(len(content)) > remaining {
			return nil, fmt.Errorf("WorkBuddy 单节点交接内容总量不得超过 %d KiB", maxWorkBuddyArtifactInputBytes>>10)
		}
		if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
			return nil, fmt.Errorf("WorkBuddy 交接产物 %s 必须是 UTF-8 文本", ref)
		}
		if stored.SHA256 != "" && !strings.EqualFold(stored.SHA256, fmt.Sprintf("%x", sha256.Sum256(content))) {
			return nil, fmt.Errorf("WorkBuddy 交接产物 %s 完整性校验失败", ref)
		}
		values = append(values, ref, string(content))
		loaded[ref] = true
		remaining -= int64(len(content))
	}
	return strings.NewReplacer(values...), nil
}
