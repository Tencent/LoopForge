package httpapi

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

// The browser submits upload IDs; names, sizes and uploader identities always
// come from this account's project-scoped attachment records.
func (h issuesHandlers) canonicalIssueAttachments(c *gin.Context, issue *domain.Issue) error {
	if len(issue.Attachments) == 0 {
		return nil
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		return fmt.Errorf("请先登录后上传需求附件")
	}
	if len(issue.Attachments) > workbenchAttachmentCount {
		return fmt.Errorf("每张需求最多附加 8 个文件")
	}
	seen := map[string]bool{}
	for i, a := range issue.Attachments {
		if seen[a.ID] {
			return fmt.Errorf("不能重复附加同一文件")
		}
		seen[a.ID] = true
		record, file, err := openWorkbenchAttachment(user.ID, a.ID)
		if err != nil {
			return fmt.Errorf("附件不存在或不属于当前账号，请重新上传")
		}
		info, statErr := file.Stat()
		file.Close()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > workbenchAttachmentLimit || record.ProjectID != issue.ProjectID {
			return fmt.Errorf("附件不属于当前项目或大小无效，请重新上传")
		}
		issue.Attachments[i] = domain.IssueAttachment{AssistantAttachment: record.AssistantAttachment, UploadedBy: user.ID}
	}
	if strings.TrimSpace(issue.Title) == "" {
		issue.Title = issue.Attachments[0].Name
	}
	if strings.TrimSpace(issue.Description) == "" {
		issue.Description = "本需求以附件正文为准。请先读取全部附件，明确需求范围、约束和验收标准；无法读取或有歧义时先澄清。"
	}
	return nil
}
