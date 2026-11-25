package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const (
	browserSnapshotElements = 150
	browserSnapshotText     = 12000
	browserRefAttr          = "data-agentflow-ref"
)

// browserSnapshotScript tags visible interactive elements with refs so a
// language model can act on them without coordinates.
var browserSnapshotScript = `(() => {
  const attr = "` + browserRefAttr + `", max = ` + strconv.Itoa(browserSnapshotElements) + `, limit = ` + strconv.Itoa(browserSnapshotText) + `;
  document.querySelectorAll("[" + attr + "]").forEach(e => e.removeAttribute(attr));
  const visible = e => { const r = e.getBoundingClientRect(); if (r.width < 1 || r.height < 1) return false; const s = getComputedStyle(e); return s.visibility !== "hidden" && s.display !== "none"; };
  const clean = v => String(v || "").replace(/\s+/g, " ").trim().slice(0, 80);
  const selector = 'a[href],button,input:not([type=hidden]),textarea,select,summary,[role=button],[role=link],[role=tab],[role=menuitem],[role=checkbox],[role=radio],[role=option],[role=combobox],[role=textbox],[role=switch],[contenteditable=""],[contenteditable=true],[onclick]';
  const elements = [];
  let n = 0;
  for (const e of document.querySelectorAll(selector)) {
    if (elements.length >= max) break;
    if (!visible(e)) continue;
    const ref = "e" + (++n);
    e.setAttribute(attr, ref);
    const tag = e.tagName.toLowerCase(), type = (e.getAttribute("type") || "").toLowerCase();
    const role = e.getAttribute("role") || (tag === "a" ? "link" : tag === "input" ? (type || "text") : tag);
    const buttonValue = tag === "input" && ["button", "submit", "reset"].includes(type) ? e.value : "";
    const item = { ref, role, name: clean(e.getAttribute("aria-label") || e.innerText || buttonValue || e.getAttribute("placeholder") || e.title || e.getAttribute("alt") || e.getAttribute("name")) };
    if ((tag === "input" && !["button", "submit", "reset", "password", "checkbox", "radio"].includes(type)) || tag === "textarea" || tag === "select") item.value = clean(e.value);
    if (e.checked) item.checked = true;
    if (e.disabled) item.disabled = true;
    elements.push(item);
  }
  const text = String(document.body ? document.body.innerText : "").replace(/\n{3,}/g, "\n\n").trim();
  return { text: text.slice(0, limit), truncated: text.length > limit, elements };
})()`

type cdpEvaluateResult struct {
	Result struct {
		Value json.RawMessage `json:"value"`
	} `json:"result"`
	ExceptionDetails *struct {
		Text      string `json:"text"`
		Exception struct {
			Description string `json:"description"`
		} `json:"exception"`
	} `json:"exceptionDetails"`
}

func (v *browserView) evaluate(ctx context.Context, expression string, out any) error {
	v.mu.Lock()
	session := v.session
	v.mu.Unlock()
	if session == "" {
		return errors.New("没有可操作的标签页")
	}
	var result cdpEvaluateResult
	if err := v.client.call(ctx, session, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}, &result); err != nil {
		return err
	}
	if result.ExceptionDetails != nil {
		msg := result.ExceptionDetails.Exception.Description
		if msg == "" {
			msg = result.ExceptionDetails.Text
		}
		return fmt.Errorf("页面脚本执行失败：%s", msg)
	}
	if out == nil || len(result.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(result.Result.Value, out)
}

func (v *browserView) snapshot(ctx context.Context) (*domain.BrowserSnapshot, error) {
	var snap domain.BrowserSnapshot
	if err := v.evaluate(ctx, browserSnapshotScript, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// settle waits briefly for navigation triggered by an action to finish.
func (v *browserView) settle(ctx context.Context) {
	timer := time.NewTimer(400 * time.Millisecond)
	select {
	case <-ctx.Done():
		timer.Stop()
		return
	case <-timer.C:
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		var ready string
		err := v.evaluate(probe, "document.readyState", &ready)
		cancel()
		if err == nil && ready == "complete" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func browserRefSelector(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if len(ref) < 2 || ref[0] != 'e' {
		return "", errors.New("ref 无效：请先获取页面快照，使用其中的元素 ref（如 e12）")
	}
	if _, err := strconv.Atoi(ref[1:]); err != nil {
		return "", errors.New("ref 无效：请先获取页面快照，使用其中的元素 ref（如 e12）")
	}
	return `document.querySelector('[` + browserRefAttr + `="` + ref + `"]')`, nil
}

// clickRef clicks the element center with real mouse events, the same way a
// user click from the task panel reaches the page.
func (v *browserView) clickRef(ctx context.Context, ref string) error {
	selector, err := browserRefSelector(ref)
	if err != nil {
		return err
	}
	var point *struct{ X, Y float64 }
	script := `(() => { const e = ` + selector + `; if (!e) return null; e.scrollIntoView({block: "center", inline: "center"}); const r = e.getBoundingClientRect(); return {x: r.left + r.width / 2, y: r.top + r.height / 2}; })()`
	if err := v.evaluate(ctx, script, &point); err != nil {
		return err
	}
	if point == nil {
		return fmt.Errorf("找不到元素 %s：页面可能已变化，请重新获取快照", ref)
	}
	for _, step := range []map[string]any{
		{"type": "mouseMoved", "x": point.X, "y": point.Y},
		{"type": "mousePressed", "x": point.X, "y": point.Y, "button": "left", "buttons": 1, "clickCount": 1},
		{"type": "mouseReleased", "x": point.X, "y": point.Y, "button": "left", "clickCount": 1},
	} {
		if err := v.pageCall(ctx, "Input.dispatchMouseEvent", step); err != nil {
			return err
		}
	}
	return nil
}

func (v *browserView) typeRef(ctx context.Context, ref, text string, submit bool) error {
	if err := v.clickRef(ctx, ref); err != nil {
		return err
	}
	selector, _ := browserRefSelector(ref)
	script := `(() => { const e = ` + selector + `; if (!e) return false; e.focus(); if (typeof e.select === "function") e.select(); else { const r = document.createRange(); r.selectNodeContents(e); const s = getSelection(); s.removeAllRanges(); s.addRange(r); } return true; })()`
	var found bool
	if err := v.evaluate(ctx, script, &found); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("找不到元素 %s：页面可能已变化，请重新获取快照", ref)
	}
	if text == "" {
		if err := v.press(ctx, "Backspace"); err != nil {
			return err
		}
	} else if err := v.pageCall(ctx, "Input.insertText", map[string]any{"text": text}); err != nil {
		return err
	}
	if submit {
		return v.press(ctx, "Enter")
	}
	return nil
}

var browserKeys = map[string]struct {
	code    string
	keyCode int
	text    string
}{
	"Enter": {"Enter", 13, "\r"}, "Tab": {"Tab", 9, ""}, "Escape": {"Escape", 27, ""}, "Backspace": {"Backspace", 8, ""}, "Delete": {"Delete", 46, ""},
	"ArrowUp": {"ArrowUp", 38, ""}, "ArrowDown": {"ArrowDown", 40, ""}, "ArrowLeft": {"ArrowLeft", 37, ""}, "ArrowRight": {"ArrowRight", 39, ""},
	"PageUp": {"PageUp", 33, ""}, "PageDown": {"PageDown", 34, ""}, "Home": {"Home", 36, ""}, "End": {"End", 35, ""}, " ": {"Space", 32, " "},
}

func (v *browserView) press(ctx context.Context, key string) error {
	if strings.EqualFold(key, "space") {
		key = " "
	}
	spec, ok := browserKeys[key]
	if !ok {
		return fmt.Errorf("不支持的按键 %q", key)
	}
	down := map[string]any{"type": "rawKeyDown", "key": key, "code": spec.code, "windowsVirtualKeyCode": spec.keyCode, "nativeVirtualKeyCode": spec.keyCode}
	if spec.text != "" {
		down["type"], down["text"], down["unmodifiedText"] = "keyDown", spec.text, spec.text
	}
	if err := v.pageCall(ctx, "Input.dispatchKeyEvent", down); err != nil {
		return err
	}
	return v.pageCall(ctx, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key, "code": spec.code, "windowsVirtualKeyCode": spec.keyCode, "nativeVirtualKeyCode": spec.keyCode})
}

func (v *browserView) screenshot(ctx context.Context) (string, error) {
	v.mu.Lock()
	session := v.session
	v.mu.Unlock()
	if session == "" {
		return "", errors.New("没有可操作的标签页")
	}
	// The browser renders at a high device scale for the panel; the model
	// gets CSS-pixel screenshots, which carry the same content in a quarter
	// of the image.
	params := map[string]any{"format": "jpeg", "quality": 75}
	var metrics struct {
		CSSVisualViewport struct {
			PageX        float64 `json:"pageX"`
			PageY        float64 `json:"pageY"`
			ClientWidth  float64 `json:"clientWidth"`
			ClientHeight float64 `json:"clientHeight"`
		} `json:"cssVisualViewport"`
	}
	var ratio struct {
		Result struct {
			Value float64 `json:"value"`
		} `json:"result"`
	}
	if v.client.call(ctx, session, "Page.getLayoutMetrics", nil, &metrics) == nil &&
		v.client.call(ctx, session, "Runtime.evaluate", map[string]any{"expression": "window.devicePixelRatio", "returnByValue": true}, &ratio) == nil &&
		ratio.Result.Value > 1 && metrics.CSSVisualViewport.ClientWidth > 0 {
		vp := metrics.CSSVisualViewport
		params["clip"] = map[string]any{"x": vp.PageX, "y": vp.PageY, "width": vp.ClientWidth, "height": vp.ClientHeight, "scale": 1 / ratio.Result.Value}
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := v.client.call(ctx, session, "Page.captureScreenshot", params, &shot); err != nil {
		return "", err
	}
	return shot.Data, nil
}

// agentOperation runs one language-model operation and, for page-changing
// actions, returns the resulting snapshot so refs stay current.
func (v *browserView) agentOperation(ctx context.Context, req domain.BrowserRequest) (*domain.BrowserSnapshot, string, error) {
	var err error
	switch req.Operation {
	case "snapshot":
	case "screenshot":
		frame, err := v.screenshot(ctx)
		return nil, frame, err
	case "click":
		err = v.clickRef(ctx, req.Ref)
	case "type":
		err = v.typeRef(ctx, req.Ref, req.Text, req.Submit)
	case "press":
		err = v.press(ctx, req.Key)
	}
	if err != nil {
		return nil, "", err
	}
	if req.Operation != "snapshot" {
		v.settle(ctx)
	}
	snap, err := v.snapshot(ctx)
	return snap, "", err
}
