import React, { useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { credentialHint, openCredentialSettings } from "./credentials";
import HarnessIcon from "./HarnessIcon";
import {
  MENU_WIDTH,
  anchorVisible,
  availableBounds,
  placeMenu,
} from "./menu-placement.js";
import "./controls-motion.css";
// CredentialAction adds a one-click link to personal settings when an error
// was caused by a missing or rejected personal credential.
export function CredentialAction({ text }) {
  const hint = credentialHint(text);
  if (!hint) return null;
  return (
    <span className="credential-action">
      <button type="button" onClick={() => openCredentialSettings(hint.field)}>
        <HarnessIcon name="key" size={13} />
        去配置 {hint.label}
      </button>
      {hint.owner ? <small>需由责任人本人在个人配置中配置</small> : null}
    </span>
  );
}
export function IconButton({
  label,
  children,
  onClick,
  className = "",
  ...props
}) {
  return (
    <button
      type="button"
      className={`icon-button ${className}`.trim()}
      title={label}
      aria-label={label}
      onClick={onClick}
      {...props}
    >
      {children}
    </button>
  );
}
export function Menu({ label, children, className = "", onClose }) {
  const ref = useRef();
  useEffect(() => {
    function click(e) {
      if (ref.current && !ref.current.contains(e.target)) onClose();
    }
    function key(e) {
      if (e.key === "Escape") {
        e.stopPropagation();
        ref.current?.parentElement?.querySelector("button")?.focus();
        onClose();
      }
      if (
        ["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key) &&
        ref.current?.contains(document.activeElement)
      ) {
        const items = [
          ...ref.current.querySelectorAll(
            "button[role^=menuitem]:not(:disabled)",
          ),
        ];
        if (!items.length) return;
        const index = items.indexOf(document.activeElement);
        const next =
          e.key === "Home"
            ? 0
            : e.key === "End"
              ? items.length - 1
              : (index + (e.key === "ArrowDown" ? 1 : -1) + items.length) %
                items.length;
        e.preventDefault();
        items[next].focus();
      }
    }
    document.addEventListener("pointerdown", click);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("pointerdown", click);
      document.removeEventListener("keydown", key);
    };
  }, [onClose]);
  return (
    <div
      ref={ref}
      className={`menu ${className}`}
      role="menu"
      aria-label={label}
    >
      {children}
    </div>
  );
}
export function Dialog({ title, children, onClose, footer }) {
  const ref = useRef();
  useEffect(() => {
    const previous = document.activeElement;
    (
      ref.current?.querySelector("input,textarea,select") || ref.current
    )?.focus();
    const key = (e) => {
      if (e.key === "Escape") onClose();
      if (e.key === "Tab") {
        const focusable = [
          ...ref.current.querySelectorAll(
            "button,input,select,textarea,a[href]",
          ),
        ].filter((x) => !x.disabled);
        if (!focusable.length) {
          e.preventDefault();
          return;
        }
        const first = focusable[0],
          last = focusable.at(-1);
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    };
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("keydown", key);
      previous?.focus?.();
    };
  }, []);
  return (
    <div
      className="dialog-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) {
          e.preventDefault();
          onClose();
        }
      }}
    >
      <section
        className="dialog"
        ref={ref}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        tabIndex={-1}
      >
        <header>
          <h2>{title}</h2>
          <IconButton label="关闭" onClick={onClose}>
            <HarnessIcon name="close" size={18} />
          </IconButton>
        </header>
        {children}
        {footer ? <footer>{footer}</footer> : null}
      </section>
    </div>
  );
}
const prefersReducedMotion = () =>
  !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

function Highlighted({ text, query }) {
  const index = query ? text.toLowerCase().indexOf(query) : -1;
  if (index < 0) return text;
  return (
    <>
      {text.slice(0, index)}
      <mark>{text.slice(index, index + query.length)}</mark>
      {text.slice(index + query.length)}
    </>
  );
}

function clippingRects(node) {
  const rects = [];
  for (
    let el = node.parentElement;
    el && el !== document.body && el !== document.documentElement;
    el = el.parentElement
  ) {
    if (/auto|scroll|hidden|clip/.test(getComputedStyle(el).overflowY))
      rects.push(el.getBoundingClientRect());
  }
  return rects;
}

function visibleViewport() {
  const v = window.visualViewport;
  const top = v?.offsetTop ?? 0;
  const left = v?.offsetLeft ?? 0;
  return {
    top,
    left,
    bottom: top + (v?.height ?? window.innerHeight),
    right: left + (v?.width ?? window.innerWidth),
  };
}

/**
 * Searchable single-select popover. It is portaled and fixed-positioned so
 * containers with `container-type` or overflow clipping cannot trap it, and it
 * stays mounted while `open` flips to false to play the closing transition.
 */
export function SelectMenu(props) {
  const { open = true } = props;
  const [present, setPresent] = React.useState(open);
  if (open && !present) setPresent(true);
  if (!present) return null;
  return (
    <SelectPopover {...props} open={open} onExited={() => setPresent(false)} />
  );
}

function SelectPopover({
  label,
  value,
  options,
  onSelect,
  onClose,
  open,
  onExited,
  anchorRef,
  searchable = false,
  actions = [],
  emptyText = "没有匹配的选项",
}) {
  const id = React.useId();
  const root = useRef(null);
  const search = useRef(null);
  const body = useRef(null);
  const list = useRef(null);
  const highlight = useRef(null);
  const rows = useRef([]);
  const side = useRef(null);
  const slide = useRef(false);
  const reveal = useRef(true);
  const applied = useRef("");
  const frame = useRef(0);
  const closing = useRef(null);
  const [query, setQuery] = React.useState("");
  const needle = query.trim().toLowerCase();
  const visible = options.filter(
    (o) => !needle || `${o.label} ${o.id}`.toLowerCase().includes(needle),
  );
  const items = [
    ...visible.map((option) => ({ key: `o:${option.id}`, option })),
    ...actions.map((action) => ({ key: `a:${action.id}`, action })),
  ];
  const [active, setActive] = React.useState(() =>
    Math.max(
      0,
      visible.findIndex((o) => o.id === value),
    ),
  );
  const current = Math.min(active, items.length - 1);
  rows.current.length = items.length;
  const latest = useRef();
  latest.current = { onClose, onSelect, anchorRef, open, query: needle };

  const dismiss = (restoreFocus) => {
    latest.current.onClose();
    if (restoreFocus) anchorRef?.current?.focus({ preventScroll: true });
  };
  const choose = (item) => {
    if (!item || !open) return;
    if (item.action) {
      onClose();
      item.action.onSelect?.();
      return;
    }
    onSelect(item.option.id);
    onClose();
  };
  const moveTo = (index) => {
    if (!items.length) return;
    slide.current = true;
    reveal.current = true;
    setActive((index + items.length) % items.length);
  };

  const positionHighlight = React.useCallback(
    (animate) => {
      const hl = highlight.current;
      const row = rows.current[current];
      if (!hl || !body.current) return;
      let next = "hidden";
      let top = 0;
      let bottom = 0;
      let box;
      if (row) {
        box = body.current.getBoundingClientRect();
        const rect = row.getBoundingClientRect();
        top = rect.top;
        bottom = rect.bottom;
        if (list.current?.contains(row)) {
          const clip = list.current.getBoundingClientRect();
          top = Math.max(top, clip.top);
          bottom = Math.min(bottom, clip.bottom);
        }
        if (bottom > top)
          next = `${top - box.top}|${bottom - top}|${rect.left - box.left}|${rect.width}`;
      }
      if (next === applied.current) return;
      applied.current = next;
      hl.style.setProperty(
        "--highlight-duration",
        animate && !prefersReducedMotion() ? "190ms" : "0ms",
      );
      if (next === "hidden") {
        hl.style.opacity = "0";
        return;
      }
      const [y, height, x, width] = next.split("|");
      hl.style.transform = `translateY(${y}px)`;
      hl.style.height = `${height}px`;
      hl.style.left = `${x}px`;
      hl.style.width = `${width}px`;
      hl.style.opacity = "1";
    },
    [current],
  );
  latest.current.positionHighlight = positionHighlight;

  const place = React.useCallback((keepSide, instant) => {
    const el = root.current;
    const anchor = latest.current.anchorRef?.current;
    if (!el || !anchor || !latest.current.open) return;
    const bounds = availableBounds(visibleViewport(), clippingRects(anchor));
    const rect = anchor.getBoundingClientRect();
    if (!anchorVisible(rect, bounds)) {
      latest.current.onClose();
      return;
    }
    const pad = getComputedStyle(body.current);
    const natural =
      el.offsetHeight -
      el.clientHeight +
      (search.current?.offsetHeight ?? 0) +
      list.current.scrollHeight +
      (body.current.lastElementChild === list.current
        ? 0
        : body.current.lastElementChild.offsetHeight) +
      parseFloat(pad.paddingTop) +
      parseFloat(pad.paddingBottom);
    const p = placeMenu(
      rect,
      bounds,
      natural,
      MENU_WIDTH,
      keepSide ? side.current : null,
    );
    side.current = p.side;
    if (instant) el.classList.add("is-instant");
    el.dataset.side = p.side;
    Object.assign(el.style, {
      top: `${p.top}px`,
      left: `${p.left}px`,
      width: `${p.width}px`,
      height: `${p.height}px`,
    });
    if (instant) {
      el.getBoundingClientRect();
      el.classList.remove("is-instant");
    }
  }, []);

  React.useLayoutEffect(() => {
    place(false, true);
    const el = root.current;
    if (searchable)
      search.current?.querySelector("input")?.focus({ preventScroll: true });
    else el?.focus({ preventScroll: true });
    if (el && !prefersReducedMotion())
      el.animate(
        [
          {
            opacity: 0,
            transform: `translateY(${side.current === "below" ? -5 : 5}px)`,
          },
          { opacity: 1, transform: "translateY(0)" },
        ],
        { duration: 220, easing: "cubic-bezier(0.16, 1, 0.3, 1)" },
      );
  }, [place, searchable]);

  const firstQuery = useRef(true);
  React.useLayoutEffect(() => {
    if (firstQuery.current) {
      firstQuery.current = false;
      return;
    }
    place(true, false);
  }, [needle, items.length, place]);

  React.useLayoutEffect(() => {
    const row = rows.current[current];
    const scroller = list.current;
    if (reveal.current && row && scroller?.contains(row)) {
      const a = row.getBoundingClientRect();
      const l = scroller.getBoundingClientRect();
      if (a.top < l.top) scroller.scrollTop += a.top - l.top - 4;
      else if (a.bottom > l.bottom)
        scroller.scrollTop += a.bottom - l.bottom + 4;
    }
    reveal.current = false;
    positionHighlight(slide.current);
    slide.current = false;
  });

  useEffect(() => {
    const el = root.current;
    if (!el) return;
    if (open) {
      closing.current?.cancel();
      closing.current = null;
      el.style.pointerEvents = "";
      return;
    }
    el.style.pointerEvents = "none";
    if (prefersReducedMotion()) {
      onExited();
      return;
    }
    const animation = el.animate(
      [
        { opacity: 1, transform: "translateY(0)" },
        {
          opacity: 0,
          transform: `translateY(${side.current === "below" ? -5 : 5}px)`,
        },
      ],
      { duration: 130, easing: "cubic-bezier(0.4, 0, 1, 1)", fill: "forwards" },
    );
    animation.onfinish = onExited;
    closing.current = animation;
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const schedule = (event) => {
      if (event?.type === "scroll" && root.current?.contains(event.target))
        return;
      cancelAnimationFrame(frame.current);
      frame.current = requestAnimationFrame(() => {
        place(!!latest.current.query, true);
        latest.current.positionHighlight(false);
      });
    };
    const pointer = (event) => {
      const anchor = latest.current.anchorRef?.current;
      if (
        root.current?.contains(event.target) ||
        anchor?.contains(event.target)
      )
        return;
      latest.current.onClose();
    };
    const key = (event) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      dismiss(true);
    };
    const viewport = window.visualViewport;
    const observer = new ResizeObserver(() => schedule());
    const anchor = latest.current.anchorRef?.current;
    if (anchor) {
      observer.observe(anchor);
      if (anchor.parentElement) observer.observe(anchor.parentElement);
    }
    document.addEventListener("pointerdown", pointer, true);
    document.addEventListener("keydown", key);
    window.addEventListener("scroll", schedule, true);
    window.addEventListener("resize", schedule);
    viewport?.addEventListener("resize", schedule);
    viewport?.addEventListener("scroll", schedule);
    return () => {
      cancelAnimationFrame(frame.current);
      observer.disconnect();
      document.removeEventListener("pointerdown", pointer, true);
      document.removeEventListener("keydown", key);
      window.removeEventListener("scroll", schedule, true);
      window.removeEventListener("resize", schedule);
      viewport?.removeEventListener("resize", schedule);
      viewport?.removeEventListener("scroll", schedule);
    };
  }, [open, place]);

  const onKeyDown = (event) => {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      moveTo(current + (event.key === "ArrowDown" ? 1 : -1));
    } else if (event.key === "Home" || event.key === "End") {
      event.preventDefault();
      moveTo(event.key === "Home" ? 0 : items.length - 1);
    } else if (event.key === "Enter") {
      event.preventDefault();
      choose(items[current]);
    } else if (event.key === "Tab") {
      onClose();
    }
  };

  const rowId = (index) => `${id}-row-${index}`;
  const renderRow = (item, index) => {
    const { option, action } = item;
    const selected = !!option && option.id === value;
    const icon = option ? option.icon : action.icon;
    return (
      <button
        type="button"
        role="option"
        id={rowId(index)}
        key={item.key}
        ref={(el) => {
          rows.current[index] = el;
        }}
        tabIndex={-1}
        aria-selected={selected}
        className={`menu-option${option?.unavailable ? " is-unavailable" : ""}${index === current ? " is-active" : ""}`}
        onMouseDown={(e) => e.preventDefault()}
        onPointerMove={() => {
          if (index === current) return;
          slide.current = true;
          setActive(index);
        }}
        onClick={() => choose(item)}
      >
        {icon ? <HarnessIcon name={icon} size={15} /> : null}
        <span className="menu-option-label">
          {option ? (
            <Highlighted text={option.label} query={needle} />
          ) : (
            action.label
          )}
        </span>
        {option?.description ? (
          <small title={option.description}>{option.description}</small>
        ) : null}
        {selected ? (
          <HarnessIcon
            name="check"
            size={15}
            className="menu-option-check"
            interactive={false}
          />
        ) : null}
      </button>
    );
  };

  const listId = `${id}-list`;
  const container = anchorRef?.current?.closest(".chat-shell") || document.body;
  return createPortal(
    <div
      ref={root}
      className="select-menu"
      aria-label={label}
      tabIndex={-1}
      onKeyDown={onKeyDown}
    >
      {searchable ? (
        <label className="menu-search" ref={search}>
          <HarnessIcon name="search" size={15} interactive={false} />
          <input
            placeholder={`搜索${label}`}
            value={query}
            role="combobox"
            aria-expanded={open}
            aria-controls={listId}
            aria-autocomplete="list"
            aria-activedescendant={items.length ? rowId(current) : undefined}
            onChange={(e) => {
              setQuery(e.target.value);
              slide.current = true;
              reveal.current = true;
              setActive(0);
              if (list.current) list.current.scrollTop = 0;
            }}
          />
        </label>
      ) : (
        <div className="menu-label" ref={search}>
          {label}
        </div>
      )}
      <div
        className="select-body"
        ref={body}
        role="listbox"
        id={listId}
        aria-label={label}
      >
        <span className="select-highlight" ref={highlight} aria-hidden="true" />
        <div
          className="select-scroll"
          ref={list}
          role="group"
          onScroll={() => positionHighlight(false)}
        >
          {visible.length ? (
            items.slice(0, visible.length).flatMap((item, index) => {
              const section = item.option.section;
              if (!section || section === visible[index - 1]?.section)
                return [renderRow(item, index)];
              return [
                index > 0 ? (
                  <div
                    className="menu-separator"
                    role="separator"
                    key={`sep:${section}`}
                  />
                ) : null,
                <div
                  className="menu-section-label"
                  role="presentation"
                  key={`s:${section}`}
                >
                  {section}
                </div>,
                renderRow(item, index),
              ];
            })
          ) : (
            <div className="menu-empty" role="presentation">
              {emptyText}
            </div>
          )}
        </div>
        {actions.length ? (
          <div className="select-actions" role="group">
            <div className="menu-separator" role="separator" />
            {items
              .slice(visible.length)
              .map((item, i) => renderRow(item, visible.length + i))}
          </div>
        ) : null}
      </div>
    </div>,
    container,
  );
}
