package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
)

const browserViewIdle = 60 * time.Second

type browserTarget struct {
	ID, Title, URL string
	created        time.Time
}

// browserView is the task panel's attachment to one run browser: it follows
// the agent's newest tab, mirrors it via Page.startScreencast and forwards
// user input. Agent automation keeps its own CDP connection.
type browserView struct {
	runID    string
	endpoint browserEndpoint
	client   *cdpClient

	mu       sync.Mutex
	targets  map[string]*browserTarget
	active   string
	session  string
	frame    string
	seq      int64
	width    float64
	height   float64
	lastUsed time.Time
	changed  chan struct{}
	follow   bool
	// frameWidth is the panel's device pixel width; 0 keeps the default cap.
	frameWidth int
	// History flags are cached per active page URL: re-reading them on every
	// frame would add a CDP round trip to each one.
	historyKey              string
	canGoBack, canGoForward bool
}

var browserViews = struct {
	sync.Mutex
	items map[string]*browserView
}{items: map[string]*browserView{}}

var browserViewReads = map[string]bool{"": true, "status": true, "frame": true}

// HandleBrowser serves one task panel browser request for a run whose browser
// lives on this host (docker sidecar or host Chromium).
func HandleBrowser(ctx context.Context, runID string, req domain.BrowserRequest) (domain.BrowserState, error) {
	view, err := openBrowserView(ctx, runID)
	if err != nil {
		return domain.BrowserState{Available: false, Reason: err.Error()}, nil
	}
	view.touch()
	switch req.Operation {
	case "", "status", "start":
	case "frame":
		view.waitFrame(ctx, req.After, req.WaitMs)
	case "input":
		if err := view.input(ctx, req.Events); err != nil {
			return view.state(req.After), err
		}
	case "navigate":
		if err := view.navigate(ctx, req.URL); err != nil {
			return view.state(req.After), err
		}
	case "back", "forward":
		if err := view.history(ctx, req.Operation == "back"); err != nil {
			return view.state(req.After), err
		}
	case "reload":
		if err := view.pageCall(ctx, "Page.reload", map[string]any{}); err != nil {
			return view.state(req.After), err
		}
	case "resize":
		if req.Width > 0 || req.Height > 0 {
			if err := view.resize(ctx, req.Width, req.Height); err != nil {
				return view.state(req.After), err
			}
		}
		if req.FrameWidth > 0 {
			if err := view.setFrameWidth(ctx, req.FrameWidth); err != nil {
				return view.state(req.After), err
			}
		}
	case "activate":
		if err := view.attach(ctx, req.TargetID); err != nil {
			return view.state(req.After), err
		}
	case "newTab":
		var created struct {
			TargetID string `json:"targetId"`
		}
		if err := view.client.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
			return view.state(req.After), err
		}
		if err := view.attach(ctx, created.TargetID); err != nil {
			return view.state(req.After), err
		}
	case "closeTab":
		target := req.TargetID
		if target == "" {
			target = view.activeID()
		}
		if err := view.client.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": target}, nil); err != nil {
			return view.state(req.After), err
		}
	case "snapshot", "screenshot", "click", "type", "press":
		snap, frame, err := view.agentOperation(ctx, req)
		state := view.state(req.After)
		state.Snapshot = snap
		if frame != "" {
			state.Frame = frame
		}
		return state, err
	default:
		return view.state(req.After), fmt.Errorf("不支持的浏览器操作 %q", req.Operation)
	}
	if !browserViewReads[req.Operation] {
		view.mu.Lock()
		view.historyKey = ""
		view.mu.Unlock()
	}
	state := view.state(req.After)
	if req.WithSnapshot {
		view.settle(ctx)
		snap, err := view.snapshot(ctx)
		state = view.state(req.After)
		state.Snapshot = snap
		if err != nil {
			return state, err
		}
	}
	if browserViewReads[req.Operation] {
		state.CanGoBack, state.CanGoForward = view.cachedHistoryFlags(ctx, state.URL)
	}
	return state, nil
}

func openBrowserView(ctx context.Context, runID string) (*browserView, error) {
	browserViews.Lock()
	view := browserViews.items[runID]
	browserViews.Unlock()
	if view != nil && !view.client.closed() {
		return view, nil
	}
	endpoint, ok := lookupBrowserEndpoint(ctx, runID)
	if !ok {
		return nil, errors.New("Agent 浏览器尚未启动：Agent 用到时会自动启动，也可以现在手动启动")
	}
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	transport, err := endpoint.dial(dialCtx)
	if err != nil {
		return nil, err
	}
	view = &browserView{runID: runID, endpoint: endpoint, targets: map[string]*browserTarget{}, changed: make(chan struct{}), lastUsed: time.Now()}
	view.client = newCDPClient(transport, view.onEvent)
	if err := view.client.call(dialCtx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		view.client.close()
		return nil, err
	}
	var listed struct {
		TargetInfos []cdpTargetInfo `json:"targetInfos"`
	}
	if err := view.client.call(dialCtx, "", "Target.getTargets", nil, &listed); err == nil {
		view.mu.Lock()
		for _, info := range listed.TargetInfos {
			if info.Type == "page" {
				view.targets[info.TargetID] = &browserTarget{ID: info.TargetID, Title: info.Title, URL: info.URL}
			}
		}
		view.mu.Unlock()
	}
	if target := view.latestTarget(); target != "" {
		_ = view.attach(dialCtx, target)
	} else {
		var created struct {
			TargetID string `json:"targetId"`
		}
		if err := view.client.call(dialCtx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err == nil {
			_ = view.attach(dialCtx, created.TargetID)
		}
	}
	view.mu.Lock()
	view.follow = true
	view.mu.Unlock()
	browserViews.Lock()
	if old := browserViews.items[runID]; old != nil && old != view {
		old.client.close()
	}
	browserViews.items[runID] = view
	browserViews.Unlock()
	go view.reapWhenIdle()
	return view, nil
}

type cdpTargetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Attached bool   `json:"attached"`
}

func (v *browserView) touch() {
	v.mu.Lock()
	v.lastUsed = time.Now()
	v.mu.Unlock()
}

// reapWhenIdle stops screencasting once nobody watches, so an unattended run
// does not keep encoding frames.
func (v *browserView) reapWhenIdle() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-v.client.done:
		case <-ticker.C:
			v.mu.Lock()
			idle := time.Since(v.lastUsed) > browserViewIdle
			v.mu.Unlock()
			if !idle {
				continue
			}
			v.client.close()
		}
		browserViews.Lock()
		if browserViews.items[v.runID] == v {
			delete(browserViews.items, v.runID)
		}
		browserViews.Unlock()
		return
	}
}

func (v *browserView) notify() {
	close(v.changed)
	v.changed = make(chan struct{})
}

func (v *browserView) onEvent(msg cdpMessage) {
	switch msg.Method {
	case "Target.targetCreated", "Target.targetInfoChanged":
		var ev struct {
			TargetInfo cdpTargetInfo `json:"targetInfo"`
		}
		if json.Unmarshal(msg.Params, &ev) != nil || ev.TargetInfo.Type != "page" {
			return
		}
		v.mu.Lock()
		t := v.targets[ev.TargetInfo.TargetID]
		created := t == nil
		if created {
			t = &browserTarget{ID: ev.TargetInfo.TargetID, created: time.Now()}
			v.targets[t.ID] = t
		}
		t.Title, t.URL = ev.TargetInfo.Title, ev.TargetInfo.URL
		follow := created && v.follow && msg.Method == "Target.targetCreated"
		v.notify()
		v.mu.Unlock()
		if follow {
			// Follow tabs the agent opens so the panel shows what it works on.
			go func(id string) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = v.attach(ctx, id)
			}(t.ID)
		}
	case "Target.targetDestroyed":
		var ev struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(msg.Params, &ev) != nil {
			return
		}
		v.mu.Lock()
		delete(v.targets, ev.TargetID)
		wasActive := v.active == ev.TargetID
		if wasActive {
			v.active, v.session = "", ""
		}
		v.notify()
		v.mu.Unlock()
		if wasActive {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if next := v.latestTarget(); next != "" {
					_ = v.attach(ctx, next)
				}
			}()
		}
	case "Page.screencastFrame":
		var ev struct {
			Data      string `json:"data"`
			SessionID int64  `json:"sessionId"`
			Metadata  struct {
				DeviceWidth  float64 `json:"deviceWidth"`
				DeviceHeight float64 `json:"deviceHeight"`
			} `json:"metadata"`
		}
		if json.Unmarshal(msg.Params, &ev) != nil {
			return
		}
		v.mu.Lock()
		current := msg.SessionID == v.session
		if current {
			v.frame, v.width, v.height = ev.Data, ev.Metadata.DeviceWidth, ev.Metadata.DeviceHeight
			v.seq++
			v.notify()
		}
		v.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = v.client.call(ctx, msg.SessionID, "Page.screencastFrameAck", map[string]any{"sessionId": ev.SessionID}, nil)
		}()
	}
}

func (v *browserView) latestTarget() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	best := ""
	var bestAt time.Time
	for id, t := range v.targets {
		if best == "" || t.created.After(bestAt) || t.created.Equal(bestAt) && id > best {
			best, bestAt = id, t.created
		}
	}
	return best
}

func (v *browserView) activeID() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.active
}

func (v *browserView) attach(ctx context.Context, targetID string) error {
	if targetID == "" {
		return errors.New("标签页不存在")
	}
	v.mu.Lock()
	if v.active == targetID && v.session != "" {
		v.mu.Unlock()
		return nil
	}
	previous := v.session
	v.mu.Unlock()
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := v.client.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, &attached); err != nil {
		return err
	}
	if previous != "" {
		_ = v.client.call(ctx, previous, "Page.stopScreencast", nil, nil)
		_ = v.client.call(ctx, "", "Target.detachFromTarget", map[string]any{"sessionId": previous}, nil)
	}
	v.mu.Lock()
	v.active, v.session = targetID, attached.SessionID
	v.notify()
	v.mu.Unlock()
	session := attached.SessionID
	_ = v.client.call(ctx, session, "Page.enable", nil, nil)
	var metrics struct {
		CSSVisualViewport struct {
			ClientWidth  float64 `json:"clientWidth"`
			ClientHeight float64 `json:"clientHeight"`
		} `json:"cssVisualViewport"`
	}
	if v.client.call(ctx, session, "Page.getLayoutMetrics", nil, &metrics) == nil {
		var shot struct {
			Data string `json:"data"`
		}
		if v.client.call(ctx, session, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 70}, &shot) == nil && shot.Data != "" {
			v.mu.Lock()
			if v.session == session {
				v.frame, v.width, v.height = shot.Data, metrics.CSSVisualViewport.ClientWidth, metrics.CSSVisualViewport.ClientHeight
				v.seq++
				v.notify()
			}
			v.mu.Unlock()
		}
	}
	return v.client.call(ctx, session, "Page.startScreencast", v.screencastParams(), nil)
}

func (v *browserView) screencastParams() map[string]any {
	v.mu.Lock()
	width := v.frameWidth
	v.mu.Unlock()
	if width <= 0 || width > 1600 {
		width = 1600
	}
	width = max(width, 320)
	return map[string]any{"format": "jpeg", "quality": 60, "maxWidth": width, "maxHeight": min(2*width, 2000), "everyNthFrame": 1}
}

// setFrameWidth restarts the screencast when the panel's pixel width changes.
func (v *browserView) setFrameWidth(ctx context.Context, width int) error {
	v.mu.Lock()
	changed := v.frameWidth != width
	v.frameWidth = width
	session := v.session
	v.mu.Unlock()
	if !changed || session == "" {
		return nil
	}
	_ = v.client.call(ctx, session, "Page.stopScreencast", nil, nil)
	return v.client.call(ctx, session, "Page.startScreencast", v.screencastParams(), nil)
}

func (v *browserView) cachedHistoryFlags(ctx context.Context, url string) (bool, bool) {
	v.mu.Lock()
	key := v.active + "\x00" + url
	if key == v.historyKey {
		back, forward := v.canGoBack, v.canGoForward
		v.mu.Unlock()
		return back, forward
	}
	v.mu.Unlock()
	back, forward := v.historyFlags(ctx)
	v.mu.Lock()
	v.historyKey, v.canGoBack, v.canGoForward = key, back, forward
	v.mu.Unlock()
	return back, forward
}

func (v *browserView) waitFrame(ctx context.Context, after int64, waitMs int) {
	if waitMs <= 0 || waitMs > 4000 {
		waitMs = 4000
	}
	deadline := time.NewTimer(time.Duration(waitMs) * time.Millisecond)
	defer deadline.Stop()
	for {
		v.mu.Lock()
		if v.seq > after {
			v.mu.Unlock()
			return
		}
		changed := v.changed
		v.mu.Unlock()
		select {
		case <-changed:
		case <-deadline.C:
			return
		case <-ctx.Done():
			return
		case <-v.client.done:
			return
		}
	}
}

func (v *browserView) state(after int64) domain.BrowserState {
	v.mu.Lock()
	defer v.mu.Unlock()
	state := domain.BrowserState{Available: !v.client.closed(), Kind: v.endpoint.Kind, Seq: v.seq, Width: v.width, Height: v.height}
	if !state.Available {
		state.Reason = "浏览器连接已断开"
	}
	if v.seq > after {
		state.Frame = v.frame
	}
	for _, t := range v.targets {
		tab := domain.BrowserTab{ID: t.ID, Title: t.Title, URL: t.URL, Active: t.ID == v.active}
		state.Tabs = append(state.Tabs, tab)
		if tab.Active {
			state.URL, state.Title = t.URL, t.Title
		}
	}
	sortBrowserTabs(state.Tabs, v.targets)
	return state
}

func sortBrowserTabs(tabs []domain.BrowserTab, targets map[string]*browserTarget) {
	for i := 1; i < len(tabs); i++ {
		for j := i; j > 0; j-- {
			a, b := targets[tabs[j-1].ID], targets[tabs[j].ID]
			if a.created.Before(b.created) || a.created.Equal(b.created) && a.ID <= b.ID {
				break
			}
			tabs[j-1], tabs[j] = tabs[j], tabs[j-1]
		}
	}
}

func (v *browserView) pageCall(ctx context.Context, method string, params any) error {
	v.mu.Lock()
	session := v.session
	v.mu.Unlock()
	if session == "" {
		return errors.New("没有可操作的标签页")
	}
	return v.client.call(ctx, session, method, params, nil)
}

func (v *browserView) navigate(ctx context.Context, raw string) error {
	target := strings.TrimSpace(raw)
	if target == "" {
		return errors.New("请输入网址")
	}
	if !strings.Contains(target, "://") && !strings.HasPrefix(target, "about:") {
		if strings.Contains(target, " ") || !strings.Contains(target, ".") && !strings.HasPrefix(target, "localhost") {
			target = "https://www.bing.com/search?q=" + url.QueryEscape(target)
		} else if strings.HasPrefix(target, "localhost") || strings.HasPrefix(target, "127.0.0.1") {
			target = "http://" + target
		} else {
			target = "https://" + target
		}
	}
	return v.pageCall(ctx, "Page.navigate", map[string]any{"url": target})
}

type cdpHistory struct {
	CurrentIndex int `json:"currentIndex"`
	Entries      []struct {
		ID int `json:"id"`
	} `json:"entries"`
}

func (v *browserView) navigationHistory(ctx context.Context) (cdpHistory, error) {
	v.mu.Lock()
	session := v.session
	v.mu.Unlock()
	var history cdpHistory
	if session == "" {
		return history, errors.New("没有可操作的标签页")
	}
	err := v.client.call(ctx, session, "Page.getNavigationHistory", nil, &history)
	return history, err
}

func (v *browserView) historyFlags(ctx context.Context) (bool, bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	history, err := v.navigationHistory(ctx)
	if err != nil {
		return false, false
	}
	return history.CurrentIndex > 0, history.CurrentIndex < len(history.Entries)-1
}

func (v *browserView) history(ctx context.Context, back bool) error {
	history, err := v.navigationHistory(ctx)
	if err != nil {
		return err
	}
	next := history.CurrentIndex + 1
	if back {
		next = history.CurrentIndex - 1
	}
	if next < 0 || next >= len(history.Entries) {
		return nil
	}
	return v.pageCall(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[next].ID})
}

// resize sizes the browser window, not an emulation override, so the agent's
// Playwright session sees the same viewport the user is looking at.
func (v *browserView) resize(ctx context.Context, width, height int) error {
	if width < 800 || height < 400 || width > 2560 || height > 2560 {
		return errors.New("窗口尺寸超出范围")
	}
	target := v.activeID()
	if target == "" {
		return errors.New("没有可操作的标签页")
	}
	var window struct {
		WindowID int `json:"windowId"`
		Bounds   struct {
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		} `json:"bounds"`
	}
	if err := v.client.call(ctx, "", "Browser.getWindowForTarget", map[string]any{"targetId": target}, &window); err != nil {
		return err
	}
	// Window bounds include (headless) browser chrome; size the viewport.
	var metrics struct {
		CSSLayoutViewport struct {
			ClientWidth  float64 `json:"clientWidth"`
			ClientHeight float64 `json:"clientHeight"`
		} `json:"cssLayoutViewport"`
	}
	chromeW, chromeH := 0.0, 0.0
	v.mu.Lock()
	session := v.session
	v.mu.Unlock()
	if session != "" && v.client.call(ctx, session, "Page.getLayoutMetrics", nil, &metrics) == nil && metrics.CSSLayoutViewport.ClientHeight > 0 {
		chromeW = max(0, window.Bounds.Width-metrics.CSSLayoutViewport.ClientWidth)
		chromeH = max(0, window.Bounds.Height-metrics.CSSLayoutViewport.ClientHeight)
	}
	return v.client.call(ctx, "", "Browser.setWindowBounds", map[string]any{"windowId": window.WindowID, "bounds": map[string]any{"width": width + int(chromeW), "height": height + int(chromeH), "windowState": "normal"}}, nil)
}

var browserMouseActions = map[string]string{"down": "mousePressed", "up": "mouseReleased", "move": "mouseMoved"}

func (v *browserView) input(ctx context.Context, events []domain.BrowserInputEvent) error {
	if len(events) > 200 {
		events = events[:200]
	}
	v.mu.Lock()
	width, height := v.width, v.height
	v.mu.Unlock()
	if width <= 0 || height <= 0 {
		width, height = 1280, 800
	}
	for _, ev := range events {
		x, y := clampUnit(ev.X)*width, clampUnit(ev.Y)*height
		var err error
		switch ev.Type {
		case "mouse":
			action := browserMouseActions[ev.Action]
			if action == "" {
				continue
			}
			button := ev.Button
			if button == "" {
				button = "none"
			}
			err = v.pageCall(ctx, "Input.dispatchMouseEvent", map[string]any{"type": action, "x": x, "y": y, "button": button, "buttons": ev.Buttons, "clickCount": ev.ClickCount, "modifiers": ev.Modifiers})
		case "wheel":
			err = v.pageCall(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": x, "y": y, "deltaX": ev.DeltaX, "deltaY": ev.DeltaY, "modifiers": ev.Modifiers})
		case "key":
			params := map[string]any{"key": ev.Key, "code": ev.Code, "windowsVirtualKeyCode": ev.KeyCode, "nativeVirtualKeyCode": ev.KeyCode, "modifiers": ev.Modifiers}
			switch ev.Action {
			case "down":
				params["type"] = "rawKeyDown"
				if ev.Text != "" {
					params["type"], params["text"], params["unmodifiedText"] = "keyDown", ev.Text, ev.Text
				}
			case "up":
				params["type"] = "keyUp"
			default:
				continue
			}
			err = v.pageCall(ctx, "Input.dispatchKeyEvent", params)
		case "text":
			if ev.Text != "" {
				err = v.pageCall(ctx, "Input.insertText", map[string]any{"text": ev.Text})
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func clampUnit(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
