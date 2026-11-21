import React, { useLayoutEffect, useRef, useState } from "react";

const pageSize = 40;

export default function MessageHistory({ messages, renderMessage, scroll, follow, loading, children }) {
  const [limit, setLimit] = useState(pageSize);
  const [readingFrom, setReadingFrom] = useState(null);
  const anchor = useRef(null);
  const tailStart = Math.max(0, messages.length - limit);
  const first = Math.min(readingFrom ?? tailStart, tailStart);

  useLayoutEffect(() => {
    follow.current = true;
  }, [follow]);

  function showEarlier() {
    const row = scroll.current.querySelector("[data-message-index]");
    if (row) anchor.current = { row, top: row.getBoundingClientRect().top };
    const start = Math.max(0, first - pageSize);
    setReadingFrom(start);
    setLimit(messages.length - start);
  }

  useLayoutEffect(() => {
    const saved = anchor.current;
    if (saved?.row.isConnected) {
      scroll.current.scrollTop += saved.row.getBoundingClientRect().top - saved.top;
    }
    anchor.current = null;
  }, [first, scroll]);

  function onScroll(event) {
    const el = event.currentTarget;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 110;
    follow.current = atBottom;
    // Keep the passage on screen if another message arrives while reading.
    setReadingFrom(atBottom ? null : first);
  }

  return (
    <div className="message-scroll" ref={scroll} onScroll={onScroll}>
      <div className="message-column">
        {loading ? <div className="loading-session">正在恢复对话…</div> : null}
        {first > 0 ? (
          <button type="button" className="message-history-more" onClick={showEarlier}>
            显示更早的消息
          </button>
        ) : null}
        {messages.slice(first).map((message, offset) => renderMessage(message, first + offset))}
        {children}
      </div>
    </div>
  );
}
