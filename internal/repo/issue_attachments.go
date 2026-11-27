package repo

import (
	"agentflow/internal/domain"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IssueAttachment struct {
	ID        string `gorm:"primaryKey;size:32" json:"id"`
	Name      string `gorm:"size:255" json:"name"`
	MediaType string `gorm:"size:32" json:"mediaType"`
	Data      []byte `gorm:"type:mediumblob" json:"-"`
}

// IssueAttachmentInputPrefix distinguishes issue files from run output
// artifacts in the Runner's existing task input manifest.
const IssueAttachmentInputPrefix = "issue-attachment:"

func (a IssueAttachment) InputPath() string {
	return path.Join(".agentflow/inputs/requirement-attachments", a.ID, IssueAttachmentName(a.Name))
}

func (a IssueAttachment) IsImage() bool {
	switch a.MediaType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	}
	return false
}

func IssueAttachmentName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Trim(strings.Map(func(ch rune) rune {
		if unicode.IsControl(ch) || strings.ContainsRune(`<>:"|?*`, ch) {
			return '_'
		}
		return ch
	}, name), " .")
	if name == "" || name == "/" {
		return "attachment"
	}
	maxBytes := 240
	if len(name) > maxBytes {
		// Keep ordinary extensions for tools that use them to identify formats.
		ext := path.Ext(name)
		if len(ext) <= 16 {
			maxBytes -= len(ext)
		} else {
			ext = ""
		}
		for !utf8.RuneStart(name[maxBytes]) {
			maxBytes--
		}
		name = name[:maxBytes] + ext
	}
	return name
}

type issueAttachmentRow struct {
	IssueID         string `gorm:"primaryKey;size:64"`
	IssueAttachment `gorm:"embedded"`
	Position        int
}

// Keep uploaded images from existing installations in the same table.
func (issueAttachmentRow) TableName() string { return "issue_images" }

func (r *IssuesRepo) IssueAttachments(issueID string) ([]IssueAttachment, error) {
	var attachments []IssueAttachment
	err := r.DB.Model(&issueAttachmentRow{}).Select("id", "name", "media_type").Where("issue_id = ?", issueID).Order("position").Find(&attachments).Error
	return attachments, err
}

func (r *IssuesRepo) IssueAttachment(issueID, attachmentID string) (IssueAttachment, error) {
	var row issueAttachmentRow
	err := r.DB.Where("issue_id = ? AND id = ?", issueID, attachmentID).Take(&row).Error
	return row.IssueAttachment, err
}

func (r *IssuesRepo) CreateIssueWithAttachments(issue domain.Issue, attachments []IssueAttachment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	issue, err := r.prepareIssueForSave(issue, false, false)
	if err != nil {
		return err
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("issues").Create(issueValues(issue)).Error; err != nil {
			return err
		}
		for position, attachment := range attachments {
			row := issueAttachmentRow{IssueID: issue.ID, IssueAttachment: attachment, Position: position}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// PutIssueWithCopiedAttachments saves the tracker issue before its local predecessor
// is removed. If the predecessor has execution history, both issues keep their attachments.
func (r *IssuesRepo) PutIssueWithCopiedAttachments(issue domain.Issue, previousID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	issue, err := r.prepareIssueForSave(issue, false, false)
	if err != nil {
		return err
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := upsert(tx, "issues", issueValues(issue)); err != nil {
			return err
		}
		var attachments []issueAttachmentRow
		if err := tx.Where("issue_id = ?", previousID).Find(&attachments).Error; err != nil {
			return err
		}
		for _, attachment := range attachments {
			attachment.IssueID = issue.ID
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&attachment).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
