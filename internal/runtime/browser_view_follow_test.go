package runtime

import (
	"encoding/json"
	"testing"
	"time"
)

// sentCDP waits until the fake transport has sent a message accepted by
// match and returns it.
func sentCDP(t *testing.T, transport *fakeCDPTransport, match func(cdpMessage) bool) cdpMessage {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		transport.mu.Lock()
		for _, msg := range transport.sent {
			if match(msg) {
				transport.mu.Unlock()
				return msg
			}
		}
		transport.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("expected CDP message was never sent")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func cdpMethodCount(transport *fakeCDPTransport, method string) int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	n := 0
	for _, msg := range transport.sent {
		if msg.Method == method {
			n++
		}
	}
	return n
}

func attachTargetIs(id string) func(cdpMessage) bool {
	return func(msg cdpMessage) bool {
		if msg.Method != "Target.attachToTarget" {
			return false
		}
		var p struct {
			TargetID string `json:"targetId"`
		}
		return json.Unmarshal(msg.Params, &p) == nil && p.TargetID == id
	}
}

func TestBrowserViewFollowsCreatedTabsOnlyWhenEnabled(t *testing.T) {
	transport := newFakeCDPTransport()
	transport.results = map[string]string{"Target.attachToTarget": `{"sessionId":"page-new"}`}
	view := &browserView{targets: map[string]*browserTarget{}, changed: make(chan struct{})}
	view.client = newCDPClient(transport, view.onEvent)
	defer view.client.close()

	// Without following, a new tab is recorded but not attached; a changed
	// target is never followed.
	view.onEvent(targetEvent(t, "Target.targetCreated", map[string]any{
		"targetInfo": map[string]any{"targetId": "quiet", "type": "page"},
	}))
	view.follow = true
	view.onEvent(targetEvent(t, "Target.targetInfoChanged", map[string]any{
		"targetInfo": map[string]any{"targetId": "changed", "type": "page"},
	}))
	time.Sleep(50 * time.Millisecond)
	if n := cdpMethodCount(transport, "Target.attachToTarget"); n != 0 {
		t.Fatalf("attached without a followed targetCreated: %d", n)
	}

	view.onEvent(targetEvent(t, "Target.targetCreated", map[string]any{
		"targetInfo": map[string]any{"targetId": "opened", "type": "page"},
	}))
	sentCDP(t, transport, attachTargetIs("opened"))
	deadline := time.Now().Add(2 * time.Second)
	for view.activeID() != "opened" {
		if time.Now().After(deadline) {
			t.Fatalf("followed tab never became active: %q", view.activeID())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBrowserViewAttachesLatestTargetWhenActiveCloses(t *testing.T) {
	transport := newFakeCDPTransport()
	transport.results = map[string]string{"Target.attachToTarget": `{"sessionId":"page-2"}`}
	now := time.Now()
	view := &browserView{targets: map[string]*browserTarget{
		"t1":  {ID: "t1", created: now.Add(-time.Minute)},
		"old": {ID: "old", created: now.Add(-time.Hour)},
		"new": {ID: "new", created: now},
	}, changed: make(chan struct{}), active: "t1", session: "page-1"}
	view.client = newCDPClient(transport, view.onEvent)
	defer view.client.close()

	view.onEvent(targetEvent(t, "Target.targetDestroyed", map[string]any{"targetId": "t1"}))
	sentCDP(t, transport, attachTargetIs("new"))
	deadline := time.Now().Add(2 * time.Second)
	for view.activeID() != "new" {
		if time.Now().After(deadline) {
			t.Fatalf("latest target never became active: %q", view.activeID())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := cdpMethodCount(transport, "Target.attachToTarget"); n != 1 {
		t.Fatalf("attach calls: %d", n)
	}
}

func TestBrowserViewAcksFramesFromOtherSessions(t *testing.T) {
	transport := newFakeCDPTransport()
	view := &browserView{targets: map[string]*browserTarget{}, changed: make(chan struct{}), session: "page-1", active: "t1", frame: "keep", seq: 3}
	view.client = newCDPClient(transport, view.onEvent)
	defer view.client.close()

	frame, _ := json.Marshal(map[string]any{"data": "stale", "sessionId": 9, "metadata": map[string]any{"deviceWidth": 10, "deviceHeight": 20}})
	view.onEvent(cdpMessage{Method: "Page.screencastFrame", SessionID: "page-old", Params: frame})
	ack := sentCDP(t, transport, func(msg cdpMessage) bool { return msg.Method == "Page.screencastFrameAck" })
	if ack.SessionID != "page-old" {
		t.Fatalf("ack sent to session %q", ack.SessionID)
	}
	var p struct {
		SessionID int64 `json:"sessionId"`
	}
	if err := json.Unmarshal(ack.Params, &p); err != nil || p.SessionID != 9 {
		t.Fatalf("ack params %s", ack.Params)
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.frame != "keep" || view.seq != 3 || view.width != 0 {
		t.Fatalf("frame from another session recorded: frame=%q seq=%d width=%v", view.frame, view.seq, view.width)
	}
	if n := cdpMethodCount(transport, "Page.getLayoutMetrics"); n != 0 {
		t.Fatalf("content refresh for another session: %d", n)
	}
}
