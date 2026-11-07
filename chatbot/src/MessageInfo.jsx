import React, {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import HarnessIcon from "./HarnessIcon";
import { messageInfo } from "./message-info.js";
import "./message-info.css";

const CLOSE_DELAY = 120;
const GAP = 8;
const EDGE = 12;
// Only one card is open at a time across all replies.
let closeOpenCard = null;

function placeCard(trigger, card) {
  const anchor = trigger.getBoundingClientRect();
  card.style.maxHeight = "none";
  const width = card.offsetWidth;
  const height = card.scrollHeight + 2;
  const above = Math.max(0, anchor.top - GAP - EDGE);
  const below = Math.max(0, innerHeight - anchor.bottom - GAP - EDGE);
  const up = height > below && above > below;
  const room = Math.max(40, up ? above : below);
  const shown = Math.min(height, room);
  card.style.left = `${Math.max(EDGE, Math.min(innerWidth - width - EDGE, anchor.left))}px`;
  card.style.top = `${Math.max(EDGE, up ? anchor.top - GAP - shown : anchor.bottom + GAP)}px`;
  card.style.maxHeight = `${room}px`;
  card.dataset.placement = up ? "top" : "bottom";
}

function InfoCard({ id, message, cardRef, onPointerEnter, onPointerLeave }) {
  const info = messageInfo(message);
  return (
    <section
      ref={cardRef}
      id={id}
      className="message-info-card"
      role="tooltip"
      onPointerEnter={onPointerEnter}
      onPointerLeave={onPointerLeave}
    >
      <strong className="message-info-title">统计信息</strong>
      <dl className="message-info-rows">
        {info.rows.map((row) => (
          <div key={row.key} data-key={row.key}>
            <dt>{row.label}</dt>
            <dd className={row.missing ? "is-missing" : ""}>{row.value}</dd>
          </div>
        ))}
      </dl>
      <div className="message-info-tokens">
        <div className="message-info-total">
          <strong>Tokens</strong>
          <span className={info.total === null ? "is-missing" : ""}>
            {info.totalText}
          </span>
        </div>
        {info.tokens.length ? (
          <dl className="message-info-parts">
            {info.tokens.map((part) => (
              <div
                key={part.key}
                data-key={part.key}
                className={part.zero ? "is-zero" : ""}
              >
                <span className="message-info-dot" aria-hidden="true" />
                <dt>{part.label}</dt>
                <dd>{part.text}</dd>
              </div>
            ))}
          </dl>
        ) : null}
      </div>
    </section>
  );
}

export default function MessageInfo({ message }) {
  const [open, setOpen] = useState(false);
  const [pinned, setPinned] = useState(false);
  const trigger = useRef(null);
  const card = useRef(null);
  const timer = useRef(0);
  const id = useRef(`message-info-${Math.random().toString(36).slice(2)}`);

  const cancelClose = () => clearTimeout(timer.current);
  const close = useCallback(() => {
    clearTimeout(timer.current);
    setOpen(false);
    setPinned(false);
  }, []);
  const show = (pin) => {
    cancelClose();
    if (closeOpenCard && closeOpenCard !== close) closeOpenCard();
    closeOpenCard = close;
    setOpen(true);
    if (pin) setPinned(true);
  };
  const leave = () => {
    if (pinned) return;
    cancelClose();
    timer.current = setTimeout(close, CLOSE_DELAY);
  };

  useEffect(() => {
    if (!open) {
      if (closeOpenCard === close) closeOpenCard = null;
      return;
    }
    const outside = (event) => {
      if (
        !trigger.current?.contains(event.target) &&
        !card.current?.contains(event.target)
      )
        close();
    };
    const key = (event) => {
      if (event.key === "Escape") close();
    };
    const reposition = () => {
      if (trigger.current && card.current)
        placeCard(trigger.current, card.current);
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("keydown", key);
    window.addEventListener("resize", close);
    window.addEventListener("scroll", reposition, true);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("keydown", key);
      window.removeEventListener("resize", close);
      window.removeEventListener("scroll", reposition, true);
    };
  }, [open, close]);
  useLayoutEffect(() => {
    if (open && trigger.current && card.current)
      placeCard(trigger.current, card.current);
  }, [open]);
  useEffect(
    () => () => {
      clearTimeout(timer.current);
      if (closeOpenCard === close) closeOpenCard = null;
    },
    [close],
  );

  return (
    <>
      <button
        ref={trigger}
        type="button"
        className="icon-button message-info-trigger"
        aria-label="统计信息"
        aria-expanded={open}
        aria-describedby={open ? id.current : undefined}
        onPointerEnter={(e) => {
          if (e.pointerType !== "touch") show(false);
        }}
        onPointerLeave={leave}
        onFocus={() => show(false)}
        onBlur={leave}
        onClick={() => (open && pinned ? close() : show(true))}
      >
        <HarnessIcon
          name="info"
          size={14}
          strokeWidth={1.7}
          aria-hidden="true"
        />
      </button>
      {open
        ? createPortal(
            <InfoCard
              id={id.current}
              message={message}
              cardRef={card}
              onPointerEnter={cancelClose}
              onPointerLeave={leave}
            />,
            document.body,
          )
        : null}
    </>
  );
}
