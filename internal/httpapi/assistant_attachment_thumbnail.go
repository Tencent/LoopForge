package httpapi

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp" // Decode still WebP. Unsupported animated formats use the original.
)

func thumbnailImageType(mime string) bool {
	return mime == "image/png" || mime == "image/jpeg" || mime == "image/webp"
}

// Each shard serializes decoding and keeps the last twelve results. This bounds both
// the number of large original images decoded at once and the memory held between reads.
type attachmentThumbnailCache struct {
	mu      sync.Mutex
	next    int
	entries [12]struct {
		owner int64
		id    string
		data  []byte
	}
}

func (h *assistantHandlers) attachmentThumbnail(owner int64, id string, file *os.File, size int64) []byte {
	// Files are immutable. Use their owner-scoped IDs as keys, and only
	// consult this cache after checking access and opening the original.
	cache := &h.attachmentThumbnails[id[0]%2]
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for _, entry := range cache.entries {
		if entry.owner == owner && entry.id == id {
			return entry.data
		}
	}
	data := makeAttachmentThumbnail(file, size)
	entry := &cache.entries[cache.next]
	entry.owner, entry.id, entry.data = owner, id, data
	cache.next = (cache.next + 1) % len(cache.entries)
	return data
}

func makeAttachmentThumbnail(file io.ReaderAt, size int64) []byte {
	// Use section readers so falling back to ServeContent always leaves the original intact.
	if size <= 16<<10 || size > workbenchAttachmentLimit {
		return nil
	}
	source := io.NewSectionReader(file, 0, size)
	config, format, err := image.DecodeConfig(source)
	if err != nil || !thumbnailSizeAllowed(config) {
		return nil
	}
	if format != "png" && format != "jpeg" && format != "webp" {
		return nil
	}
	_, _ = source.Seek(0, io.SeekStart)
	original, err := imaging.Decode(source, imaging.AutoOrientation(true))
	if err != nil {
		return nil
	}
	// Twice the chat bubble's 340 × 480 CSS pixels stays sharp on a retina display.
	thumb := imaging.Fit(original, 680, 960, imaging.Box)
	var buf bytes.Buffer
	if thumb.Opaque() {
		err = jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 78})
	} else {
		err = png.Encode(&buf, thumb)
	}
	if err != nil || int64(buf.Len()) >= size || buf.Len() > 512<<10 {
		return nil
	}
	return buf.Bytes()
}

func thumbnailSizeAllowed(config image.Config) bool {
	maxPixels := 24_000_000
	switch config.ColorModel {
	case color.Gray16Model, color.RGBA64Model, color.NRGBA64Model:
		// A 16-bit PNG can need eight bytes per pixel, even grayscale with transparency.
		maxPixels /= 2
	}
	return config.Width > 0 && config.Height > 0 && config.Width <= maxPixels/config.Height
}

// attachmentETagMatches applies If-None-Match's weak comparison to one entity tag.
func attachmentETagMatches(headers []string, tag string) bool {
	for _, header := range headers {
		for _, candidate := range strings.Split(header, ",") {
			candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "W/")
			if candidate == tag {
				return true
			}
		}
	}
	return false
}
