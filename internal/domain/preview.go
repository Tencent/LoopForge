package domain

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ErrInvalidPreviewBackendURL is returned when a preview session backend URL
// is not a credential-free http(s) origin.
var ErrInvalidPreviewBackendURL = errors.New("preview backendUrl must be an http(s) URL without credentials")

const (
	PreviewKindStatic = "static"

	PreviewPending   = "pending"
	PreviewArchiving = "archiving"
	PreviewReady     = "ready"
	PreviewSkipped   = "skipped"
	PreviewFailed    = "failed"
	PreviewExpired   = "expired"
	PreviewDeleting  = "deleting"
	PreviewDeleted   = "deleted"

	PreviewBuildNodeID     = "af-preview-build"
	PreviewBuildAgentID    = "af-preview-builder"
	PreviewBuildExecutor   = "codebuddy"
	PreviewDefaultModel    = "default"
	PreviewDefaultSource   = "dist"
	PreviewBuildEdgeID     = "af-preview-build-edge"
	PreviewMaxRetentionSec = int64(30 * 24 * 3600)
)

// PreviewContract is the run-level delivery contract frozen into
// WorkflowSnapshot. Enabling it injects a CodeBuddy build node. The static
// site is archived when that node succeeds, so a later confirmation can open
// the preview before the run itself finishes.
type PreviewContract struct {
	Enabled      bool   `json:"enabled"`
	Kind         string `json:"kind"`                 // static; later service/environment
	ExecutorID   string `json:"executorId,omitempty"` // currently only codebuddy
	Model        string `json:"model,omitempty"`      // CodeBuddy model id
	SourcePath   string `json:"sourcePath,omitempty"` // relative to the run workspace; default dist
	IndexFile    string `json:"indexFile,omitempty"`  // default index.html
	SPAFallback  bool   `json:"spaFallback,omitempty"`
	Required     bool   `json:"required,omitempty"`
	CaptureShot  bool   `json:"captureScreenshot,omitempty"`
	RetentionSec int64  `json:"retentionSec,omitempty"`
}

// Static reports whether this contract asks the MVP static finalizer to run.
func (c *PreviewContract) Static() bool {
	if c == nil || !c.Enabled {
		return false
	}
	kind := strings.TrimSpace(c.Kind)
	return kind == "" || strings.EqualFold(kind, PreviewKindStatic)
}

func (c *PreviewContract) Index() string {
	if c == nil {
		return "index.html"
	}
	if name := strings.TrimSpace(c.IndexFile); name != "" {
		return name
	}
	return "index.html"
}

func (c *PreviewContract) KindOrStatic() string {
	if c == nil || strings.TrimSpace(c.Kind) == "" {
		return PreviewKindStatic
	}
	return strings.TrimSpace(c.Kind)
}

func (c *PreviewContract) SourceOrDist() string {
	if c == nil {
		return PreviewDefaultSource
	}
	if path := strings.TrimSpace(c.SourcePath); path != "" {
		return strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	}
	return PreviewDefaultSource
}

func (c *PreviewContract) ExecutorOrCodeBuddy() string {
	if c == nil {
		return PreviewBuildExecutor
	}
	if id := strings.TrimSpace(c.ExecutorID); id != "" {
		return id
	}
	return PreviewBuildExecutor
}

func (c *PreviewContract) ModelOrDefault() string {
	if c == nil {
		return PreviewDefaultModel
	}
	if model := strings.TrimSpace(c.Model); model != "" {
		return model
	}
	return PreviewDefaultModel
}

func (c *PreviewContract) Retention() time.Duration {
	if c == nil {
		return time.Duration(PreviewMaxRetentionSec) * time.Second
	}
	sec := c.RetentionSec
	if sec <= 0 || sec > PreviewMaxRetentionSec {
		sec = PreviewMaxRetentionSec
	}
	return time.Duration(sec) * time.Second
}

func (c *PreviewContract) EntryRel() string {
	return strings.Trim(c.SourceOrDist()+"/"+c.Index(), "/")
}

func (c *PreviewContract) Normalize() {
	if c == nil || !c.Enabled {
		return
	}
	c.Kind = c.KindOrStatic()
	c.ExecutorID = c.ExecutorOrCodeBuddy()
	c.Model = c.ModelOrDefault()
	c.SourcePath = c.SourceOrDist()
	c.IndexFile = c.Index()
	if c.RetentionSec <= 0 || c.RetentionSec > PreviewMaxRetentionSec {
		c.RetentionSec = PreviewMaxRetentionSec
	}
}

func (c *PreviewContract) Validate() error {
	if c == nil || !c.Enabled {
		return nil
	}
	c.Normalize()
	if !c.Static() {
		return fmt.Errorf("frontend preview currently supports static sites only")
	}
	if !strings.EqualFold(c.ExecutorID, PreviewBuildExecutor) {
		return fmt.Errorf("frontend preview currently supports CodeBuddy CLI only")
	}
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("frontend preview requires a CodeBuddy model")
	}
	if strings.Contains(c.SourcePath, "..") || strings.HasPrefix(c.SourcePath, "/") {
		return fmt.Errorf("frontend preview sourcePath %q is invalid", c.SourcePath)
	}
	return nil
}

func IsPreviewBuildNode(id string) bool {
	return strings.TrimSpace(id) == PreviewBuildNodeID
}

func IsPreviewBuildAgent(id string) bool {
	id = strings.TrimSpace(id)
	return id == PreviewBuildAgentID || id == PreviewBuildNodeID
}

// NormalizePreviewBackendURL accepts an empty value or an http(s) URL. The
// URL is chosen when opening a preview, not stored on the SOP.
func NormalizePreviewBackendURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", ErrInvalidPreviewBackendURL
	}
	u.Fragment = ""
	return u.String(), nil
}

// RunPreview is the durable metadata for a delivered frontend preview. The
// binary bundle lives in PreviewStore, never in run_artifacts.content.
type RunPreview struct {
	ID                string    `json:"id"`
	RunID             string    `json:"runId"`
	ProjectID         string    `json:"projectId,omitempty"`
	Kind              string    `json:"kind"`
	Status            string    `json:"status"`
	SourcePath        string    `json:"sourcePath,omitempty"`
	StorageKey        string    `json:"storageKey,omitempty"`
	ContentSHA256     string    `json:"contentSha256,omitempty"`
	CompressedSize    int64     `json:"compressedSize,omitempty"`
	ExpandedSize      int64     `json:"expandedSize,omitempty"`
	FileCount         int       `json:"fileCount,omitempty"`
	EntryFile         string    `json:"entryFile,omitempty"`
	SPAFallback       bool      `json:"spaFallback,omitempty"`
	BackendURL        string    `json:"backendUrl,omitempty"`
	OwnerRunnerID     string    `json:"ownerRunnerId,omitempty"`
	ScreenshotKey     string    `json:"screenshotKey,omitempty"`
	ErrorText         string    `json:"errorText,omitempty"`
	UploadTokenHash   string    `json:"-"`
	UploadMaxBytes    int64     `json:"-"`
	UploadExpiresAt   time.Time `json:"-"`
	ArchiveLeaseUntil time.Time `json:"-"`
	LastAccessedAt    time.Time `json:"lastAccessedAt,omitempty"`
	ExpiresAt         time.Time `json:"expiresAt,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func (p RunPreview) ActiveHold() bool {
	switch p.Status {
	case PreviewPending, PreviewArchiving:
		return true
	default:
		return false
	}
}

func (p RunPreview) Serveable() bool {
	return p.Status == PreviewReady && strings.TrimSpace(p.StorageKey) != ""
}

// PreviewSession is a short-lived access grant for /preview/{token}/.
// The database stores only the token hash. BackendURL is chosen when the
// session is created so Studio can point the preview at a backend without
// baking it into the SOP.
type PreviewSession struct {
	ID             string    `json:"id"`
	PreviewID      string    `json:"previewId"`
	TokenHash      string    `json:"-"`
	BackendURL     string    `json:"backendUrl,omitempty"`
	ExpiresAt      time.Time `json:"expiresAt"`
	CreatedAt      time.Time `json:"createdAt"`
	LastAccessedAt time.Time `json:"lastAccessedAt,omitempty"`
	RevokedAt      time.Time `json:"revokedAt,omitempty"`
}

func (s PreviewSession) Live(now time.Time) bool {
	if !s.RevokedAt.IsZero() {
		return false
	}
	return s.ExpiresAt.IsZero() || !s.ExpiresAt.Before(now)
}

// RunnerPreviewArchiveSpec is the built-in runner action payload. The bundle
// itself is streamed to the dedicated upload endpoint, never through task JSON.
type RunnerPreviewArchiveSpec struct {
	PreviewID   string `json:"previewId"`
	RunID       string `json:"runId"`
	SourcePath  string `json:"sourcePath"`
	IndexFile   string `json:"indexFile"`
	SPAFallback bool   `json:"spaFallback,omitempty"`
	MaxFiles    int    `json:"maxFiles"`
	MaxBytes    int64  `json:"maxBytes"`
	UploadURL   string `json:"uploadUrl,omitempty"`
	UploadToken string `json:"uploadToken,omitempty"`
}
