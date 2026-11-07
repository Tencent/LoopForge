import React, {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import "./session-context-menu.css";

export default function SessionContextMenu({
  x,
  y,
  anchor,
  activeSessionId,
  onClose,
  children,
}) {
  const root = useRef(null);
  const closeTimer = useRef();
  const closing = useRef(false);
  const closeCallback = useRef(onClose);
  const initialSession = useRef(activeSessionId);
  const [position, setPosition] = useState({ left: x, top: y });
  const [visible, setVisible] = useState(false);
  closeCallback.current = onClose;

  const close = useCallback(
    (returnFocus = false) => {
      if (closing.current) return;
      closing.current = true;
      setVisible(false);
      if (returnFocus && anchor?.isConnected) anchor.focus();
      const reduced = window.matchMedia(
        "(prefers-reduced-motion: reduce)",
      ).matches;
      closeTimer.current = window.setTimeout(
        () => closeCallback.current(),
        reduced ? 0 : 160,
      );
    },
    [anchor],
  );

  useLayoutEffect(() => {
    const bounds = root.current.getBoundingClientRect();
    const viewport = window.visualViewport;
    const left = viewport?.offsetLeft || 0;
    const top = viewport?.offsetTop || 0;
    const width = viewport?.width || window.innerWidth;
    const height = viewport?.height || window.innerHeight;
    setPosition({
      left: Math.max(left + 8, Math.min(x, left + width - bounds.width - 8)),
      top: Math.max(top + 8, Math.min(y, top + height - bounds.height - 8)),
    });
    root.current
      .querySelector('[role="menuitem"]:not(:disabled)')
      ?.focus({ preventScroll: true });
    const frame = window.requestAnimationFrame(() => setVisible(true));
    return () => window.cancelAnimationFrame(frame);
  }, [x, y]);

  useEffect(() => {
    if (activeSessionId !== initialSession.current) close();
  }, [activeSessionId, close]);

  useEffect(() => {
    const documents = [document];
    try {
      if (window.parent !== window && window.parent.document !== document)
        documents.push(window.parent.document);
    } catch {
      /* The embedded workbench can also run across origins. */
    }
    const outside = (event) => {
      if (!root.current?.contains(event.target)) close();
    };
    const scroll = (event) => {
      if (!root.current?.contains(event.target)) close();
    };
    const resize = () => close();
    const key = (event) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        close(true);
        return;
      }
      if (!root.current?.contains(document.activeElement)) return;
      if (event.key === "Tab") {
        close(true);
        return;
      }
      if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
      const items = [
        ...root.current.querySelectorAll('[role="menuitem"]:not(:disabled)'),
      ];
      if (!items.length) return;
      event.preventDefault();
      const current = items.indexOf(document.activeElement);
      const next =
        event.key === "Home"
          ? 0
          : event.key === "End"
            ? items.length - 1
            : (current + (event.key === "ArrowDown" ? 1 : -1) + items.length) %
              items.length;
      items[next].focus();
    };
    for (const doc of documents) {
      doc.addEventListener("pointerdown", outside, true);
      doc.addEventListener("scroll", scroll, true);
      doc.addEventListener("keydown", key, true);
      doc.defaultView?.addEventListener("resize", resize);
    }
    window.visualViewport?.addEventListener("resize", resize);
    window.visualViewport?.addEventListener("scroll", resize);
    return () => {
      window.clearTimeout(closeTimer.current);
      for (const doc of documents) {
        doc.removeEventListener("pointerdown", outside, true);
        doc.removeEventListener("scroll", scroll, true);
        doc.removeEventListener("keydown", key, true);
        doc.defaultView?.removeEventListener("resize", resize);
      }
      window.visualViewport?.removeEventListener("resize", resize);
      window.visualViewport?.removeEventListener("scroll", resize);
    };
  }, [close]);

  return createPortal(
    <div
      ref={root}
      id="session-context-menu"
      className="session-context-menu"
      role="menu"
      aria-label="会话操作"
      data-visible={visible}
      style={position}
      onContextMenu={(event) => {
        event.preventDefault();
        event.stopPropagation();
      }}
      onClick={(event) => {
        if (event.target.closest('[role="menuitem"]')) close();
      }}
    >
      {children}
    </div>,
    document.body,
  );
}
