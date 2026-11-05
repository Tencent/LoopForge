import HarnessIcon from "./HarnessIcon";
import { IconButton } from "./ui";

// Messages typed while a reply runs, shown as a tray tucked under the top of
// the composer like Codex. They go out in order after the reply ends;
// stopping the reply holds them until the user continues. "引导" on one of
// them interrupts the running reply with it.
export default function QueuedMessages({
  items = [],
  steering = null,
  held = false,
  busy = false,
  readOnly = false,
  onEdit,
  onRemove,
  onResume,
  onSendNow,
}) {
  if (!items.length && !steering) return null;
  // While a steer is in flight the reply is briefly not running; the other
  // rows still belong to the interrupted turn.
  const interrupting = busy || !!steering;
  const steerLabel = interrupting ? "引导" : "发送";
  const steerTitle = interrupting ? "打断当前回复，立即发送这条" : "立即发送这条";
  return (
    <section
      className={`queued-messages${held ? " is-held" : ""}`}
      aria-label="排队中的消息"
    >
      {held && items.length > 0 && !readOnly ? (
        <div className="queued-messages-note" role="status">
          <span>已暂停自动发送</span>
          <button
            type="button"
            className="queued-messages-resume"
            onClick={onResume}
          >
            {busy ? "结束后继续发送" : "继续发送"}
          </button>
        </div>
      ) : null}
      <ol className="queued-messages-list">
        {steering ? (
          <li className="queued-message is-steering" aria-busy="true">
            <HarnessIcon
              name="spinner"
              motion="busy"
              size={14}
              className="queued-message-icon"
              aria-hidden="true"
            />
            <span className="queued-message-text" title={steering.text}>
              {steering.text || "图片与文件"}
            </span>
            <span className="queued-message-state">正在引导</span>
          </li>
        ) : null}
        {items.map((item) => {
          const extra =
            (item.attachments?.length || 0) + (item.references?.length || 0);
          return (
            <li key={item.id} className="queued-message">
              <HarnessIcon
                name="queue"
                size={14}
                interactive={false}
                className="queued-message-icon"
                aria-hidden="true"
              />
              <span className="queued-message-text" title={item.text}>
                {item.text || "图片与文件"}
              </span>
              {extra ? (
                <span className="queued-message-extra">
                  <HarnessIcon name="paperclip" size={11} aria-hidden="true" />
                  {extra}
                </span>
              ) : null}
              {!readOnly ? (
                <span className="queued-message-actions">
                  <button
                    type="button"
                    className="queued-message-steer"
                    title={steerTitle}
                    disabled={!!steering}
                    onClick={() => onSendNow?.(item.id)}
                  >
                    <HarnessIcon name="redo" size={13} />
                    <span>{steerLabel}</span>
                  </button>
                  <IconButton
                    label="编辑消息"
                    className="queued-message-action"
                    onClick={() => onEdit?.(item)}
                  >
                    <HarnessIcon name="edit" size={14} />
                  </IconButton>
                  <IconButton
                    label="删除"
                    className="queued-message-action"
                    onClick={() => onRemove?.(item.id)}
                  >
                    <HarnessIcon name="trash" size={14} />
                  </IconButton>
                </span>
              ) : null}
            </li>
          );
        })}
      </ol>
    </section>
  );
}
