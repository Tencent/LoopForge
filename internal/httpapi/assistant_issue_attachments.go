package httpapi

import (
	"fmt"

	"github.com/gin-gonic/gin"
)

// Read only the stored issue's references. The query parameter cannot choose
// an arbitrary attachment owner or grant access to another project's files.
func (h *assistantHandlers) issueAttachmentOwner(c *gin.Context, issueID, id string, userID int64) (int64, error) {
	issue, ok := h.repo.Issue(issueID)
	if !ok {
		return 0, fmt.Errorf("需求不存在")
	}
	if !h.workbenchProjectAllowed(c, issue.ProjectID, userID) {
		return 0, fmt.Errorf("无权访问该项目")
	}
	for _, a := range issue.Attachments {
		if a.ID == id {
			return a.UploadedBy, nil
		}
	}
	return 0, fmt.Errorf("附件不属于该需求")
}

func (h *assistantHandlers) addIssueAttachments(req *assistantRequest, userID int64) error {
	issueID := req.linkedIssueID
	if s, ok := h.repo.AssistantSession(assistantStorageOwner(h.repo, req.SessionID, userID), req.SessionID); ok {
		issueID = s.IssueID
	}
	refs := lastMessageReferences(req.Messages)
	for _, issue := range turnIssues(h.deps, req.ProjectID, issueID, refs) {
		for _, a := range issue.Attachments {
			if req.attachmentIDs[a.ID] {
				continue
			}
			record, file, err := openWorkbenchAttachment(a.UploadedBy, a.ID)
			if err != nil {
				return fmt.Errorf("需求附件 %q 已不可用，请重新上传", a.Name)
			}
			info, statErr := file.Stat()
			file.Close()
			if statErr != nil || !info.Mode().IsRegular() || info.Size() > workbenchAttachmentLimit || record.ProjectID != issue.ProjectID {
				return fmt.Errorf("需求附件 %q 无效", a.Name)
			}
			req.attachmentIDs[a.ID] = true
			req.attachmentOwners[a.ID] = a.UploadedBy
			req.issueAttachments = append(req.issueAttachments, record.AssistantAttachment)
		}
	}
	return nil
}
