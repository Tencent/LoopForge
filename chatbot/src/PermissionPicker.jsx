import React, { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import HarnessIcon from "./HarnessIcon";
import { usePanelPresence } from "./usePanelPresence";
import "./permission-picker.css";

const modes = [
  { id: "confirm", icon: "gate", label: "每次确认", detail: "平台写入先生成草稿，由你确认执行。" },
  { id: "assisted", icon: "confirmation-check", label: "帮我批准", detail: "AI 审查具体操作与对话授权，通过后执行；未通过时交给你确认。" },
  { id: "auto", icon: "priority-bolt", label: "完全权限", tone: "danger", detail: "使用当前账号权限直接执行模型提出的平台操作。" },
];

export default function PermissionPicker({ value, onSelect, open, onOpenChange, label = "操作确认方式", icon, options = modes }) {
  const trigger = useRef(null);
  const panel = useRef(null);
  const initialFocus = useRef(null);
  const callbacks = useRef(null);
  callbacks.current = { onSelect, onOpenChange };
  const identity = `permission-picker-${useId().replaceAll(":", "")}`;
  const presence = usePanelPresence(open, 160);
  const [position, setPosition] = useState({ top: 0, left: 0, width: 260, maxHeight: 360, placement: "top" });
  const current = options.find((mode) => mode.id === value) || options[0];

  const place = useCallback(() => {
    if (!trigger.current || !panel.current) return;
    const viewport = window.visualViewport;
    const minX = (viewport?.offsetLeft || 0) + 8;
    const minY = (viewport?.offsetTop || 0) + 8;
    const maxX = minX + (viewport?.width || window.innerWidth) - 16;
    const maxY = minY + (viewport?.height || window.innerHeight) - 16;
    const anchor = trigger.current.getBoundingClientRect();
    const width = Math.max(0, Math.min(260, maxX - minX));
    const desiredHeight = panel.current.scrollHeight + 2;
    const above = Math.max(0, Math.min(anchor.top, maxY) - minY - 6);
    const below = Math.max(0, maxY - Math.max(anchor.bottom, minY) - 6);
    const placement = above >= desiredHeight || above >= below ? "top" : "bottom";
    const maxHeight = Math.max(0, Math.min(maxY - minY, placement === "top" ? above : below));
    const height = Math.min(desiredHeight, maxHeight);
    const next = {
      width,
      maxHeight,
      placement,
      left: Math.max(minX, Math.min(anchor.left, maxX - width)),
      top: Math.max(minY, Math.min(maxY - height, placement === "top" ? anchor.top - height - 6 : anchor.bottom + 6)),
    };
    setPosition((old) => Object.keys(next).every((key) => old[key] === next[key]) ? old : next);
  }, []);

  useLayoutEffect(() => {
    if (presence.mounted) place();
  }, [presence.mounted, options.length, place]);

  useEffect(() => {
    if (!open || !presence.mounted) return;
    const frame = requestAnimationFrame(() => {
      const items = [...(panel.current?.querySelectorAll('[role="menuitemradio"]') || [])];
      const selected = initialFocus.current === "first" ? items[0] :
        initialFocus.current === "last" ? items.at(-1) :
          items.find((item) => item.getAttribute("aria-checked") === "true") || items[0];
      initialFocus.current = null;
      selected?.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [open, presence.mounted]);

  useEffect(() => {
    if (!presence.mounted) return;
    const observer = new ResizeObserver(place);
    if (trigger.current) observer.observe(trigger.current);
    if (panel.current) observer.observe(panel.current);
    const scroll = (event) => {
      if (!panel.current?.contains(event.target)) place();
    };
    window.addEventListener("resize", place);
    document.addEventListener("scroll", scroll, true);
    window.visualViewport?.addEventListener("resize", place);
    window.visualViewport?.addEventListener("scroll", place);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", place);
      document.removeEventListener("scroll", scroll, true);
      window.visualViewport?.removeEventListener("resize", place);
      window.visualViewport?.removeEventListener("scroll", place);
    };
  }, [presence.mounted, place]);

  useEffect(() => {
    if (!open) return;
    const inside = (target) => trigger.current?.contains(target) || panel.current?.contains(target);
    const outside = (event) => {
      if (!inside(event.target)) callbacks.current.onOpenChange(false);
    };
    const parentOutside = (event) => {
      if (event.target !== window.frameElement) callbacks.current.onOpenChange(false);
    };
    document.addEventListener("pointerdown", outside, true);
    document.addEventListener("focusin", outside, true);
    let parentDocument;
    try {
      if (window.parent !== window) {
        parentDocument = window.parent.document;
        parentDocument.addEventListener("pointerdown", parentOutside, true);
        parentDocument.addEventListener("focusin", parentOutside, true);
      }
    } catch {
      // A cross-origin host cannot expose its document to the embedded app.
    }
    return () => {
      document.removeEventListener("pointerdown", outside, true);
      document.removeEventListener("focusin", outside, true);
      parentDocument?.removeEventListener("pointerdown", parentOutside, true);
      parentDocument?.removeEventListener("focusin", parentOutside, true);
    };
  }, [open]);

  function close(returnFocus = false) {
    if (returnFocus) trigger.current?.focus({ preventScroll: true });
    callbacks.current.onOpenChange(false);
  }
  function choose(id) {
    callbacks.current.onSelect(id);
    close(true);
  }
  function keyDown(event) {
    if (event.isComposing || event.nativeEvent?.isComposing) return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      close(true);
      return;
    }
    if (event.key === "Tab") {
      // Continue from the trigger's place in the composer, not the body portal.
      trigger.current?.focus({ preventScroll: true });
      close();
      return;
    }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    const items = [...(panel.current?.querySelectorAll('[role="menuitemradio"]') || [])];
    if (!items.length) return;
    event.preventDefault();
    const index = items.indexOf(document.activeElement);
    const next = event.key === "Home" ? 0 : event.key === "End" ? items.length - 1 :
      index < 0 ? (event.key === "ArrowDown" ? 0 : items.length - 1) :
        (index + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
    items[next].focus({ preventScroll: true });
    items[next].scrollIntoView({ block: "nearest" });
  }

  return (
    <div className="permission-picker-anchor">
      <button
        ref={trigger}
        type="button"
        className="permission-selector"
        data-tone={current.tone}
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={identity}
        onClick={() => onOpenChange(!open)}
        onKeyDown={(event) => {
          if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
            if (open && presence.mounted) keyDown(event);
            else {
              event.preventDefault();
              initialFocus.current = ["ArrowUp", "End"].includes(event.key) ? "last" : "first";
              onOpenChange(true);
            }
          } else if (open && ["Escape", "Tab"].includes(event.key)) keyDown(event);
        }}
      >
        <HarnessIcon name={icon || current.icon} size={16} aria-hidden="true" />
        <span>{current.label}</span>
        <HarnessIcon name="chevron-down" size={12} aria-hidden="true" />
      </button>
      {presence.mounted ? createPortal(
        <div
          ref={panel}
          id={identity}
          className="permission-menu"
          role="menu"
          aria-label={label}
          aria-hidden={!open || undefined}
          inert={!open}
          data-phase={presence.phase}
          data-placement={position.placement}
          style={{ top: position.top, left: position.left, width: position.width, maxHeight: position.maxHeight }}
          onKeyDown={keyDown}
        >
          {options.map((mode) => (
            <button
              type="button"
              key={mode.id}
              className="permission-menu-option"
              data-tone={mode.tone}
              role="menuitemradio"
              aria-label={mode.label}
              aria-describedby={`${identity}-${mode.id}-detail`}
              aria-checked={mode.id === current.id}
              tabIndex={mode.id === current.id ? 0 : -1}
              onClick={() => choose(mode.id)}
            >
              <HarnessIcon name={mode.icon} size={16} aria-hidden="true" />
              <span className="permission-menu-copy">
                <strong>{mode.label}</strong>
                <small id={`${identity}-${mode.id}-detail`}>{mode.detail}</small>
              </span>
              <span className="permission-menu-check" aria-hidden="true">
                {mode.id === current.id ? <HarnessIcon name="check" size={14} interactive={false} aria-hidden="true" /> : null}
              </span>
            </button>
          ))}
        </div>, document.body,
      ) : null}
    </div>
  );
}
