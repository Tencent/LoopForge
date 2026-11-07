package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

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
