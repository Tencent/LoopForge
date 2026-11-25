package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// settledFrameTransport answers like fakeCDPTransport, except that
// Page.captureScreenshot can fail, or be preceded by a screencast frame that
// Chromium sends while the screenshot is being taken.
type settledFrameTransport struct {
	*fakeCDPTransport
	shotError       string
	frameDuringShot bool
}

func (f *settledFrameTransport) write(body []byte) error {
	var msg cdpMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return err
	}
	if msg.Method != "Page.captureScreenshot" {
		return f.fakeCDPTransport.write(body)
	}
	f.mu.Lock()
	f.sent = append(f.sent, msg)
	f.mu.Unlock()
	if f.frameDuringShot {
		params, _ := json.Marshal(map[string]any{"data": "RESENT", "sessionId": 1, "metadata": map[string]any{"deviceWidth": 1000, "deviceHeight": 500}})
		event, _ := json.Marshal(cdpMessage{Method: "Page.screencastFrame", SessionID: msg.SessionID, Params: params})
		f.replies <- event
	}
	reply := cdpMessage{ID: msg.ID, Result: json.RawMessage(`{"data":"SHARP"}`)}
	if f.shotError != "" {
		reply.Result = nil
		reply.Error = &struct {
			Message string `json:"message"`
		}{Message: f.shotError}
	}
	raw, _ := json.Marshal(reply)
	f.replies <- raw
	return nil
}

func cdpCalls(f *fakeCDPTransport, method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, msg := range f.sent {
		if msg.Method == method {
			n++
		}
	}
	return n
}

// newSettledFrameView is a view showing a light frame at seq 5 on page-1.
// contentBusy keeps screencast frames from starting a layout metrics read.
func newSettledFrameView(t *testing.T, transport cdpTransport) *browserView {
	t.Helper()
	view := &browserView{targets: map[string]*browserTarget{}, changed: make(chan struct{}), session: "page-1", active: "t1",
		width: 1000, height: 500, frame: "LIGHT", seq: 5, contentBusy: true}
	view.client = newCDPClient(transport, view.onEvent)
	t.Cleanup(func() { view.client.close() })
	return view
}

func TestEnsureSettledFramePublishesTheScreenshot(t *testing.T) {
	fake := newFakeCDPTransport()
	fake.results = map[string]string{"Page.captureScreenshot": `{"data":"SHARP"}`}
	view := newSettledFrameView(t, fake)
	view.mu.Lock()
	changed := view.changed
	view.mu.Unlock()

	view.ensureSettledFrame(context.Background(), "page-1", time.Millisecond)

	if state := view.state(0); state.Frame != "SHARP" || state.Seq != 6 {
		t.Fatalf("frame %q seq %d, want SHARP at 6", state.Frame, state.Seq)
	}
	select {
	case <-changed:
	default:
		t.Fatal("watchers were not notified of the captured frame")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sent) != 1 || fake.sent[0].SessionID != "page-1" {
		t.Fatalf("sent %+v", fake.sent)
	}
	var params struct {
		Format  string `json:"format"`
		Quality int    `json:"quality"`
	}
	if err := json.Unmarshal(fake.sent[0].Params, &params); err != nil || params.Format != "jpeg" || params.Quality != 80 {
		t.Fatalf("screenshot params %s (%v)", fake.sent[0].Params, err)
	}
}

func TestEnsureSettledFrameKeepsTheViewWhenItShouldNotCapture(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*browserView)
	}{
		{"input moves again before the wait ends", func(v *browserView) { v.motion = true }},
		{"the panel switched to another tab", func(v *browserView) { v.session = "page-2" }},
		{"a frame already arrived", func(v *browserView) { v.seq = 7 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeCDPTransport()
			fake.results = map[string]string{"Page.captureScreenshot": `{"data":"SHARP"}`}
			view := newSettledFrameView(t, fake)
			// Change the view during the wait, right after seq was sampled.
			view.settledFrameSampled = func() {
				view.mu.Lock()
				tc.setup(view)
				view.mu.Unlock()
			}

			view.ensureSettledFrame(context.Background(), "page-1", time.Millisecond)

			if n := cdpCalls(fake, "Page.captureScreenshot"); n != 0 {
				t.Fatalf("screenshots: %d", n)
			}
			if frame := view.state(0).Frame; frame != "LIGHT" {
				t.Fatalf("frame %q", frame)
			}
		})
	}
}

func TestEnsureSettledFrameStopsWhenTheContextEnds(t *testing.T) {
	fake := newFakeCDPTransport()
	view := newSettledFrameView(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		view.ensureSettledFrame(ctx, "page-1", time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ensureSettledFrame kept waiting after its context ended")
	}
	if n := cdpCalls(fake, "Page.captureScreenshot"); n != 0 {
		t.Fatalf("screenshots: %d", n)
	}
	if state := view.state(0); state.Frame != "LIGHT" || state.Seq != 5 {
		t.Fatalf("frame %q seq %d", state.Frame, state.Seq)
	}
}

func TestEnsureSettledFrameKeepsTheLightFrameWhenTheCaptureFails(t *testing.T) {
	cases := []struct {
		name      string
		transport func() (cdpTransport, *fakeCDPTransport)
	}{
		{"cdp error", func() (cdpTransport, *fakeCDPTransport) {
			f := &settledFrameTransport{fakeCDPTransport: newFakeCDPTransport(), shotError: "Unable to capture screenshot"}
			return f, f.fakeCDPTransport
		}},
		{"empty data", func() (cdpTransport, *fakeCDPTransport) {
			f := newFakeCDPTransport()
			f.results = map[string]string{"Page.captureScreenshot": `{"data":""}`}
			return f, f
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport, fake := tc.transport()
			view := newSettledFrameView(t, transport)

			view.ensureSettledFrame(context.Background(), "page-1", time.Millisecond)

			if n := cdpCalls(fake, "Page.captureScreenshot"); n != 1 {
				t.Fatalf("screenshots: %d", n)
			}
			if state := view.state(0); state.Frame != "LIGHT" || state.Seq != 5 {
				t.Fatalf("frame %q seq %d, want LIGHT at 5", state.Frame, state.Seq)
			}
		})
	}
}

// A frame Chromium sends while the screenshot is taken is newer than the
// screenshot request, so the screenshot is dropped.
func TestEnsureSettledFrameDropsTheScreenshotWhenAFrameArrivesMeanwhile(t *testing.T) {
	transport := &settledFrameTransport{fakeCDPTransport: newFakeCDPTransport(), frameDuringShot: true}
	view := newSettledFrameView(t, transport)

	view.ensureSettledFrame(context.Background(), "page-1", time.Millisecond)

	if n := cdpCalls(transport.fakeCDPTransport, "Page.captureScreenshot"); n != 1 {
		t.Fatalf("screenshots: %d", n)
	}
	if state := view.state(0); state.Frame != "RESENT" || state.Seq != 6 {
		t.Fatalf("frame %q seq %d, want RESENT at 6", state.Frame, state.Seq)
	}
}
