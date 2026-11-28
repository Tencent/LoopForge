package httpapi

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestChatImageThumbnailRevalidatesAfterAuthorization(t *testing.T) {
	h, user := workbenchFixture(t)
	pic := image.NewRGBA(image.Rect(0, 0, 1360, 800))
	for y := 0; y < 800; y++ {
		for x := 0; x < 1360; x++ {
			pic.SetRGBA(x, y, color.RGBA{R: uint8(x * y), G: uint8(x + y), B: uint8(x ^ y), A: 255})
		}
	}
	var source bytes.Buffer
	if err := png.Encode(&source, pic); err != nil {
		t.Fatal(err)
	}
	a, err := saveWorkbenchImage(user.ID, "shot.png", "image/png", source.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	get := func(viewer domain.User, query, etag string) *httptest.ResponseRecorder {
		c, rec := workbenchContext(viewer, "GET", "/api/assistant/attachments/"+a.ID+query, bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: a.ID}}
		c.Request.Header.Set("If-None-Match", etag)
		h.downloadAttachment(c)
		c.Writer.WriteHeaderNow()
		return rec
	}

	preview := get(user, "?view=thumbnail", "")
	cfg, _, err := image.DecodeConfig(bytes.NewReader(preview.Body.Bytes()))
	if err != nil || preview.Code != 200 || cfg.Width != 680 || cfg.Height != 400 || preview.Body.Len() >= source.Len() {
		t.Fatalf("preview status=%d size=%d dimensions=%+v error=%v", preview.Code, preview.Body.Len(), cfg, err)
	}
	tag := preview.Header().Get("ETag")
	if tag == "" || preview.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("preview headers: %v", preview.Header())
	}
	if cached := get(user, "?view=thumbnail", tag); cached.Code != 304 || cached.Body.Len() != 0 {
		t.Fatalf("revalidated preview status=%d bytes=%d", cached.Code, cached.Body.Len())
	}
	original := get(user, "", tag)
	if original.Code != 200 || !bytes.Equal(original.Body.Bytes(), source.Bytes()) || original.Header().Get("ETag") == tag {
		t.Fatal("the full-size URL must still return the original PNG")
	}
	other := user
	other.ID++
	if denied := get(other, "?view=thumbnail", tag); denied.Code != 404 {
		t.Fatalf("a private thumbnail validator must not grant access: %d", denied.Code)
	}
}

func TestChatImageThumbnailRevalidationSkipsDecoding(t *testing.T) {
	h, user := workbenchFixture(t)
	pic := image.NewRGBA(image.Rect(0, 0, 1360, 800))
	for y := 0; y < 800; y++ {
		for x := 0; x < 1360; x++ {
			pic.SetRGBA(x, y, color.RGBA{R: uint8(x * y), G: uint8(x + y), B: uint8(x ^ y), A: 255})
		}
	}
	var source bytes.Buffer
	if err := png.Encode(&source, pic); err != nil {
		t.Fatal(err)
	}
	a, err := saveWorkbenchImage(user.ID, "shot.png", "image/png", source.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	get := func(viewer domain.User, query string, etags ...string) *httptest.ResponseRecorder {
		c, rec := workbenchContext(viewer, "GET", "/api/assistant/attachments/"+a.ID+query, bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: a.ID}}
		for _, tag := range etags {
			c.Request.Header.Add("If-None-Match", tag)
		}
		h.downloadAttachment(c)
		c.Writer.WriteHeaderNow()
		return rec
	}
	cached := func() int {
		n := 0
		for i := range h.attachmentThumbnails {
			for _, entry := range h.attachmentThumbnails[i].entries {
				if entry.id == a.ID {
					n++
				}
			}
		}
		return n
	}

	preview := get(user, "?view=thumbnail")
	tag := preview.Header().Get("ETag")
	if preview.Code != 200 || tag == "" || cached() != 1 {
		t.Fatalf("preview status=%d tag=%q cached=%d", preview.Code, tag, cached())
	}
	// Simulate the preview being evicted from the in-memory cache.
	for i := range h.attachmentThumbnails {
		h.attachmentThumbnails[i] = attachmentThumbnailCache{}
	}
	for _, validators := range [][]string{{tag}, {"W/" + tag}, {`"other"`, tag}, {`"other", ` + tag}} {
		rec := get(user, "?view=thumbnail", validators...)
		if rec.Code != 304 || rec.Body.Len() != 0 || rec.Header().Get("ETag") != tag || rec.Header().Get("Cache-Control") != "private, no-cache" {
			t.Fatalf("If-None-Match %q: status=%d bytes=%d headers=%v", validators, rec.Code, rec.Body.Len(), rec.Header())
		}
	}
	if n := cached(); n != 0 {
		t.Fatalf("revalidating a known preview decoded the original again (%d cache entries)", n)
	}
	if stale := get(user, "?view=thumbnail", `"`+a.ID+`"`); stale.Code != 200 || stale.Header().Get("ETag") != tag || cached() != 1 {
		t.Fatalf("an original-image validator must not match the preview: status=%d tag=%q", stale.Code, stale.Header().Get("ETag"))
	}
	other := user
	other.ID++
	if denied := get(other, "?view=thumbnail", tag); denied.Code != 404 {
		t.Fatalf("a private thumbnail validator must not grant access: %d", denied.Code)
	}
}
