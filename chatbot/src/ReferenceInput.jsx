import React, {
  forwardRef,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
} from "react";
import MentionPicker from "./MentionPicker";
import HarnessIcon from "./HarnessIcon";
import { insertMention, mentionAt } from "./mentions.js";
import { elementTitle } from "./element-context.js";

// Shared by ordinary conversations and scheduled prompts. The parent owns the
// draft; picking an asset always changes its text and structured references together.
export default forwardRef(function ReferenceInput(
  {
    projectId,
    text,
    references,
    mention,
    onMentionChange,
    onChange,
    onError,
    editorRef,
    issueId,
    canManageIssue,
    onManageIssue,
    textareaProps = {},
    referencesReadOnly = false,
    showReferenceChips = true,
  },
  ref,
) {
  const picker = useRef(null);
  const returnFocus = useRef(null),
    selection = useRef(null),
    dismissedSelection = useRef(null);
  const range = (event) =>
    mentionAt(event.target.value, event.target.selectionStart, references);
  const open = (trigger) => {
    if (referencesReadOnly) return;
    dismissedSelection.current = null;
    returnFocus.current = trigger || editorRef.current;
    const caret = editorRef.current?.selectionStart ?? text.length;
    onMentionChange(
      mentionAt(text, caret, references) || {
        start: caret,
        end: caret,
        query: "",
      },
    );
    editorRef.current?.focus();
  };
  const close = () => {
    const editor = editorRef.current;
    dismissedSelection.current = editor
      ? {
          value: editor.value,
          start: editor.selectionStart,
          end: editor.selectionEnd,
        }
      : null;
    onMentionChange(null);
  };
  useImperativeHandle(ref, () => ({
    openReferences: open,
    keyDown: (event) => picker.current?.keyDown(event),
  }));
  useLayoutEffect(() => {
    if (selection.current == null) return;
    editorRef.current?.focus({ preventScroll: true });
    editorRef.current?.setSelectionRange(selection.current, selection.current);
    selection.current = null;
  });
  function choose(asset) {
    if (referencesReadOnly) return;
    const exists = references.some(
      (item) => item.type === asset.type && item.id === asset.id,
    );
    if (!exists && references.length >= 12) {
      onError?.("每次最多引用 12 个对象。");
      return;
    }
    const next = textareaProps.readOnly
      ? { text, caret: 0 }
      : insertMention(text, mention, asset);
    selection.current = textareaProps.readOnly ? null : next.caret;
    onChange({
      text: next.text,
      references: exists ? references : [...references, asset],
    });
    onMentionChange(null);
    if (textareaProps.readOnly)
      editorRef.current?.focus({ preventScroll: true });
  }
  return (
    <>
      <MentionPicker
        ref={picker}
        key={projectId}
        projectId={projectId}
        trigger={mention}
        editor={editorRef}
        onSelect={choose}
        onClose={close}
        onRestoreFocus={() =>
          (returnFocus.current?.isConnected
            ? returnFocus.current
            : editorRef.current
          )?.focus({ preventScroll: true })
        }
        issueId={issueId}
        canManageIssue={canManageIssue}
        onManageIssue={onManageIssue}
      />
      {showReferenceChips && references.length ? (
        <div className="composer-attachments">
          {references.map((asset) => (
            <span
              key={asset.type + asset.id}
              className={`composer-attachment-file${asset.type === "element" ? " is-element" : ""}`}
              title={asset.type === "element" ? elementTitle(asset) : undefined}
            >
              {asset.type === "element" ? (
                <HarnessIcon name="crosshair" size={13} />
              ) : (
                "@"
              )}
              {asset.name || asset.id}
              {!referencesReadOnly ? (
                <button
                  type="button"
                  aria-label={`移除引用 ${asset.name || asset.id}`}
                  onClick={() => {
                    onChange({
                      text: textareaProps.readOnly
                        ? text
                        : text.replace(`@${asset.name || asset.id} `, ""),
                      references: references.filter(
                        (item) =>
                          item.id !== asset.id || item.type !== asset.type,
                      ),
                    });
                    onMentionChange(null);
                  }}
                >
                  <HarnessIcon name="close" size={13} />
                </button>
              ) : null}
            </span>
          ))}
        </div>
      ) : null}
      <textarea
        {...textareaProps}
        ref={editorRef}
        value={text}
        aria-expanded={!!mention}
        onChange={(event) => {
          dismissedSelection.current = null;
          returnFocus.current = editorRef.current;
          const value = event.target.value;
          onChange({
            text: value,
            references: references.filter(
              (asset) =>
                !text.includes(`@${asset.name || asset.id}`) ||
                value.includes(`@${asset.name || asset.id}`),
            ),
          });
          onMentionChange(event.nativeEvent.isComposing ? null : range(event));
          textareaProps.onChange?.(event);
        }}
        onCompositionEnd={(event) => {
          if (!referencesReadOnly) onMentionChange(range(event));
        }}
        onClick={(event) => {
          if (!referencesReadOnly) {
            dismissedSelection.current = null;
            onMentionChange(range(event));
          }
        }}
        onSelect={(event) => {
          if (referencesReadOnly) return;
          // React also emits select events on Escape and focus restoration.
          // Keep a dismissed mention closed until its text or selection changes.
          const dismissed = dismissedSelection.current,
            editor = event.target;
          if (
            dismissed &&
            dismissed.value === editor.value &&
            dismissed.start === editor.selectionStart &&
            dismissed.end === editor.selectionEnd
          )
            return;
          dismissedSelection.current = null;
          onMentionChange(range(event));
        }}
        onKeyDown={(event) => {
          if (mention && picker.current?.keyDown(event)) return;
          textareaProps.onKeyDown?.(event);
        }}
      />
    </>
  );
});
