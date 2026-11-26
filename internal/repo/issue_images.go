package repo

import (
	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IssueImage struct {
	ID        string `gorm:"primaryKey;size:32" json:"id"`
	Name      string `gorm:"size:255" json:"name"`
	MediaType string `gorm:"size:32" json:"-"`
	Data      []byte `gorm:"type:mediumblob" json:"-"`
}

type issueImageRow struct {
	IssueID    string `gorm:"primaryKey;size:64"`
	IssueImage `gorm:"embedded"`
	Position   int
}

func (issueImageRow) TableName() string { return "issue_images" }

func (r *IssuesRepo) IssueImages(issueID string) ([]IssueImage, error) {
	var images []IssueImage
	err := r.DB.Model(&issueImageRow{}).Select("id", "name").Where("issue_id = ?", issueID).Order("position").Find(&images).Error
	return images, err
}

func (r *IssuesRepo) IssueImage(issueID, imageID string) (IssueImage, error) {
	var row issueImageRow
	err := r.DB.Where("issue_id = ? AND id = ?", issueID, imageID).Take(&row).Error
	return row.IssueImage, err
}

func (r *IssuesRepo) CreateIssueWithImages(issue domain.Issue, images []IssueImage) error {
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
		for position, image := range images {
			row := issueImageRow{IssueID: issue.ID, IssueImage: image, Position: position}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// PutIssueWithCopiedImages saves the tracker issue before its local predecessor
// is removed. If the predecessor has execution history, both issues keep their images.
func (r *IssuesRepo) PutIssueWithCopiedImages(issue domain.Issue, previousID string) error {
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
		var images []issueImageRow
		if err := tx.Where("issue_id = ?", previousID).Find(&images).Error; err != nil {
			return err
		}
		for _, image := range images {
			image.IssueID = issue.ID
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&image).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
