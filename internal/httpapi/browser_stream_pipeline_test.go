package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeFrames is a browser behind a link with the given one-way delay. It
// captures a frame every interval, or, with interval 0, only when frame is
// raised. Like browserView.state it sends the image only when it is newer
// than the poll's threshold, and a poll gives up after timeout.
type fakeFrames struct {
	start    time.Time
	interval time.Duration
	delay    time.Duration
	timeout  time.Duration
	frame    atomic.Int64
	down     atomic.Bool
	pulls    atomic.Int64
}

func (f *fakeFrames) seq() int64 {
	if f.interval == 0 {
		return f.frame.Load()
	}
	return int64(time.Since(f.start) / f.interval)
}

func (f *fakeFrames) pull(ctx context.Context, after int64) json.RawMessage {
	f.pulls.Add(1)
	time.Sleep(f.delay)
	if f.down.Load() {
		return json.RawMessage(`{"available":false,"reason":"gone"}`)
	}
	timeout := f.timeout
	if timeout == 0 {
		timeout = time.Second
	}
	deadline := time.Now().Add(timeout)
	for f.seq() <= after && time.Now().Before(deadline) && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	seq, frame := f.seq(), ""
	if seq > after {
		frame = "x"
	}
	payload := json.RawMessage(fmt.Sprintf(`{"available":true,"seq":%d,"frame":%q}`, seq, frame))
	time.Sleep(f.delay)
	return payload
}

// panelFrames stands in for browserStream.publish: it shows a frame only when
// it carries an image newer than the last one shown.
type panelFrames struct {
	mu    sync.Mutex
	last  int64
	shown int
}

func (p *panelFrames) publish(payload json.RawMessage) (bool, error) {
	var state struct {
		Available bool   `json:"available"`
		Seq       int64  `json:"seq"`
		Frame     string `json:"frame"`
	}
	_ = json.Unmarshal(payload, &state)
	p.mu.Lock()
	defer p.mu.Unlock()
	if state.Frame != "" && state.Seq > p.last {
		p.last = state.Seq
		p.shown++
	}
	return state.Available, nil
}

func (p *panelFrames) latest() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// deliver runs the pipeline for d and counts frames shown.
func deliver(t *testing.T, pulls int, f *fakeFrames, d time.Duration) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	panel := &panelFrames{}
	if err := pipelineFrames(ctx, pulls, f.pull, panel.publish, panel.latest, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	return panel.shown
}

func TestPipelinedFramePollsKeepUpWithTheBrowser(t *testing.T) {
	const run = 1500 * time.Millisecond
	single := deliver(t, 1, &fakeFrames{start: time.Now(), interval: 30 * time.Millisecond, delay: 60 * time.Millisecond}, run)
	f := &fakeFrames{start: time.Now(), interval: 30 * time.Millisecond, delay: 60 * time.Millisecond}
	piped := deliver(t, 3, f, run)
	produced := int(run / f.interval)
	// One poll waits a round trip (120ms) per frame; three waiting ahead
	// deliver up to three per round trip of the 50 captured meanwhile.
	if single > 15 || piped*2 < single*3 || piped*3 < produced {
		t.Fatalf("single poll showed %d, pipelined %d of %d produced", single, piped, produced)
	}
	// A jump in sequence makes the polls waiting below it stale; each comes
	// back once and is not re-sent, so polls stay within twice the frames.
	if pulls := f.pulls.Load(); pulls > int64(2*piped)+5 {
		t.Fatalf("%d polls for %d frames", pulls, piped)
	}
}

func TestPipelinedFramePollsRetryWhileTheBrowserIsDown(t *testing.T) {
	f := &fakeFrames{start: time.Now(), interval: 30 * time.Millisecond}
	f.down.Store(true)
	retries := 0
	published := 0
	err := pipelineFrames(context.Background(), 3, f.pull, func(json.RawMessage) (bool, error) {
		published++
		return false, nil
	}, func() int64 { return 0 }, func() bool {
		retries++
		return retries < 2
	})
	if err != nil {
		t.Fatal(err)
	}
	// Each round sends the polls once and drains them before waiting.
	if retries != 2 || published != 6 || f.pulls.Load() != 6 {
		t.Fatalf("retries=%d published=%d pulls=%d", retries, published, f.pulls.Load())
	}
}

func TestPipelinedFramePollsShowTheFirstFrameAfterAnIdlePage(t *testing.T) {
	f := &fakeFrames{timeout: 20 * time.Millisecond}
	f.frame.Store(1)
	panel := &panelFrames{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = pipelineFrames(ctx, 3, f.pull, panel.publish, panel.latest, func() bool { return false }) }()
	waitFor := func(seq int64) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
			if panel.latest() >= seq {
				return
			}
		}
		t.Fatalf("panel stuck at frame %d, browser at %d", panel.latest(), seq)
	}
	waitFor(1)
	// A still page: every poll times out many times over.
	time.Sleep(400 * time.Millisecond)
	f.frame.Store(2)
	waitFor(2)
	time.Sleep(200 * time.Millisecond)
	f.frame.Store(3)
	waitFor(3)
}
