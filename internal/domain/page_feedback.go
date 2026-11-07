package domain

import "time"

// PageFeedback is one report submitted from the floating page feedback
// widget. It belongs to no project or issue; the submitter fields are always
// copied from the server-side session, never from the request body.
type PageFeedback struct {
	ID          string               `json:"id"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	URL         string               `json:"url"`
	Route       string               `json:"route"`
	View        string               `json:"view,omitempty"`
	FrameURL    string               `json:"frameUrl,omitempty"`
	Context     PageFeedbackContext  `json:"context"`
	Element     *PageFeedbackElement `json:"element,omitempty"`
	Submitter   PageFeedbackUser     `json:"submitter"`
	Images      []PageFeedbackImage  `json:"images"`
	CreatedAt   time.Time            `json:"createdAt"`
}

// PageFeedbackUser is the submitter snapshot. ChineseName is frozen at submit
// time and falls back to LoginName (the employee RTX) when the profile has none.
type PageFeedbackUser struct {
	UserID      int64  `json:"userId"`
	LoginName   string `json:"loginName"`
	ChineseName string `json:"chineseName"`
}

// PageFeedbackContext is the environment captured when the card was opened.
type PageFeedbackContext struct {
	Scroll     PageFeedbackPoint `json:"scroll"`
	Viewport   PageFeedbackSize  `json:"viewport"`
	DPR        float64           `json:"dpr,omitempty"`
	UserAgent  string            `json:"userAgent,omitempty"`
	Language   string            `json:"language,omitempty"`
	Theme      string            `json:"theme,omitempty"`
	CapturedAt string            `json:"capturedAt,omitempty"`
}

// PageFeedbackElement is the optional element the user picked on the page.
type PageFeedbackElement struct {
	Selector string            `json:"selector"`
	Tag      string            `json:"tag,omitempty"`
	Text     string            `json:"text,omitempty"`
	Rect     PageFeedbackRect  `json:"rect"`
	PageRect PageFeedbackRect  `json:"pageRect"`
	Point    PageFeedbackPoint `json:"point"`
	InFrame  bool              `json:"inFrame"`
}

type PageFeedbackPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type PageFeedbackSize struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type PageFeedbackRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// PageFeedbackImage is an attached image. Data is only populated when the
// image itself is requested; listings carry metadata and the download URL.
type PageFeedbackImage struct {
	ID         string    `json:"id"`
	FeedbackID string    `json:"-"`
	Position   int       `json:"position"`
	FileName   string    `json:"fileName"`
	MimeType   string    `json:"mimeType"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	URL        string    `json:"url,omitempty"`
	Data       []byte    `json:"-"`
	CreatedAt  time.Time `json:"createdAt"`
}
