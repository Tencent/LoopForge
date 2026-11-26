package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A static page still ends on a sharp frame after a scroll, whether or not
// Chromium re-sends it when the screencast restarts.
func TestBrowserScrollEndsOnASharpFrame(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := FindChromium(); err != nil {
		t.Skip("chromium not installed")
	}
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<title>静态</title><h1>不会动的页面</h1>`)
	}))
	defer page.Close()
	workspace := t.TempDir()
	runID := "browser-motion-" + fmt.Sprint(time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := ensureHostBrowser(ctx, workspace, runID); err != nil {
		t.Fatal(err)
	}
	defer stopHostBrowsers(workspace)
	if _, err := HandleBrowser(ctx, runID, domain.BrowserRequest{Operation: "resize", FrameWidth: 800}); err != nil {
		t.Fatal(err)
	}
	state, err := HandleBrowser(ctx, runID, domain.BrowserRequest{Operation: "navigate", URL: page.URL})
	if err != nil {
		t.Fatal(err)
	}
	// nextWidth waits for a frame after seq and returns its pixel width.
	nextWidth := func(after int64) (int64, int) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			st, err := HandleBrowser(ctx, runID, domain.BrowserRequest{Operation: "frame", After: after, WaitMs: 2000})
			if err != nil {
				t.Fatal(err)
			}
			if st.Seq > after && st.Frame != "" {
				raw, _ := base64.StdEncoding.DecodeString(st.Frame)
				config, err := jpeg.DecodeConfig(bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				return st.Seq, config.Width
			}
		}
		t.Fatal("no frame")
		return 0, 0
	}
	seq, width := nextWidth(state.Seq)
	// Let the page finish painting at full width.
	for width < 700 {
		seq, width = nextWidth(seq)
	}
	scrolled := time.Now()
	if _, err := HandleBrowser(ctx, runID, domain.BrowserRequest{Operation: "input", Events: []domain.BrowserInputEvent{{Type: "wheel", X: 0.5, Y: 0.5, DeltaY: 100}}}); err != nil {
		t.Fatal(err)
	}
	// Light frames may arrive while the scroll is in motion, and a full-width
	// frame already in flight can arrive right after the wheel; neither is
	// guaranteed on a page that does not repaint. Once input settles a
	// full-width frame arrives although nothing changed.
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		if seq, width = nextWidth(seq); width >= 700 && time.Since(scrolled) >= browserMotionSettle {
			return
		}
	}
	t.Fatalf("no sharp frame after the scroll settled (last %dpx)", width)
}
