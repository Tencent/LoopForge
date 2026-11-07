import { useLayoutEffect, useRef, useState } from "react";
import "./artifact-layout.css";

const storageKey = "agentflow.artifactPanelWidth";
const defaultWidth = 440;
const sashWidth = 4;
const snapDistance = 16;

function readWidth() {
  try {
    const saved = localStorage.getItem(storageKey);
    const width = Number(saved);
    return {
      preferred: Number.isFinite(width) && width > snapDistance ? width : defaultWidth,
      covered: saved === "cover",
    };
  } catch {
    return { preferred: defaultWidth, covered: false };
  }
}

function capturePassage(shell) {
  const scroll = shell?.querySelector(".conversation-main .message-scroll");
  if (!scroll) return () => {};
  const nearBottom = scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 56;
  const top = scroll.getBoundingClientRect().top;
  const anchor = [...scroll.querySelectorAll("[data-message-index]")]
    .find((message) => message.getBoundingClientRect().bottom > top);
  const offset = anchor ? anchor.getBoundingClientRect().top - top : 0;
  const scrollTop = scroll.scrollTop;
  return () => {
    if (!scroll.isConnected) return;
    if (nearBottom) scroll.scrollTop = scroll.scrollHeight;
    else if (anchor?.isConnected)
      scroll.scrollTop += anchor.getBoundingClientRect().top - scroll.getBoundingClientRect().top - offset;
    else scroll.scrollTop = scrollTop;
  };
}

// Pane sizing and keyboard behavior adapted from Grasp (MIT).
// See artifact-preview.css for the upstream license.
/** Grasp's 440px pane, 4px sash and 800px conversation breakpoint. */
export function useArtifactPanelLayout(root, onClose, nodeId = null, open = true) {
  const [layout, setLayout] = useState(readWidth);
  const [available, setAvailable] = useState(0);
  const [dragging, setDragging] = useState(false);
  const shell = useRef(null);
  const returnFocus = useRef(null);
  const closeRef = useRef(onClose);
  const openRef = useRef(open);
  const pointer = useRef(null);
  const nodeLayout = useRef(null);
  const current = useRef(null);
  closeRef.current = onClose;
  openRef.current = open;
  const compact = available > 0 && available < 800;
  const maxWidth = available ? Math.max(0, Math.round(available - sashWidth)) : defaultWidth;
  const width = compact ? available : layout.covered ? maxWidth : Math.min(layout.preferred, maxWidth);
  current.current = { ...layout, width, compact, maxWidth };

  function save(next) {
    try {
      localStorage.setItem(storageKey, next.covered ? "cover" : String(next.preferred));
    } catch {}
  }
  function change(next, persist = true) {
    const restore = capturePassage(shell.current);
    current.current = { ...current.current, ...next };
    setLayout(next);
    if (persist) save(next);
    requestAnimationFrame(restore);
  }
  function close() {
    if (current.current.covered)
      save({ preferred: defaultWidth, covered: false });
    setDragging(false);
    closeRef.current();
  }
  function commitWidth(next, persist = false) {
    if (!Number.isFinite(next)) return;
    const rounded = Math.round(next);
    if (rounded <= snapDistance) {
      save({ preferred: defaultWidth, covered: false });
      close();
      return;
    }
    change(
      current.current.maxWidth - rounded <= snapDistance
        ? { preferred: defaultWidth, covered: true }
        : { preferred: Math.min(rounded, current.current.maxWidth), covered: false },
      persist,
    );
  }
  function resetWidth() {
    change({ preferred: defaultWidth, covered: false });
  }
  function toggleCovered() {
    change({ preferred: defaultWidth, covered: !current.current.covered });
  }
  function startResize(event) {
    if (!openRef.current || event.button !== 0 || current.current.compact) return;
    event.preventDefault();
    pointer.current = { x: event.clientX, width: current.current.width };
    event.currentTarget.setPointerCapture(event.pointerId);
    setDragging(true);
  }
  function resize(event) {
    if (!pointer.current) return;
    commitWidth(pointer.current.width + pointer.current.x - event.clientX);
  }
  function finishResize() {
    if (!pointer.current) return;
    pointer.current = null;
    setDragging(false);
    save(current.current);
  }
  function resizeByKey(event) {
    if (!["ArrowLeft", "ArrowRight", "Home", "End", "Enter"].includes(event.key)) return;
    event.preventDefault();
    if (event.key === "Home") {
      save({ preferred: defaultWidth, covered: false });
      close();
    } else if (event.key === "End") change({ preferred: defaultWidth, covered: true });
    else if (event.key === "Enter") resetWidth();
    else commitWidth(current.current.width + (event.key === "ArrowLeft" ? 24 : -24), true);
  }

  useLayoutEffect(() => {
    const node = root.current;
    const host = node?.closest(".chat-shell");
    if (!node || !host) return;
    shell.current = host;
    returnFocus.current = document.activeElement;
    const toolbar = host.querySelector(".conversation-header");
    const measure = () => {
      const restore = capturePassage(host);
      setAvailable(toolbar?.getBoundingClientRect().width || host.clientWidth);
      requestAnimationFrame(restore);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(toolbar || host);
    const onKey = (event) => {
      if (!openRef.current || event.defaultPrevented || document.querySelector(".menu, .dialog")) return;
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        close();
      } else if (event.key === "Tab" && current.current.compact) {
        const controls = [...node.querySelectorAll(
          'button:not(:disabled), a[href], input:not(:disabled), textarea:not(:disabled), select:not(:disabled), iframe, [tabindex="0"]',
        )].filter((control) => control.getClientRects().length);
        const first = controls[0], last = controls.at(-1);
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last?.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first?.focus();
        }
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      const restore = capturePassage(host);
      const restoreFocus = node.contains(document.activeElement) ||
        document.activeElement?.matches(".artifact-panel-sash") || document.activeElement === document.body;
      if (nodeLayout.current)
        save(nodeLayout.current.covered ? { preferred: defaultWidth, covered: false } : nodeLayout.current);
      else if (current.current.covered) save({ preferred: defaultWidth, covered: false });
      observer.disconnect();
      document.removeEventListener("keydown", onKey);
      host.classList.remove("artifact-compact", "artifact-covered", "artifact-resizing");
      host.style.removeProperty("--panel-width");
      host.style.removeProperty("--artifact-canvas-width");
      const main = host.querySelector(".conversation-main");
      if (main) {
        main.inert = false;
        main.removeAttribute("aria-hidden");
      }
      requestAnimationFrame(() => {
        restore();
        const previous = returnFocus.current;
        const target = previous?.isConnected && !node.contains(previous)
          ? previous : host.querySelector(".panel-toggle");
        if (restoreFocus && !host.querySelector(".artifact-panel")) target?.focus({ preventScroll: true });
      });
    };
  }, []);

  useLayoutEffect(() => {
    const host = shell.current;
    if (!host) return;
    host.style.setProperty("--panel-width", `${width}px`);
    host.style.setProperty("--artifact-canvas-width", `${available}px`);
    host.classList.toggle("artifact-compact", compact);
    host.classList.toggle("artifact-covered", open && layout.covered && !compact);
    host.classList.toggle("artifact-resizing", open && dragging);
    const main = host.querySelector(".conversation-main");
    if (main) {
      // The compact overlay keeps the passage inert until its exit finishes.
      // The desktop passage may expand and receive input as the pane closes.
      main.inert = compact || (open && layout.covered);
      if (main.inert) main.setAttribute("aria-hidden", "true");
      else main.removeAttribute("aria-hidden");
    }
    // App owns the shell's React className. Reapply layout-only classes after
    // every commit so a presence phase change cannot remove the compact mode.
  });

  useLayoutEffect(() => {
    if (!open) {
      pointer.current = null;
      setDragging(false);
      const node = root.current;
      if (node?.contains(document.activeElement) ||
          document.activeElement?.matches(".artifact-panel-sash") ||
          document.activeElement === document.body)
        shell.current?.querySelector(".panel-toggle")?.focus({ preventScroll: true });
    }
  }, [open]);

  useLayoutEffect(() => {
    if (!compact || !open) return;
    const frame = requestAnimationFrame(() => {
      root.current?.querySelector('[aria-label="返回聊天"], button')?.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [compact, open]);

  useLayoutEffect(() => {
    if (nodeId && !compact && !nodeLayout.current) {
      nodeLayout.current = { preferred: current.current.preferred, covered: current.current.covered };
      if (!current.current.covered)
        change({ preferred: Math.max(current.current.width, Math.min(820, innerWidth * .48)), covered: false }, false);
    } else if (!nodeId && nodeLayout.current) {
      const previous = nodeLayout.current;
      nodeLayout.current = null;
      change(previous);
    }
  }, [nodeId, compact]);

  return {
    width, compact, covered: layout.covered, dragging, maxWidth,
    startResize, resize, finishResize, resizeByKey, resetWidth, toggleCovered,
  };
}
