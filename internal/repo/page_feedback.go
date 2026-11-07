package repo

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

// FeedbackRepo owns page feedback reports and their images. The tables are
// standalone: no foreign keys to projects, issues or users.
type FeedbackRepo struct {
	*deps
}

type pageFeedbackRow struct {
	ID          string     `gorm:"column:id;primaryKey;size:64"`
	Title       string     `gorm:"column:title;size:255"`
	Description string     `gorm:"column:description;type:text"`
	URL         string     `gorm:"column:url;size:2048"`
	Route       string     `gorm:"column:route;size:512"`
	View        string     `gorm:"column:view;size:128"`
	FrameURL    string     `gorm:"column:frame_url;size:2048"`
	Context     []byte     `gorm:"column:context;type:json"`
	Element     []byte     `gorm:"column:element;type:json"`
	UserID      int64      `gorm:"column:user_id"`
	LoginName   string     `gorm:"column:login_name;size:128;index:idx_page_feedbacks_login"`
	ChineseName string     `gorm:"column:chinese_name;size:128"`
	Created     *time.Time `gorm:"column:created_at;index:idx_page_feedbacks_created"`
}

func (pageFeedbackRow) TableName() string { return "page_feedbacks" }

type pageFeedbackImageRow struct {
	ID         string     `gorm:"column:id;primaryKey;size:64"`
	FeedbackID string     `gorm:"column:feedback_id;size:64;index:idx_page_feedback_images_feedback"`
	Position   int        `gorm:"column:position"`
	FileName   string     `gorm:"column:file_name;size:255"`
	MimeType   string     `gorm:"column:mime_type;size:64"`
	Size       int64      `gorm:"column:size"`
	SHA256     string     `gorm:"column:sha256;size:64"`
	Data       []byte     `gorm:"column:data;type:longblob"`
	Created    *time.Time `gorm:"column:created_at"`
}

func (pageFeedbackImageRow) TableName() string { return "page_feedback_images" }

// imageMetaColumns is every image column except the binary payload.
var imageMetaColumns = []string{"id", "feedback_id", "position", "file_name", "mime_type", "size", "sha256", "created_at"}

func newFeedbackID(prefix string) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("%s-%d%s", prefix, time.Now().UTC().UnixNano(), hex.EncodeToString(buf))
}

// CreatePageFeedback writes the report and every image in one transaction:
// either all rows land or none do. Missing IDs and timestamps are filled in
// and the stored feedback (without image data) is returned.
func (r *FeedbackRepo) CreatePageFeedback(fb domain.PageFeedback, images []domain.PageFeedbackImage) (domain.PageFeedback, error) {
	if strings.TrimSpace(fb.Title) == "" {
		return domain.PageFeedback{}, errors.New("page feedback title is required")
	}
	if strings.TrimSpace(fb.ID) == "" {
		fb.ID = newFeedbackID("fb")
	}
	if fb.CreatedAt.IsZero() {
		fb.CreatedAt = time.Now().UTC()
	}
	created := fb.CreatedAt.UTC()
	contextJSON, err := json.Marshal(fb.Context)
	if err != nil {
		return domain.PageFeedback{}, err
	}
	var elementJSON []byte
	if fb.Element != nil {
		if elementJSON, err = json.Marshal(fb.Element); err != nil {
			return domain.PageFeedback{}, err
		}
	}
	row := pageFeedbackRow{
		ID: fb.ID, Title: fb.Title, Description: fb.Description, URL: fb.URL, Route: fb.Route,
		View: fb.View, FrameURL: fb.FrameURL, Context: contextJSON, Element: elementJSON,
		UserID: fb.Submitter.UserID, LoginName: fb.Submitter.LoginName, ChineseName: fb.Submitter.ChineseName,
		Created: &created,
	}
	imageRows := make([]pageFeedbackImageRow, 0, len(images))
	fb.Images = make([]domain.PageFeedbackImage, 0, len(images))
	for i, img := range images {
		if strings.TrimSpace(img.ID) == "" {
			img.ID = newFeedbackID("fbi")
		}
		img.FeedbackID = fb.ID
		img.Position = i
		img.CreatedAt = created
		imageRows = append(imageRows, pageFeedbackImageRow{
			ID: img.ID, FeedbackID: fb.ID, Position: i, FileName: img.FileName, MimeType: img.MimeType,
			Size: img.Size, SHA256: img.SHA256, Data: img.Data, Created: &created,
		})
		img.Data = nil
		fb.Images = append(fb.Images, img)
	}
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		for i := range imageRows {
			if err := tx.Create(&imageRows[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.PageFeedback{}, err
	}
	return fb, nil
}

// PageFeedbacks lists reports newest first with image metadata only.
func (r *FeedbackRepo) PageFeedbacks(limit, offset int) ([]domain.PageFeedback, int64, error) {
	out := make([]domain.PageFeedback, 0)
	var total int64
	if err := r.DB.Model(&pageFeedbackRow{}).Count(&total).Error; err != nil {
		return out, 0, err
	}
	var rows []pageFeedbackRow
	if err := r.DB.Order("created_at DESC").Order("id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return out, 0, err
	}
	if len(rows) == 0 {
		return out, total, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	images, err := r.pageFeedbackImageMeta(ids)
	if err != nil {
		return out, 0, err
	}
	for _, row := range rows {
		fb := pageFeedbackFromRow(row)
		fb.Images = images[row.ID]
		if fb.Images == nil {
			fb.Images = []domain.PageFeedbackImage{}
		}
		out = append(out, fb)
	}
	return out, total, nil
}

// PageFeedback returns one report with image metadata.
func (r *FeedbackRepo) PageFeedback(id string) (domain.PageFeedback, bool, error) {
	var row pageFeedbackRow
	err := r.DB.Where("id = ?", strings.TrimSpace(id)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.PageFeedback{}, false, nil
	}
	if err != nil {
		return domain.PageFeedback{}, false, err
	}
	images, err := r.pageFeedbackImageMeta([]string{row.ID})
	if err != nil {
		return domain.PageFeedback{}, false, err
	}
	fb := pageFeedbackFromRow(row)
	fb.Images = images[row.ID]
	if fb.Images == nil {
		fb.Images = []domain.PageFeedbackImage{}
	}
	return fb, true, nil
}

// PageFeedbackImage returns one image including its data, only when it
// belongs to the given feedback.
func (r *FeedbackRepo) PageFeedbackImage(feedbackID, imageID string) (domain.PageFeedbackImage, bool, error) {
	var row pageFeedbackImageRow
	err := r.DB.Where("id = ? AND feedback_id = ?", strings.TrimSpace(imageID), strings.TrimSpace(feedbackID)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.PageFeedbackImage{}, false, nil
	}
	if err != nil {
		return domain.PageFeedbackImage{}, false, err
	}
	img := pageFeedbackImageFromRow(row)
	img.Data = row.Data
	return img, true, nil
}

func (r *FeedbackRepo) pageFeedbackImageMeta(feedbackIDs []string) (map[string][]domain.PageFeedbackImage, error) {
	var rows []pageFeedbackImageRow
	if err := r.DB.Select(imageMetaColumns).Where("feedback_id IN ?", feedbackIDs).Order("position ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string][]domain.PageFeedbackImage{}
	for _, row := range rows {
		out[row.FeedbackID] = append(out[row.FeedbackID], pageFeedbackImageFromRow(row))
	}
	return out, nil
}

func pageFeedbackFromRow(row pageFeedbackRow) domain.PageFeedback {
	fb := domain.PageFeedback{
		ID: row.ID, Title: row.Title, Description: row.Description, URL: row.URL, Route: row.Route,
		View: row.View, FrameURL: row.FrameURL,
		Submitter: domain.PageFeedbackUser{UserID: row.UserID, LoginName: row.LoginName, ChineseName: row.ChineseName},
	}
	if len(row.Context) > 0 {
		_ = json.Unmarshal(row.Context, &fb.Context)
	}
	if len(row.Element) > 0 && string(row.Element) != "null" {
		var el domain.PageFeedbackElement
		if json.Unmarshal(row.Element, &el) == nil {
			fb.Element = &el
		}
	}
	if row.Created != nil {
		fb.CreatedAt = row.Created.UTC()
	}
	return fb
}

func pageFeedbackImageFromRow(row pageFeedbackImageRow) domain.PageFeedbackImage {
	img := domain.PageFeedbackImage{
		ID: row.ID, FeedbackID: row.FeedbackID, Position: row.Position, FileName: row.FileName,
		MimeType: row.MimeType, Size: row.Size, SHA256: row.SHA256,
	}
	if row.Created != nil {
		img.CreatedAt = row.Created.UTC()
	}
	return img
}
