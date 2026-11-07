import React, { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import HarnessIcon from "./HarnessIcon";
import ModelProviderIcon from "./ModelProviderIcon";
import { usePanelPresence } from "./usePanelPresence";
import "./model-picker.css";

const unavailable = (model) => !!(model.disabled || model.unavailable);
const menuWidth = 240;

export default function ModelPicker({ models, value, label, open, onOpenChange, onSelect }) {
  const trigger = useRef(null);
  const panel = useRef(null);
  const search = useRef(null);
  const callbacks = useRef(null);
  callbacks.current = { onOpenChange, onSelect };
  const identity = `model-picker-${useId().replaceAll(":", "")}`;
  const presence = usePanelPresence(open, 180);
  const [query, setQuery] = useState("");
  const [position, setPosition] = useState({ top: 0, left: 0, width: menuWidth, maxHeight: 420, placement: "top" });
  const current = models.find((model) => model.id === value);
  const filtered = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    return models.filter((model) => [model.name, model.id, model.family].filter(Boolean).join(" ").toLocaleLowerCase().includes(needle));
  }, [models, query]);

  const place = useCallback(() => {
    if (!trigger.current || !panel.current) return;
    const viewport = window.visualViewport;
    const minX = (viewport?.offsetLeft || 0) + 8;
    const minY = (viewport?.offsetTop || 0) + 8;
    const maxX = minX + (viewport?.width || innerWidth) - 16;
    const maxY = minY + (viewport?.height || innerHeight) - 16;
    const anchor = trigger.current.getBoundingClientRect();
    const anchorTop = Math.max(minY, Math.min(anchor.top, maxY));
    const anchorBottom = Math.max(minY, Math.min(anchor.bottom, maxY));
    const width = Math.min(menuWidth, maxX - minX);
    const list = panel.current.querySelector(".grasp-model-options");
    const chrome = panel.current.getBoundingClientRect().height - (list?.getBoundingClientRect().height || 0);
    const desired = Math.min(420, chrome + (list?.scrollHeight || 0));
    const above = Math.max(0, anchorTop - minY - 6);
    const below = Math.max(0, maxY - anchorBottom - 6);
    const placement = below < Math.min(280, desired) && above > below ? "top" : "bottom";
    const maxHeight = Math.min(420, placement === "top" ? above : below);
    const height = Math.min(desired, maxHeight);
    const next = {
      width,
      maxHeight,
      placement,
      left: Math.max(minX, Math.min(anchor.left, maxX - width)),
      top: Math.max(minY, Math.min(maxY - height, placement === "top" ? anchorTop - 6 - height : anchorBottom + 6)),
    };
    setPosition((old) => Object.keys(next).every((key) => old[key] === next[key]) ? old : next);
  }, []);

  useLayoutEffect(() => {
    if (presence.mounted) place();
  }, [presence.mounted, filtered.length, place]);

  useEffect(() => {
    if (!open) return;
    setQuery("");
    const frame = requestAnimationFrame(() => search.current?.focus({ preventScroll: true }));
    return () => cancelAnimationFrame(frame);
  }, [open]);

  useEffect(() => {
    if (!presence.mounted) return;
    const observer = new ResizeObserver(place);
    if (panel.current) observer.observe(panel.current);
    if (trigger.current) observer.observe(trigger.current);
    const onScroll = (event) => {
      if (!panel.current?.contains(event.target)) place();
    };
    window.addEventListener("resize", place);
    document.addEventListener("scroll", onScroll, true);
    window.visualViewport?.addEventListener("resize", place);
    window.visualViewport?.addEventListener("scroll", place);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", place);
      document.removeEventListener("scroll", onScroll, true);
      window.visualViewport?.removeEventListener("resize", place);
      window.visualViewport?.removeEventListener("scroll", place);
    };
  }, [presence.mounted, place]);

  useEffect(() => {
    if (!open) return;
    const inside = (target) => panel.current?.contains(target) || trigger.current?.contains(target);
    const outside = (event) => {
      if (!inside(event.target)) callbacks.current.onOpenChange(false);
    };
    const parentOutside = (event) => {
      if (event.target !== window.frameElement) callbacks.current.onOpenChange(false);
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    let parentDocument;
    try {
      if (window.parent !== window) {
        parentDocument = window.parent.document;
        parentDocument.addEventListener("pointerdown", parentOutside);
        parentDocument.addEventListener("focusin", parentOutside);
      }
    } catch {}
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("focusin", outside);
      parentDocument?.removeEventListener("pointerdown", parentOutside);
      parentDocument?.removeEventListener("focusin", parentOutside);
    };
  }, [open]);

  function choose(model) {
    if (!model || unavailable(model)) return;
    callbacks.current.onSelect(model.id);
    callbacks.current.onOpenChange(false);
  }
  function keyDown(event) {
    if (event.isComposing || event.nativeEvent?.isComposing) return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      trigger.current?.focus({ preventScroll: true });
      callbacks.current.onOpenChange(false);
      return;
    }
    if (event.key === "Tab") {
      // Resume the composer's normal tab order, rather than the portal's order.
      trigger.current?.focus({ preventScroll: true });
      callbacks.current.onOpenChange(false);
      return;
    }
    const inSearch = event.target === search.current;
    if (inSearch && event.key === "Enter") {
      event.preventDefault();
      choose(filtered.find((model) => !unavailable(model)));
      return;
    }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key) ||
        (inSearch && ["Home", "End"].includes(event.key))) return;
    const items = [...(panel.current?.querySelectorAll('[role="menuitemradio"]:not(:disabled)') || [])];
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
    <div className="menu-anchor model-picker-anchor">
      <button
        ref={trigger}
        type="button"
        className="model-picker grasp-model-picker"
        aria-label="选择模型"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={presence.mounted ? identity : undefined}
        disabled={!models.length}
        onClick={() => onOpenChange(!open)}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault();
            onOpenChange(true);
          }
        }}
      >
        <ModelProviderIcon model={current || value} size={20} />
        <span className="model-picker-label">{label || current?.name || "模型"}</span>
        <HarnessIcon name="chevron-down" size={12} />
      </button>
      {presence.mounted ? createPortal(
        <div
          ref={panel}
          id={identity}
          className="grasp-model-menu"
          role="menu"
          aria-label="模型"
          aria-hidden={!open || undefined}
          inert={!open}
          data-phase={presence.phase}
          data-placement={position.placement}
          style={{ top: position.top, left: position.left, width: position.width, maxHeight: position.maxHeight }}
          onKeyDown={keyDown}
        >
          <label className="grasp-model-search" data-icon-motion-host="">
            <HarnessIcon name="search" size={16} />
            <input ref={search} type="text" aria-label="搜索模型" placeholder="搜索模型" value={query} onChange={(event) => setQuery(event.target.value)} />
            {query ? <button type="button" className="model-search-clear" aria-label="清空搜索" onClick={() => { setQuery(""); search.current?.focus(); }}><HarnessIcon name="close" size={13} /></button> : null}
          </label>
          <div className="grasp-model-options">
            {filtered.length ? filtered.map((model) => (
              <button
                type="button"
                key={model.id}
                className="grasp-model-option"
                role="menuitemradio"
                aria-checked={model.id === value}
                disabled={unavailable(model)}
                title={model.name || model.id}
                onClick={() => choose(model)}
              >
                <ModelProviderIcon model={model} size={18} />
                <span className="model-option-copy">
                  <strong>{model.name || model.id}</strong>
                  {[model.family, model.credits].some((item) => item !== undefined && item !== null && String(item).trim()) ? <small>{[model.family, model.credits].filter((item) => item !== undefined && item !== null && String(item).trim()).join(" · ")}</small> : null}
                </span>
                <span className="model-option-check">{model.id === value ? <HarnessIcon name="check" size={14} motion="once" /> : null}</span>
              </button>
            )) : <p className="model-picker-empty" role="status">没有匹配的模型</p>}
          </div>
        </div>, document.body,
      ) : null}
    </div>
  );
}
