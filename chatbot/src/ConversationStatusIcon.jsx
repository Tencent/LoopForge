import React from "react";

export default function ConversationStatusIcon({ status }) {
  return (
    <span
      className="conversation-status"
      data-conversation-status={status.state}
      role="img"
      aria-label={status.label}
      title={status.label}
    >
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <path d="M21 12a8 8 0 0 1-11.3 7.3L4 20l.7-5.7A8 8 0 1 1 21 12z" />
        <circle className="conversation-status-dot" cx="8.6" cy="12" r="1.25" stroke="none" />
        <circle className="conversation-status-dot" cx="12.6" cy="12" r="1.25" stroke="none" />
        <circle className="conversation-status-dot" cx="16.6" cy="12" r="1.25" stroke="none" />
      </svg>
      <span className="conversation-status-mark" aria-hidden="true" />
    </span>
  );
}
