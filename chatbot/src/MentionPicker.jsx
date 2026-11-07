import React, {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import HarnessIcon from "./HarnessIcon";
import { api } from "./api";
import { referenceTypes } from "./mentions.js";
import { usePanelPresence } from "./usePanelPresence";
import "./mention-context.css";

const icons = {
  agent: "user",
  skill: "skills",
  issue: "doc",
  sop: "branch",
  run: "play",
  repository: "folder",
};
export default forwardRef(function MentionPicker(
  {
    projectId,
    trigger,
    onSelect,
    onClose,
    editor,
    issueId,
    onManageIssue,
    canManageIssue = false,
  },
  ref,
) {
  const open = !!trigger;
  const presence = usePanelPresence(open, 280);
  const [type, setType] = useState(""),
    [query, setQuery] = useState(trigger?.query || ""),
    [rows, setRows] = useState([]),
    [next, setNext] = useState(0),
    [loading, setLoading] = useState(false),
    [error, setError] = useState(""),
    [active, setActive] = useState(0);
  const root = useRef(null),
    request = useRef(0),
    openRef = useRef(open);
  openRef.current = open;
  const categories = !type && !query.trim();
  const options = categories ? referenceTypes : rows;
  const currentIssue = rows.find(
    (row) => row.type === "issue" && row.id === issueId,
  );
  useEffect(() => {
    if (!open) return;
    setQuery(trigger.query);
    setType("");
  }, [open, trigger?.query]);
  async function load(offset = 0) {
    if (!openRef.current) return;
    const version = ++request.current;
    setLoading(true);
    setError("");
    try {
      const data = await api(
        "/api/assistant/references?" +
          new URLSearchParams({
            projectId,
            // A project conversation only references its own project's
            // objects (plus shared ones); a personal one searches them all.
            ...(projectId ? {} : { scope: "all" }),
            type,
            q: query.trim(),
            offset: String(offset),
          }),
      );
      if (version !== request.current || !openRef.current) return;
      setRows((old) =>
        offset ? [...old, ...(data.items || data)] : data.items || data,
      );
      setNext(data.nextOffset || 0);
    } catch (e) {
      if (version === request.current && openRef.current) setError(e.message);
    } finally {
      if (version === request.current && openRef.current) setLoading(false);
    }
  }
  useEffect(() => {
    if (!open) return;
    setRows([]);
    setNext(0);
    setActive(0);
    setError("");
    if (categories) {
      setLoading(false);
      return;
    }
    const timer = setTimeout(() => void load(), 180);
    return () => {
      clearTimeout(timer);
      request.current++;
    };
  }, [open, projectId, type, query, categories]);
  useEffect(() => {
    if (!open) return;
    function outside(event) {
      if (
        !root.current?.contains(event.target) &&
        event.target !== editor.current
      )
        onClose();
    }
    document.addEventListener("pointerdown", outside);
    return () => {
      document.removeEventListener("pointerdown", outside);
      request.current++;
    };
  }, [open]);
  function choose(option) {
    if (!open) return;
    if (categories) {
      setType(option.type);
      setQuery("");
      setActive(0);
    } else onSelect(option);
  }
  function keyDown(event) {
    if (!open) return false;
    if (event.nativeEvent?.isComposing) return false;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      onClose();
      editor.current?.focus();
      return true;
    }
    if (["ArrowDown", "ArrowUp"].includes(event.key)) {
      event.preventDefault();
      const index =
        (active + (event.key === "ArrowDown" ? 1 : -1) + options.length) %
        (options.length || 1);
      setActive(index);
      root.current
        ?.querySelectorAll('[role="option"]')
        [index]?.scrollIntoView({ block: "nearest" });
      return true;
    }
    if (event.key === "Enter" && !event.shiftKey) {
      const option = event.target.closest?.('[role="option"]');
      if (
        !option &&
        event.target !== editor.current &&
        event.target.tagName !== "INPUT"
      )
        return false;
      event.preventDefault();
      const index = option
        ? [...root.current.querySelectorAll('[role="option"]')].indexOf(option)
        : active;
      if (options[index] && !loading) choose(options[index]);
      return true;
    }
    return false;
  }
  useImperativeHandle(ref, () => ({ keyDown }));
  useLayoutEffect(() => {
    const node = root.current;
    if (!open || !node) return undefined;
    const fit = () => {
      const box = node.getBoundingClientRect();
      let top = 12;
      for (
        let parent = node.parentElement;
        parent;
        parent = parent.parentElement
      ) {
        const style = getComputedStyle(parent);
        const clips = [style.overflowY, style.overflowX].some((value) =>
          ["auto", "scroll", "hidden", "clip"].includes(value),
        );
        if (!clips) continue;
        top = Math.max(top, parent.getBoundingClientRect().top + 8);
      }
      const room = box.bottom - top;
      node.style.maxHeight = `${Math.min(440, Math.max(80, room))}px`;
    };
    fit();
    window.addEventListener("resize", fit);
    const scroller = node.closest(".empty-conversation, .message-scroll");
    scroller?.addEventListener("scroll", fit, { passive: true });
    return () => {
      window.removeEventListener("resize", fit);
      scroller?.removeEventListener("scroll", fit);
    };
  }, [open, presence.phase, options.length, categories]);
  if (!presence.mounted) return null;
  return (
    <div
      className="mention-picker"
      ref={root}
      role="dialog"
      aria-label="引用平台对象"
      aria-hidden={!open || undefined}
      inert={!open}
      data-phase={presence.phase}
      onKeyDown={keyDown}
    >
      <header>
        <button
          type="button"
          aria-label="返回所有引用类别"
          onClick={() => {
            setType("");
            setQuery("");
          }}
        >
          <HarnessIcon name="chevron-left" size={17} />
        </button>
        <HarnessIcon name="search" size={16} />
        <input
          aria-label="搜索引用对象"
          placeholder={
            type
              ? `搜索${referenceTypes.find((x) => x.type === type)?.label}…`
              : "搜索所有对象…"
          }
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <button type="button" onClick={() => { onClose(); editor.current?.focus({ preventScroll: true }); }} className="mention-close">
          关闭
        </button>
      </header>
      {onManageIssue && (type === "issue" || issueId) ? (
        <button
          type="button"
          className="mention-issue-context"
          disabled={!canManageIssue}
          onClick={onManageIssue}
          aria-haspopup="dialog"
        >
          <HarnessIcon name="doc" size={16} />
          <span>会话主需求</span>
          <small>
            {issueId ? `已关联 · ${currentIssue?.name || issueId}` : "未关联"}
          </small>
          <HarnessIcon name="chevron-right" size={14} />
        </button>
      ) : null}
      <div role="listbox" aria-label="平台对象" className="mention-options">
        {options.map((option, index) => {
          const icon = icons[option.type] || "doc";
          return (
            <button
              type="button"
              role="option"
              aria-selected={index === active}
              key={option.type + ":" + (option.id || option.type)}
              onPointerMove={() => setActive(index)}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => choose(option)}
            >
              <HarnessIcon name={icon} size={18} />
              <span>
                <strong>
                  {categories ? option.label : option.name || option.id}
                </strong>
                <small>
                  {categories
                    ? option.hint
                    : [
                        referenceTypes.find((x) => x.type === option.type)
                          ?.label,
                        option.projectName || "共享",
                        option.id,
                      ].join(" · ")}
                </small>
              </span>
              {categories ? (
                <HarnessIcon name="chevron-right" size={15} />
              ) : null}
            </button>
          );
        })}
        {loading ? (
          <p className="end-label" role="status">
            正在查找…
          </p>
        ) : null}
        {error ? (
          <p className="error" role="alert">
            {error}
            <button className="quiet-button" onClick={() => void load()}>
              重试
            </button>
          </p>
        ) : null}
        {!categories && !loading && !error && !rows.length ? (
          <p className="end-label">没有匹配的对象</p>
        ) : null}
        {next ? (
          <button
            className="quiet-button"
            disabled={loading}
            onClick={() => void load(next)}
          >
            加载更多
          </button>
        ) : null}
      </div>
    </div>
  );
});
