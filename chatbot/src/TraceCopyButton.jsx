import React from "react";
import { CopyButton } from "./RichContent";
import { loadPlatformCommit, traceDiagnostics } from "./trace-diagnostics.js";

/** Copies the message's trace ID with the environment around it (#221). */
export default function TraceCopyButton(props) {
  const prefetch = () => void loadPlatformCommit();
  return (
    <span
      className="trace-copy"
      onPointerEnter={prefetch}
      onFocus={prefetch}
    >
      <CopyButton
        value={() => traceDiagnostics(props)}
        label="复制 Trace ID 与排障信息"
        icon="tag"
      />
    </span>
  );
}
