package engine

import (
	"bytes"
	"fmt"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func (e *Engine) requirementAttachments(run domain.Run) ([]repo.IssueAttachment, error) {
	issueID := run.ExecutionContext.IssueID
	if issueID == "" {
		return nil, nil
	}
	issue, ok := e.repo.Issue(issueID)
	if !ok || domain.RunProjectID(run, issue.ProjectID) != issue.ProjectID {
		return nil, nil
	}
	return e.repo.IssueAttachments(issueID)
}

func (e *Engine) materializeRequirementAttachments(workspace, issueID string, files []repo.IssueAttachment) error {
	for _, attachment := range files {
		file, err := e.repo.IssueAttachment(issueID, attachment.ID)
		if err != nil {
			return fmt.Errorf("读取需求附件 %q：%w", attachment.Name, err)
		}
		if err := runtime.WriteArtifactInput(workspace, attachment.InputPath(), "", bytes.NewReader(file.Data)); err != nil {
			return fmt.Errorf("写入需求附件 %q：%w", attachment.Name, err)
		}
	}
	return nil
}

func runnerRequirementInputs(files []repo.IssueAttachment) []domain.RunnerInputFile {
	var inputs []domain.RunnerInputFile
	for _, file := range files {
		inputs = append(inputs, domain.RunnerInputFile{Path: file.InputPath(), ArtifactID: repo.IssueAttachmentInputPrefix + file.ID, Image: file.IsImage()})
	}
	return inputs
}

func requirementImagePaths(files []repo.IssueAttachment) []string {
	var paths []string
	for _, file := range files {
		if file.IsImage() {
			paths = append(paths, file.InputPath())
		}
	}
	return paths
}

func requirementAttachmentPrompt(files []repo.IssueAttachment, executor string) string {
	if len(files) == 0 {
		return ""
	}
	var prompt strings.Builder
	prompt.WriteString("\n\n## 需求附件\n\n请先查阅相关附件，按需求资料理解其中的内容。路径相对于运行工作区根目录。\n")
	for _, file := range files {
		fmt.Fprintf(&prompt, "- %q：%q", file.Name, file.InputPath())
		if file.IsImage() {
			tool := "图片读取工具"
			switch executor {
			case runtime.AgentCLIDeepSeek:
				tool = "read_image"
			case runtime.AgentCLIKimi:
				tool = "ReadMediaFile"
			case "codebuddy", "claude-code":
				tool = "Read"
			}
			fmt.Fprintf(&prompt, "。请使用 %s 查看图片。", tool)
		}
		prompt.WriteByte('\n')
	}
	return prompt.String()
}
