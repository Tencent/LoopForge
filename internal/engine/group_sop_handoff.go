package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

const groupSOPHandoffNode = "group-handoff"

type sopHandoffOperationKey struct{}
type sopHandoffOperation struct {
	id       string
	progress func(string) error
}

// WithSOPHandoffOperation binds a durable job identity and optional progress
// recorder to an otherwise synchronous handoff. Existing callers remain valid.
func WithSOPHandoffOperation(ctx context.Context, id string, progress func(phase string) error) context.Context {
	return context.WithValue(ctx, sopHandoffOperationKey{}, sopHandoffOperation{id: id, progress: progress})
}

func sopHandoffOperationFromContext(ctx context.Context) (sopHandoffOperation, error) {
	op, _ := ctx.Value(sopHandoffOperationKey{}).(sopHandoffOperation)
	if len(op.id) > 64 {
		return op, fmt.Errorf("交接任务 ID 无效")
	}
	for _, ch := range op.id {
		if ch != '-' && ch != '_' && !(ch >= 'a' && ch <= 'z') && !(ch >= 'A' && ch <= 'Z') && !(ch >= '0' && ch <= '9') {
			return op, fmt.Errorf("交接任务 ID 无效")
		}
	}
	return op, nil
}

func (op sopHandoffOperation) report(phase string) error {
	if op.progress != nil {
		return op.progress(phase)
	}
	return nil
}

// CheckGroupSOPHandoffReady performs the same idle/graph validation used by the
// worker before accepting a durable handoff operation.
func (e *Engine) CheckGroupSOPHandoffReady(id, nodeID string) error {
	run, ok := e.repo.RunState(id)
	if !ok {
		return fmt.Errorf("运行不存在")
	}
	return e.checkGroupSOPHandoffReady(run, nodeID)
}

type preparedSOPHandoff struct {
	revision  int
	source    repo.SOPHandoffSource
	reference domain.GroupSOPHandoff
	artifacts []domain.Artifact
}

// RestoreGroupSOPWithHandoff freezes an idle group's public conversation before
// the graph changes. The callback only summarizes supplied data: it cannot run
// tools or mutate the workspace. Failed/stale handoffs leave the group paused.
func (e *Engine) RestoreGroupSOPWithHandoff(ctx context.Context, id, nodeID string, actorID int64, summarize func(context.Context, domain.Run, domain.AssistantSession, string) (string, error)) error {
	op, err := sopHandoffOperationFromContext(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	run, ok := e.repo.RunState(id)
	if !ok {
		return fmt.Errorf("运行不存在")
	}
	if ref := run.WorkflowSnapshot.GroupSOPHandoff; op.id != "" && ref != nil && ref.ID == op.id && run.WorkflowSnapshot.CollaborationMode != "group" {
		if ref.NodeID != nodeID {
			return fmt.Errorf("交接任务已用于其他继续节点")
		}
		return validateSOPHandoffArtifacts(run)
	}
	if err := e.checkGroupSOPHandoffReady(run, nodeID); err != nil {
		return err
	}
	if summarize == nil {
		return fmt.Errorf("Main Bot 交接服务不可用")
	}
	// Pausing blocks new member dispatches and automatic Main Bot follow-ups.
	if !run.PauseRequested {
		if err := e.Pause(id); err != nil {
			return err
		}
	}
	token, err := e.BeginConversationWorkspaceUse(id, op.id)
	if err != nil {
		return err
	}
	defer func() {
		finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer finishCancel()
		if err := e.EndConversationWorkspaceUse(finishCtx, id, token); err != nil {
			log.Printf("SOP handoff workspace release %s: %v", id, err)
		}
	}()
	if _, err := e.PrepareConversationWorkspace(ctx, id, token); err != nil {
		return err
	}
	run, ok = e.repo.RunState(id)
	if !ok {
		return fmt.Errorf("运行不存在")
	}
	if err := e.checkGroupSOPHandoffReady(run, nodeID); err != nil {
		return err
	}
	session, err := e.repo.EnsureRunAssistantSession(id, run.ExecutionContext.ProjectID, run.WorkflowSnapshot.Name, actorID)
	if err != nil {
		return err
	}
	if e.conversationGenerationRunning(run) {
		return fmt.Errorf("Main Bot 仍在回复，请等待结束后再切回 SOP")
	}
	transcript, err := e.groupSOPHandoffTranscript(run, nodeID, session)
	if err != nil {
		return err
	}
	source := repo.SOPHandoffSessionSource(session)
	summaryRun := run
	summaryRun.ExecutionContext.ResponsibleUserID = actorID
	modelTranscript, err := groupSOPHandoffModelTranscript(run, transcript)
	if err != nil {
		return err
	}
	if err := op.report("summarizing"); err != nil {
		return err
	}
	summary, err := summarize(ctx, summaryRun, session, string(modelTranscript))
	if err != nil {
		return fmt.Errorf("生成交接摘要失败，仍保留在自由协作：%w", err)
	}
	summary = strings.TrimSpace(summary)
	if summary == "" || len(summary) > 64<<10 || strings.ContainsRune(summary, 0) {
		return fmt.Errorf("交接摘要为空或过长，请重试")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, exists := e.repo.RunState(id)
	currentSession, sessionExists := e.repo.SharedAssistantSession(session.ID)
	if !exists || current.Revision != run.Revision || !sessionExists || repo.SOPHandoffSessionSource(currentSession) != source {
		return fmt.Errorf("群聊或运行已变化，请重新生成交接摘要后切回 SOP")
	}
	if err := e.checkGroupSOPHandoffReady(current, nodeID); err != nil {
		return err
	}
	handoffID := op.id
	if handoffID == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		handoffID = "sop-" + hex.EncodeToString(nonce[:])
	}
	base := "handoffs/" + handoffID + "/"
	if op.id != "" {
		// A retry after a crash may leave uncommitted create-only files. A new
		// candidate directory never overwrites those files; only the committed
		// artifact references become authoritative.
		var attempt [16]byte
		if _, err := rand.Read(attempt[:]); err != nil {
			return err
		}
		base += hex.EncodeToString(attempt[:]) + "/"
	}
	summary = "# 自由协作交接\n\n" + fmt.Sprintf("运行：%s\n\n继续节点：%s\n\n原始对话与执行事实：`%sconversation.json`（较大归档按清单分片保存，所有分片均完整保留）\n\n", id, nodeID, base) + summary + "\n"
	artifacts, err := buildSOPHandoffArtifacts(id, handoffID, summary, transcript, base)
	if err != nil {
		return err
	}
	if err := op.report("saving"); err != nil {
		return err
	}
	files := []domain.WorkspaceArtifactFile{}
	for _, a := range artifacts {
		files = append(files, domain.WorkspaceArtifactFile{Path: a.Path, Content: a.Content, SHA256: a.SHA256})
	}
	written, err := e.restoreWorkspaceFiles(ctx, run, files)
	if err != nil {
		return fmt.Errorf("保存交接文件失败，仍保留在自由协作：%w", err)
	}
	if len(written) != len(files) {
		return fmt.Errorf("交接文件未完整写入，请重试")
	}
	handoff := &preparedSOPHandoff{revision: run.Revision, source: source, artifacts: artifacts, reference: domain.GroupSOPHandoff{
		ID: handoffID, NodeID: nodeID, SummaryPath: artifacts[0].Path, TranscriptPath: artifacts[1].Path,
		SummaryArtifactID: artifacts[0].ID, TranscriptArtifactID: artifacts[1].ID, CreatedAt: time.Now().UTC(),
	}}
	return e.restoreGroupSOPWithContext(id, nodeID, false, handoff)
}

func (e *Engine) checkGroupSOPHandoffReady(run domain.Run, nodeID string) error {
	if run.WorkflowSnapshot.CollaborationMode != "group" {
		return fmt.Errorf("当前任务不是自由协作")
	}
	if run.CancelRequested || run.Status == "running" || hasRunningNode(run) || e.isRunSchedulerActive(run.ID) {
		return fmt.Errorf("请先暂停协作并等待执行停止")
	}
	tasks, err := e.repo.ActiveRunnerTasksForRun(run.ID)
	if err != nil {
		return err
	}
	if len(tasks) > 0 {
		return fmt.Errorf("执行机器仍在停止任务，请稍后再切回 SOP")
	}
	if e.workspaceTerminals.HasRunScope(run.ID) || e.WorkspaceActive != nil && e.WorkspaceActive(run) {
		return fmt.Errorf("请先关闭工作区终端，再生成交接摘要")
	}
	checkpoint, err := e.groupSOPCheckpoint(run)
	if err != nil {
		return err
	}
	if checkpoint.Nodes[nodeID] == nil {
		return fmt.Errorf("请选择原 SOP 的继续节点")
	}
	if !replayUpstreamSucceeded(domain.Run{WorkflowSnapshot: checkpoint.Workflow, Nodes: checkpoint.Nodes}, nodeID) {
		return fmt.Errorf("所选节点的上游尚未完成，请从更早节点继续")
	}
	return nil
}

type sopHandoffTask struct {
	ID, AgentID, Name, Prompt, Status, Output, Error string
	Files                                            []string
}
type sopHandoffArtifact struct {
	ID, NodeID, Name, Path, SHA256 string
	CreatedAt                      time.Time
}
type sopHandoffArchive struct {
	RunID           string                          `json:"runId"`
	Goal            string                          `json:"goal"`
	ResumeNode      string                          `json:"resumeNode"`
	HumanContext    string                          `json:"humanContext"`
	CapturedAt      time.Time                       `json:"capturedAt"`
	Conversation    []domain.GroupConversationEntry `json:"conversation"`
	Tasks           []sopHandoffTask                `json:"tasks"`
	Artifacts       []sopHandoffArtifact            `json:"artifacts"`
	PreviousHandoff string                          `json:"previousHandoff"`
	Verification    domain.GroupVerification        `json:"verification"`
	WorkspaceNotice string                          `json:"workspaceNotice"`
}

// The durable archive always keeps the full published history. The model input
// is derived separately so incremental summaries never replace source evidence.
func (e *Engine) groupSOPHandoffTranscript(run domain.Run, nodeID string, session domain.AssistantSession) ([]byte, error) {
	tasks := []sopHandoffTask{}
	for _, n := range run.WorkflowSnapshot.Nodes {
		nr := run.Nodes[n.ID]
		if nr != nil {
			tasks = append(tasks, sopHandoffTask{n.ID, n.AgentID, n.Name, n.TaskPrompt, nr.Status, nr.Output, nr.Error, n.ClaimedFiles})
		}
	}
	artifacts := []sopHandoffArtifact{}
	for _, a := range run.Artifacts {
		// Handoff metadata is represented by previousHandoff, not as new task
		// deliverables on every round.
		if a.NodeID != groupSOPHandoffNode {
			artifacts = append(artifacts, sopHandoffArtifact{a.ID, a.NodeID, a.Name, a.Path, a.SHA256, a.CreatedAt})
		}
	}
	previousSummary, _, _ := previousSOPHandoffArchive(run)
	facts, ok := e.repo.RunGroupActivityState(run.ID)
	if !ok {
		return nil, fmt.Errorf("读取执行检查记录失败")
	}
	raw, err := json.MarshalIndent(sopHandoffArchive{
		RunID: run.ID, Goal: run.Input, ResumeNode: nodeID, HumanContext: run.HumanContext,
		CapturedAt: time.Now().UTC(), Conversation: domain.ArchiveGroupConversation(run, session.Messages),
		Tasks: tasks, Artifacts: artifacts, PreviousHandoff: previousSummary,
		Verification:    domain.BuildGroupActivity(facts, nil).Receipt.Verification,
		WorkspaceNotice: "文件修改与 Git 提交状态仅以已有记录为依据；未直接核对的内容标为未核实。成员自述不等于平台检查通过。",
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	// Leave room for the summary and multipart manifest under the existing
	// Runner restore_artifacts total limit. No data is silently truncated.
	if len(raw) > runtime.WorkspaceCheckpointMaxBytes-(96<<10) {
		return nil, fmt.Errorf("完整群聊归档超过 12 MB 传输上限，暂时无法交接；原记录已保留，未截断或切回 SOP")
	}
	return raw, nil
}

func previousSOPHandoffArchive(run domain.Run) (string, sopHandoffArchive, bool) {
	var previous sopHandoffArchive
	ref := run.WorkflowSnapshot.GroupSOPHandoff
	if ref == nil {
		return "", previous, false
	}
	var summary string
	for _, a := range run.Artifacts {
		if a.ID == ref.SummaryArtifactID && a.Path == ref.SummaryPath && a.Name == "sop-handoff" && a.NodeID == groupSOPHandoffNode && a.SHA256 == runtime.WorkspaceContentSHA(a.Content) {
			summary = a.Content
		}
	}
	if strings.TrimSpace(summary) == "" {
		return "", previous, false
	}
	selected, err := archivedSOPHandoffArtifacts(run)
	if err != nil {
		return summary, previous, false
	}
	raw, err := readSOPHandoffTranscript(selected)
	if err != nil || json.Unmarshal(raw, &previous) != nil || previous.RunID != run.ID || previous.CapturedAt.IsZero() {
		return summary, previous, false
	}
	return summary, previous, true
}

// A previous immutable transcript is the capture boundary, rather than a wall
// clock: late-arriving events and messages with old timestamps are still new.
// Edited/removed historical speech invalidates that boundary and falls back to
// full context so old constraints cannot silently disappear.
func groupSOPHandoffModelTranscript(run domain.Run, raw []byte) ([]byte, error) {
	var current sopHandoffArchive
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil, err
	}
	fallback := func() ([]byte, error) {
		// An edited or unverifiable source invalidates its derived summary too.
		// Rebuild from current public history instead of resurrecting deleted decisions.
		if current.PreviousHandoff == "" {
			return raw, nil
		}
		current.PreviousHandoff = ""
		current.WorkspaceNotice += " 上一版交接来源已变化或无法校验，本轮以当前完整公开记录重新整理，不沿用旧摘要。"
		return json.Marshal(current)
	}
	summary, previous, ok := previousSOPHandoffArchive(run)
	if !ok {
		return fallback()
	}
	oldEntries := map[string]domain.GroupConversationEntry{}
	for _, entry := range previous.Conversation {
		if entry.ID == "" {
			return fallback()
		}
		if _, duplicate := oldEntries[entry.ID]; duplicate {
			return fallback()
		}
		oldEntries[entry.ID] = entry
	}
	seen := map[string]bool{}
	added := []domain.GroupConversationEntry{}
	for _, entry := range current.Conversation {
		if entry.ID == "" || seen[entry.ID] {
			return fallback()
		}
		seen[entry.ID] = true
		if old, exists := oldEntries[entry.ID]; exists {
			if !sopHandoffRecordsEqual(old, entry) {
				return fallback()
			}
		} else {
			added = append(added, entry)
		}
	}
	for id := range oldEntries {
		if !seen[id] {
			return fallback()
		}
	}
	tasks, removedTasks := changedSOPHandoffRecords(current.Tasks, previous.Tasks, func(t sopHandoffTask) string { return t.ID })
	artifacts, removedArtifacts := changedSOPHandoffRecords(current.Artifacts, previous.Artifacts, func(a sopHandoffArtifact) string { return a.ID })
	checks, removedChecks := changedSOPHandoffRecords(current.Verification.Checks, previous.Verification.Checks, func(c domain.GroupVerificationCheck) string { return c.ID })
	verification := current.Verification
	verification.Checks = checks
	return json.Marshal(map[string]any{
		"runId": current.RunID, "goal": current.Goal, "resumeNode": current.ResumeNode, "humanContext": current.HumanContext,
		"capturedAt": current.CapturedAt, "contextMode": "incremental", "previousCapturedAt": previous.CapturedAt,
		"previousHandoffId": run.WorkflowSnapshot.GroupSOPHandoff.ID, "previousHandoff": summary,
		"conversation": added, "tasks": tasks, "artifacts": artifacts, "verification": verification,
		"removedTaskIds": removedTasks, "removedArtifactIds": removedArtifacts, "removedVerificationIds": removedChecks,
		"fullConversationCount": len(current.Conversation), "workspaceNotice": current.WorkspaceNotice,
		"incrementalNotice": "以上一版交接摘要为完整基础，仅 conversation/tasks/artifacts/checks 是其后新增或变更的记录。必须保留仍有效的早期约束与未完成事项；本轮无新增证据不代表历史检查仍有效，验证有效性以当前 verification.status/reason 为准；removed 列表中的记录不得继续当作当前证据。输出完整更新后的交接摘要。",
	})
}

func sopHandoffRecordsEqual(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}
func changedSOPHandoffRecords[T any](current, previous []T, key func(T) string) ([]T, []string) {
	prior := map[string]T{}
	for _, record := range previous {
		prior[key(record)] = record
	}
	changed := []T{}
	for _, record := range current {
		id := key(record)
		old, exists := prior[id]
		if !exists || !sopHandoffRecordsEqual(old, record) {
			changed = append(changed, record)
		}
		delete(prior, id)
	}
	removed := []string{}
	for id := range prior {
		removed = append(removed, id)
	}
	sort.Strings(removed)
	return changed, removed
}

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

func buildSOPHandoffArtifacts(runID, handoffID, summary string, transcript []byte, candidateBase ...string) ([]domain.Artifact, error) {
	base := "handoffs/" + handoffID + "/"
	if len(candidateBase) > 0 {
		base = candidateBase[0]
	}
	artifacts := []domain.Artifact{newArtifact(runID, groupSOPHandoffNode, 0, "sop-handoff", base+"handoff.md", "text/markdown", []byte(summary))}
	root := transcript
	var parts []domain.Artifact
	if len(transcript) > maxArtifactBytes {
		manifest := sopHandoffTranscriptManifest{Format: sopHandoffTranscriptFormat, Bytes: len(transcript), SHA256: runtime.WorkspaceContentSHA(string(transcript))}
		for offset := 0; offset < len(transcript); {
			end := min(offset+maxArtifactBytes, len(transcript))
			for end < len(transcript) && !utf8.RuneStart(transcript[end]) {
				end--
			}
			name := fmt.Sprintf("sop-handoff-conversation-part-%03d", len(parts)+1)
			rel := fmt.Sprintf("%sconversation.part-%03d.txt", base, len(parts)+1)
			part := newArtifact(runID, groupSOPHandoffNode, 0, name, rel, "text/plain", transcript[offset:end])
			parts = append(parts, part)
			manifest.Parts = append(manifest.Parts, sopHandoffTranscriptPart{Name: name, Path: rel, InputPath: sopHandoffInputPath(&domain.GroupSOPHandoff{ID: handoffID}, name), SHA256: part.SHA256})
			offset = end
		}
		var err error
		root, err = json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return nil, err
		}
	}
	artifacts = append(artifacts, newArtifact(runID, groupSOPHandoffNode, 0, "sop-handoff-conversation", base+"conversation.json", "application/json", root))
	artifacts = append(artifacts, parts...)
	total := 0
	for _, artifact := range artifacts {
		total += len(artifact.Content)
		if len(artifact.Content) > maxArtifactBytes || total > runtime.WorkspaceCheckpointMaxBytes {
			return nil, fmt.Errorf("完整交接归档超过工作区传输上限；原记录已保留，未截断或切回 SOP")
		}
	}
	return artifacts, nil
}

// archivedSOPHandoffArtifacts validates both legacy single-file archives and
// multipart manifests. Parts remain ordinary artifact input files understood by
// existing local/Runner transports; no protocol or Runner upgrade is required.
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
