// Harness UI Selector (MIT, see harness-ui-LICENSE.txt)
// for React: a controlled replacement for <select>. onChange receives
// {target: {value}} so handlers written for the native select keep working.
import React, { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import HarnessIcon from "./HarnessIcon";
import { usePanelPresence } from "./usePanelPresence";
import {
  filterOptions,
  nextOptionIndex,
  normalizeOptions,
  placeSelectPanel,
  selectChangeEvent,
  selectedOption,
  showsSearch,
} from "./harness-select-model.js";
import "./harness-controls.css";

export default function HarnessSelect({
  value,
  onChange,
  options,
  disabled = false,
  ariaLabel,
  className = "",
  searchable,
  placeholder = "请选择",
}) {
  const trigger = useRef(null);
  const panel = useRef(null);
  const search = useRef(null);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const identity = `harness-select-${useId().replaceAll(":", "")}`;
  const [open, setOpen] = useState(false);
  const presence = usePanelPresence(open, 180);
  const [query, setQuery] = useState("");
  const [position, setPosition] = useState({ top: 0, left: 0, width: 240, maxHeight: 360, placement: "bottom" });
  const items = useMemo(() => normalizeOptions(options), [options]);
  const current = selectedOption(items, value);
  const withSearch = showsSearch(items, searchable);
  const visible = useMemo(() => filterOptions(items, query), [items, query]);

  const place = useCallback(() => {
    if (!trigger.current || !panel.current) return;
    const vv = window.visualViewport;
    const next = placeSelectPanel(
      trigger.current.getBoundingClientRect(),
      { width: vv?.width || innerWidth, height: vv?.height || innerHeight },
      panel.current.scrollHeight,
    );
    setPosition((old) => (Object.keys(next).every((key) => old[key] === next[key]) ? old : next));
  }, []);

  useLayoutEffect(() => {
    if (presence.mounted) place();
  }, [presence.mounted, visible.length, place]);

  useEffect(() => {
    if (!open) return;
    setQuery("");
    const frame = requestAnimationFrame(() => {
      if (withSearch) search.current?.focus({ preventScroll: true });
      else (panel.current?.querySelector('[aria-selected="true"]:not(:disabled)') ||
        panel.current?.querySelector('[role="option"]:not(:disabled)'))?.focus({ preventScroll: true });
    });
    const inside = (target) => panel.current?.contains(target) || trigger.current?.contains(target);
    const outside = (event) => { if (!inside(event.target)) setOpen(false); };
    const scroll = (event) => { if (!panel.current?.contains(event.target)) setOpen(false); };
    const resize = () => setOpen(false);
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    window.addEventListener("scroll", scroll, true);
    window.addEventListener("resize", resize);
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("focusin", outside);
      window.removeEventListener("scroll", scroll, true);
      window.removeEventListener("resize", resize);
    };
  }, [open, withSearch]);

  useEffect(() => {
    if (disabled) setOpen(false);
  }, [disabled]);

  function close(focusTrigger) {
    setOpen(false);
    if (focusTrigger) trigger.current?.focus({ preventScroll: true });
  }
  function choose(option) {
    if (!option || option.disabled) return;
    if (option.value !== current?.value) onChangeRef.current?.(selectChangeEvent(option.value));
    close(true);
  }
  function keyDown(event) {
    if (event.nativeEvent?.isComposing) return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      close(true);
      return;
    }
    if (event.key === "Tab") {
      close(true);
      return;
    }
    const inSearch = event.target === search.current;
    if (inSearch && event.key === "Enter") {
      event.preventDefault();
      choose(visible.find((option) => !option.disabled));
      return;
    }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key) || (inSearch && ["Home", "End"].includes(event.key))) return;
    event.preventDefault();
    const buttons = [...(panel.current?.querySelectorAll('[role="option"]') || [])];
    const index = nextOptionIndex(visible, buttons.indexOf(document.activeElement), event.key);
    buttons[index]?.focus({ preventScroll: true });
    buttons[index]?.scrollIntoView({ block: "nearest" });
  }

  return (
    <>
      <button
        ref={trigger}
        type="button"
        className={`harness-select${className ? ` ${className}` : ""}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={presence.mounted ? identity : undefined}
        aria-label={ariaLabel ? `${ariaLabel}：${current?.label || placeholder}` : undefined}
        disabled={disabled}
        onClick={() => setOpen(!open)}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault();
            setOpen(true);
          }
        }}
      >
        <span className={`harness-select-value${current ? "" : " is-placeholder"}`}>{current?.label || placeholder}</span>
        <HarnessIcon name="chevron-down" size={14} className="harness-select-chevron" interactive={false} />
      </button>
      {presence.mounted
        ? createPortal(
            <div
              ref={panel}
              id={identity}
              className="harness-select-panel"
              role="listbox"
              aria-label={ariaLabel}
              aria-hidden={!open || undefined}
              inert={!open}
              data-phase={presence.phase}
              data-placement={position.placement}
              style={{ top: position.top, left: position.left, width: position.width, maxHeight: position.maxHeight }}
              onKeyDown={keyDown}
            >
              {withSearch ? (
                <label className="harness-select-search">
                  <HarnessIcon name="search" size={14} interactive={false} />
                  <input
                    ref={search}
                    type="search"
                    aria-label="搜索选项"
                    placeholder="搜索…"
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                  />
                </label>
              ) : null}
              <div className="harness-select-options">
                {visible.map((option) => {
                  const selected = option.value === current?.value;
                  return (
                    <button
                      type="button"
                      key={option.value}
                      role="option"
                      tabIndex={-1}
                      aria-selected={selected}
                      disabled={option.disabled}
                      className={`harness-select-option${selected ? " is-selected" : ""}`}
                      onClick={() => choose(option)}
                    >
                      <span className="harness-select-option-label">{option.label}</span>
                      <span className="harness-select-option-meta">
                        {option.meta}
                        {selected ? <HarnessIcon name="check" size={14} className="harness-select-check" interactive={false} /> : null}
                      </span>
                    </button>
                  );
                })}
                {!visible.length ? <div className="harness-select-empty">没有匹配的选项</div> : null}
              </div>
            </div>,
            document.body,
          )
        : null}
    </>
  );
}
