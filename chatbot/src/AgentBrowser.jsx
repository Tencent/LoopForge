import React, { useEffect, useRef, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import { api } from "./api";
import "./agent-browser.css";

const modifiers = (e) =>
  (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0);
const keyText = { Enter: "\r", Tab: "\t" };
const buttonName = (b) => (b === 1 ? "middle" : b === 2 ? "right" : "left");
const deniedError = "无权操作";
const startingText = "正在启动 Agent 浏览器…";

// AgentBrowser shows the Agent browser of the conversation's workspace: the
// browser Main Bot drives with workbench_browser_*, running on the execution
// machine next to the workspace. The user watches it live and can take over.
// Protocol and input handling follow the task panel in web/collaboration.js:
// one WebSocket carries frames and input, plain polling is the fallback.
export default function AgentBrowser({ runId, request = null, onRequestHandled = () => {} }) {
  const stageRef = useRef(null);
  const screenRef = useRef(null);
  const imeRef = useRef(null);
  const addressRef = useRef(null);
  const controller = useRef(null);
  const [page, setPage] = useState({ available: false, canGoBack: false, canGoForward: false, tabs: [] });
  const [overlay, setOverlay] = useState({ text: "正在连接 Agent 浏览器…", tone: "idle" });
  const [status, setStatus] = useState("连接中");
  const [notice, setNotice] = useState("");
  const [address, setAddress] = useState("");
  const [denied, setDenied] = useState(false);

  useEffect(() => {
    if (!runId) return undefined;
    const stage = stageRef.current;
    const screen = screenRef.current;
    const ime = imeRef.current;
    const endpoint = `/api/runs/${encodeURIComponent(runId)}/browser`;
    const call = (body) => api(endpoint, { method: "POST", body: JSON.stringify(body) });
    let alive = true;
    let seq = 0, state = null, queue = [], sending = false, lastInput = 0;
    let moveTimer = 0, pendingMove = null, ws = null, wsFails = 0, frameURL = "";
    let starting = 0, ready = false, readOnly = false, polling = false;
    let pendingNavigate = "", noticeTimer = 0, reconnectTimer = 0, sizeTimer = 0, sentSize = "";
    // A hidden page stops streaming; the browser keeps running on the executor.
    const live = () => alive && !document.hidden;

    function notify(message) {
      if (!alive || !message) return;
      if (message.includes(deniedError)) {
        // Viewers of the project see the browser but cannot drive it.
        readOnly = true;
        queue = [];
        pendingNavigate = "";
        setDenied(true);
        if (state) paint(state);
        return;
      }
      setNotice(message);
      clearTimeout(noticeTimer);
      noticeTimer = setTimeout(() => alive && setNotice(""), 6000);
    }
    function paint(next) {
      if (!alive || !next) return;
      state = next;
      if (!next.available) {
        if (starting && Date.now() - starting < 240000) setOverlay({ text: startingText, tone: "idle" });
        else {
          starting = 0;
          setOverlay({ text: next.reason || "Agent 浏览器尚未启动", tone: "idle", start: !readOnly });
        }
        setStatus("未连接");
        setPage((p) => ({ ...p, available: false }));
        return;
      }
      starting = 0;
      if (!ready) {
        ready = true;
        fit();
      }
      if (next.frame) {
        screen.src = `data:image/jpeg;base64,${next.frame}`;
        seq = next.seq;
      }
      setOverlay(screen.getAttribute("src") ? null : { text: "正在等待第一帧画面…", tone: "idle" });
      if (document.activeElement !== addressRef.current)
        setAddress(next.url && next.url !== "about:blank" ? next.url : "");
      setPage({
        available: true,
        canGoBack: !!next.canGoBack,
        canGoForward: !!next.canGoForward,
        tabs: next.tabs || [],
      });
      setStatus(
        `${next.kind === "docker" ? "沙箱浏览器" : "本机 Chromium"}${Date.now() - lastInput < 4000 ? " · 你正在操作" : " · 实时"}`,
      );
      if (pendingNavigate) {
        const url = pendingNavigate;
        pendingNavigate = "";
        command({ operation: "navigate", url });
      }
    }
    function send(body) {
      if (ws?.readyState !== WebSocket.OPEN) return false;
      ws.send(JSON.stringify(body));
      return true;
    }
    async function command(body) {
      if (readOnly) return;
      if (send(body)) return;
      try {
        paint(await call({ ...body, after: seq }));
      } catch (err) {
        notify(err.message);
      }
    }
    // Starting can take minutes when the workspace has to be restored first.
    async function startBrowser(body = { operation: "start" }) {
      if (readOnly || starting) return;
      starting = Date.now();
      setOverlay({ text: startingText, tone: "idle" });
      if (send({ ...body, ensure: true })) return;
      try {
        const next = await call({ ...body, ensure: true, after: seq });
        starting = 0;
        paint(next);
        if (!next.available) notify(next.reason || "Agent 浏览器未能启动");
      } catch (err) {
        starting = 0;
        notify(err.message);
        if (state) paint(state);
        else if (alive) setOverlay({ text: err.message, tone: "error", start: !readOnly });
      }
    }
    function navigate(url) {
      if (readOnly) {
        notify(deniedError);
        return;
      }
      if (state?.available) command({ operation: "navigate", url });
      else if (starting) pendingNavigate = url;
      else startBrowser({ operation: "navigate", url });
      stage.focus({ preventScroll: true });
    }

    // Frames that arrive while one is decoding are dropped; only the newest is shown next.
    let nextFrame = null, decoding = false;
    function showFrame(blob) {
      nextFrame = blob;
      if (!decoding) drawFrame();
    }
    function drawFrame() {
      const blob = nextFrame;
      nextFrame = null;
      decoding = !!blob && alive;
      if (!decoding) return;
      const url = URL.createObjectURL(blob);
      screen.onload = screen.onerror = () => {
        if (frameURL && frameURL !== url) URL.revokeObjectURL(frameURL);
        frameURL = url;
        if (alive && state?.available) setOverlay(null);
        drawFrame();
      };
      screen.src = url;
    }
    function connect() {
      if (!live() || ws) return;
      let opened = false;
      const socket = new WebSocket(
        `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}${endpoint}/stream`,
      );
      socket.binaryType = "blob";
      ws = socket;
      socket.onopen = () => {
        opened = true;
        wsFails = 0;
        sentSize = "";
        fit();
      };
      socket.onmessage = (e) => {
        if (!live()) {
          socket.close();
          return;
        }
        if (typeof e.data !== "string") {
          showFrame(e.data);
          return;
        }
        let msg;
        try {
          msg = JSON.parse(e.data);
        } catch {
          return;
        }
        if (msg.type === "state" && msg.state) paint(msg.state);
        else if (msg.type === "error") {
          notify(msg.error);
          if (starting) {
            starting = 0;
            if (state) paint(state);
          }
        }
      };
      socket.onclose = () => {
        if (ws === socket) ws = null;
        if (!live()) return;
        if (!opened && ++wsFails >= 2) {
          loop();
          return;
        }
        reconnectTimer = setTimeout(connect, opened ? 1000 : 2000);
      };
    }
    async function loop() {
      if (polling) return;
      polling = true;
      let delay = 0;
      try {
        while (live()) {
          if (delay) await new Promise((r) => setTimeout(r, delay));
          if (!live()) return;
          try {
            const next = await call({ operation: state ? "frame" : "status", after: seq, waitMs: 3000 });
            if (!live()) return;
            paint(next);
            delay = next.available ? 0 : 4000;
          } catch (err) {
            if (!live()) return;
            setOverlay({ text: err.message, tone: "error" });
            delay = 4000;
          }
        }
      } finally {
        polling = false;
      }
    }
    const resume = () => {
      if (document.hidden) {
        ws?.close();
        return;
      }
      if (typeof WebSocket !== "function" || wsFails >= 2) loop();
      else connect();
    };

    function flush() {
      if (readOnly) {
        queue = [];
        return;
      }
      if (sending || !queue.length || !alive) return;
      lastInput = Date.now();
      if (send({ operation: "input", events: queue })) {
        queue = [];
        return;
      }
      const events = queue;
      queue = [];
      sending = true;
      call({ operation: "input", events, after: seq })
        .then(paint, (err) => notify(err.message))
        .finally(() => {
          sending = false;
          flush();
        });
    }
    function push(ev) {
      if (readOnly) return;
      queue.push(ev);
      flush();
    }
    // Scrolling is shown locally first: the current frame slides by the pending
    // delta until a frame captured after that scroll replaces it.
    let wheel = null, wheelTimer = 0, settleTimer = 0, scrolls = [], shiftX = 0, shiftY = 0, frameLag = 80;
    function point(e) {
      const rect = screen.getBoundingClientRect();
      return {
        x: (e.clientX - rect.left + shiftX) / (rect.width || 1),
        y: (e.clientY - rect.top + shiftY) / (rect.height || 1),
      };
    }
    function applyShift() {
      const scale = (screen.getBoundingClientRect().width || 1) / (state?.width || 1);
      const limit = stage.clientHeight || 0;
      let dx = wheel?.deltaX || 0, dy = wheel?.deltaY || 0;
      for (const b of scrolls) {
        dx += b.dx;
        dy += b.dy;
      }
      shiftX = Math.max(-limit, Math.min(limit, -dx * scale));
      shiftY = Math.max(-limit, Math.min(limit, -dy * scale));
      screen.style.transform = shiftX || shiftY ? `translate(${shiftX}px,${shiftY}px)` : "";
    }
    function settleScroll(age) {
      const now = Date.now();
      scrolls = scrolls.filter((b) => now - b.at < age);
      applyShift();
    }
    function flushWheel() {
      wheelTimer = 0;
      if (!wheel) return;
      const ev = wheel;
      wheel = null;
      scrolls.push({ dx: ev.deltaX, dy: ev.deltaY, at: Date.now() });
      push(ev);
      applyShift();
      clearTimeout(settleTimer);
      settleTimer = setTimeout(() => settleScroll(400), 500);
    }
    // A frame newer than the round trip already includes the scroll.
    const onFrameLoad = () => {
      const now = Date.now(), first = scrolls.find((b) => !b.seen);
      if (first) {
        first.seen = true;
        frameLag = Math.round(frameLag * 0.7 + (now - first.at) * 0.3);
      }
      settleScroll(Math.max(50, Math.min(400, frameLag)));
    };
    const mouse = (e, action) => ({
      type: "mouse",
      action,
      ...point(e),
      button: buttonName(e.button),
      buttons: e.buttons,
      clickCount: e.detail || 1,
      modifiers: modifiers(e),
    });
    const onPointerDown = (e) => {
      if (!state?.available || (e.target !== screen && e.target !== stage)) return;
      e.preventDefault();
      stage.setPointerCapture?.(e.pointerId);
      ime.focus({ preventScroll: true });
      push(mouse(e, "down"));
    };
    const onPointerUp = (e) => {
      if (state?.available) push(mouse(e, "up"));
    };
    const onPointerMove = (e) => {
      if (!state?.available || readOnly) return;
      pendingMove = {
        type: "mouse",
        action: "move",
        ...point(e),
        button: e.buttons & 1 ? "left" : "none",
        buttons: e.buttons,
        modifiers: modifiers(e),
      };
      if (moveTimer) return;
      moveTimer = setTimeout(() => {
        moveTimer = 0;
        if (pendingMove) {
          push(pendingMove);
          pendingMove = null;
        }
      }, 60);
    };
    const onWheel = (e) => {
      if (!state?.available || readOnly) return;
      e.preventDefault();
      const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? state.height || 600 : 1;
      wheel = {
        type: "wheel",
        ...point(e),
        deltaX: (wheel?.deltaX || 0) + e.deltaX * unit,
        deltaY: (wheel?.deltaY || 0) + e.deltaY * unit,
        modifiers: modifiers(e),
      };
      applyShift();
      if (!wheelTimer) wheelTimer = setTimeout(flushWheel, 40);
    };
    const onContextMenu = (e) => e.preventDefault();
    const onStageFocus = () => ime.focus({ preventScroll: true });
    const onKeyDown = (e) => {
      if (e.isComposing || e.keyCode === 229) return;
      if ((e.metaKey || e.ctrlKey) && ["v", "c", "x"].includes(e.key.toLowerCase())) return;
      // Handled here so Escape or Tab reach the page instead of closing the panel.
      e.preventDefault();
      const text = keyText[e.key] || (e.key.length === 1 && !e.ctrlKey && !e.metaKey ? e.key : "");
      push({ type: "key", action: "down", key: e.key, code: e.code, keyCode: e.keyCode, text, modifiers: modifiers(e) });
    };
    const onKeyUp = (e) => {
      if (e.isComposing || e.keyCode === 229) return;
      push({ type: "key", action: "up", key: e.key, code: e.code, keyCode: e.keyCode, modifiers: modifiers(e) });
    };
    const onCompositionStart = () => {
      ime.dataset.composing = "1";
    };
    const onCompositionEnd = (e) => {
      delete ime.dataset.composing;
      if (e.data) push({ type: "text", text: e.data });
      ime.value = "";
    };
    const onPaste = (e) => {
      e.preventDefault();
      const text = e.clipboardData?.getData("text/plain");
      if (text) push({ type: "text", text });
    };
    const onInput = (e) => {
      if (ime.dataset.composing || e.isComposing || e.inputType === "insertCompositionText") return;
      if (ime.value) push({ type: "text", text: ime.value });
      ime.value = "";
    };
    function fit() {
      clearTimeout(sizeTimer);
      sizeTimer = setTimeout(() => {
        if (!alive || readOnly) return;
        const rect = stage.getBoundingClientRect();
        if (!state?.available || rect.width < 120 || rect.height < 120) return;
        const width = Math.round(Math.min(1600, Math.max(1024, rect.width)));
        const height = Math.round(Math.min(2000, Math.max(500, (width * rect.height) / rect.width)));
        const frameWidth = Math.round(rect.width * Math.min(window.devicePixelRatio || 1, 1.5));
        const key = `${width}x${height}@${frameWidth}`;
        if (key === sentSize) return;
        sentSize = key;
        command({ operation: "resize", width, height, frameWidth });
      }, 400);
    }

    const listeners = [
      [stage, "pointerdown", onPointerDown],
      [stage, "pointerup", onPointerUp],
      [stage, "pointermove", onPointerMove],
      [stage, "wheel", onWheel, { passive: false }],
      [stage, "contextmenu", onContextMenu],
      [stage, "focus", onStageFocus],
      [screen, "load", onFrameLoad],
      [ime, "keydown", onKeyDown],
      [ime, "keyup", onKeyUp],
      [ime, "compositionstart", onCompositionStart],
      [ime, "compositionend", onCompositionEnd],
      [ime, "paste", onPaste],
      [ime, "input", onInput],
      [document, "visibilitychange", resume],
    ];
    for (const [target, type, fn, options] of listeners) target.addEventListener(type, fn, options);
    const observer = typeof ResizeObserver === "function" ? new ResizeObserver(fit) : null;
    observer?.observe(stage);
    controller.current = { command, navigate, start: () => startBrowser() };
    if (typeof WebSocket === "function") connect();
    else loop();
    return () => {
      alive = false;
      controller.current = null;
      for (const [target, type, fn, options] of listeners) target.removeEventListener(type, fn, options);
      observer?.disconnect();
      for (const timer of [noticeTimer, reconnectTimer, sizeTimer, moveTimer, wheelTimer, settleTimer]) clearTimeout(timer);
      screen.onload = screen.onerror = null;
      ws?.close();
      ws = null;
      if (frameURL) URL.revokeObjectURL(frameURL);
    };
  }, [runId]);

  // A link the user clicked in the conversation. App clears the request once
  // handled, so reopening the tab does not navigate again.
  useEffect(() => {
    if (!request?.url || !controller.current) return;
    controller.current.navigate(request.url);
    onRequestHandled(request.id);
  }, [request?.id, request?.url, runId]);

  const disabled = !page.available || denied;
  const navButton = (icon, label, operation, off = disabled) => (
    <button
      type="button"
      className="agent-browser-btn"
      title={label}
      aria-label={label}
      disabled={off}
      onClick={() => controller.current?.command({ operation })}
    >
      <HarnessIcon name={icon} size={15} />
    </button>
  );
  return (
    <div className="agent-browser">
      <div className="agent-browser-bar">
        {navButton("arrow-left", "后退", "back", disabled || !page.canGoBack)}
        {navButton("arrow-right", "前进", "forward", disabled || !page.canGoForward)}
        {navButton("refresh", "刷新", "reload")}
        <form
          className="agent-browser-address"
          onSubmit={(event) => {
            event.preventDefault();
            const target = address.trim();
            if (target) controller.current?.navigate(target);
          }}
        >
          <HarnessIcon name="globe" size={13} />
          <input
            ref={addressRef}
            type="text"
            spellCheck={false}
            placeholder="输入网址或搜索内容"
            aria-label="网址"
            value={address}
            readOnly={denied}
            onChange={(event) => setAddress(event.target.value)}
          />
        </form>
        {navButton("plus", "新标签页", "newTab")}
      </div>
      {page.tabs.length > 1 ? (
        <div className="agent-browser-tabs" role="tablist" aria-label="浏览器标签页">
          {page.tabs.map((tab) => (
            <div
              key={tab.id}
              className={`agent-browser-tab${tab.active ? " is-active" : ""}`}
              role="tab"
              aria-selected={!!tab.active}
              title={tab.url || ""}
            >
              <button
                type="button"
                disabled={denied}
                onClick={() => controller.current?.command({ operation: "activate", targetId: tab.id })}
              >
                {tab.title || tab.url || "新标签页"}
              </button>
              <button
                type="button"
                className="agent-browser-tab-close"
                title="关闭标签页"
                aria-label="关闭标签页"
                disabled={denied}
                onClick={() => controller.current?.command({ operation: "closeTab", targetId: tab.id })}
              >
                <HarnessIcon name="close" size={11} />
              </button>
            </div>
          ))}
        </div>
      ) : null}
      <div
        ref={stageRef}
        className={`agent-browser-stage${page.available ? "" : " is-offline"}`}
        tabIndex={0}
        aria-label={denied ? "Agent 浏览器画面（只读）" : "Agent 浏览器画面，点击后可直接操作"}
      >
        <img ref={screenRef} className="agent-browser-screen" alt="" draggable={false} />
        <textarea
          ref={imeRef}
          className="agent-browser-ime"
          aria-hidden="true"
          tabIndex={-1}
          autoCapitalize="off"
          spellCheck={false}
        />
        {overlay ? (
          <div className="agent-browser-overlay" data-tone={overlay.tone}>
            <span>{overlay.text}</span>
            {overlay.start && !denied ? (
              <button type="button" className="agent-browser-start" onClick={() => controller.current?.start()}>
                <HarnessIcon name="globe" size={14} />
                启动浏览器
              </button>
            ) : null}
          </div>
        ) : null}
      </div>
      <div className="agent-browser-foot">
        <span>{status}</span>
        <small role="status" data-tone={notice ? "error" : undefined}>
          {notice ||
            (denied
              ? "你没有该项目的编辑权限，只能查看画面"
              : "与 Agent 用的是同一个浏览器，点击画面即可接管操作")}
        </small>
      </div>
    </div>
  );
}
