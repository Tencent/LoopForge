import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import {
  activityFace,
  activityGroupKind,
  activityGroupSummary,
  traceBlocks,
  activityTarget,
  processBlocks,
} from "./activity";
import { elapsedSeconds, formatElapsed } from "./api";
import { RichContent } from "./RichContent";
import { CredentialAction } from "./ui";

const Brain = (props) => <HarnessIcon name="brain" {...props} />;
const Terminal = (props) => <HarnessIcon name="terminal" {...props} />;
const BookOpen = (props) => <HarnessIcon name="doc" {...props} />;
const Wrench = (props) => <HarnessIcon name="integration" {...props} />;
const Globe = (props) => <HarnessIcon name="globe" {...props} />;
const Search = (props) => <HarnessIcon name="search" {...props} />;
const Pencil = (props) => <HarnessIcon name="edit" {...props} />;
const Image = (props) => <HarnessIcon name="image" {...props} />;
const GitBranch = (props) => <HarnessIcon name="workflow" {...props} />;
const LoaderCircle = (props) => (
  <HarnessIcon name="spinner" motion="busy" {...props} />
);
const ChevronRight = (props) => <HarnessIcon name="chevron-right" {...props} />;
const AlertCircle = (props) => <HarnessIcon name="alert" {...props} />;
const Info = (props) => <HarnessIcon name="info" {...props} />;
const UserRound = (props) => <HarnessIcon name="user" {...props} />;
const CircleCheck = (props) => <HarnessIcon name="check" {...props} />;
const ListTodo = (props) => <HarnessIcon name="stage-log" {...props} />;
const icons = {
  thinking: Brain,
  command: Terminal,
  read: BookOpen,
  search: Search,
  edit: Pencil,
  image: Image,
  web: Globe,
  workflow: GitBranch,
  tool: Wrench,
  status: Info,
  role: UserRound,
  verify: CircleCheck,
  plan: ListTodo,
};
function Step({ step, live, interrupted }) {
  const [expanded, setExpanded] = useState(false);
  const face = activityFace(step);
  const target = activityTarget(step);
  const Icon = icons[face.kind];
  const pending = live && !step.finishedAt;
  const duration = elapsedSeconds(step.startedAt, step.finishedAt);
  const stopped = interrupted && !step.finishedAt;
  // Like Codex, a command that took a while says so in its row.
  const label = step.failed
    ? "执行失败"
    : stopped
      ? "已中断"
      : pending
        ? face.busy
        : face.kind === "command" && duration >= 1
          ? `已在${formatElapsed(duration)}内运行`
          : face.done;
  return (
    <div
      className={`execution-step ${pending ? "is-running" : ""} ${step.failed ? "is-failed" : ""}`}
      data-kind={face.kind}
      data-status={
        step.failed
          ? "failed"
          : stopped
            ? "interrupted"
            : pending
              ? "running"
              : "done"
      }
    >
      <Icon
        size={15}
        strokeWidth={1.7}
        aria-hidden="true"
        motion={pending && !stopped && !step.failed ? "busy" : "idle"}
      />
      <div className="execution-step-content">
        <details
          className={`execution-item ${face.kind === "command" ? "execution-command" : ""}`}
          onToggle={(e) => setExpanded(e.currentTarget.open)}
        >
          <summary className="execution-step-title">
            <span>{label}</span>
            {target ? (
              <span
                className={`execution-step-target ${["read", "edit"].includes(face.kind) ? "is-file" : ""}`}
                title={step.detail || target}
              >
                {target}
              </span>
            ) : null}
            {step.failed ? <AlertCircle size={13} aria-label="失败" /> : null}
            <ChevronRight className="execution-item-chevron" size={13} />
          </summary>
          {expanded ? (
            <div className="execution-item-expanded">
              {duration !== null ? (
                <small className="execution-duration">
                  耗时 {formatElapsed(duration)}
                </small>
              ) : null}
              {step.kind === "thinking" && step.text ? (
                <div className="execution-thought">
                  <RichContent message={step.text} />
                </div>
              ) : step.children?.length ? (
                <SubagentProcess step={step} live={pending} interrupted={interrupted} />
              ) : (
                <div className="execution-item-body">
                  {step.tool ? (
                    <div className="execution-tool-name">{step.tool}</div>
                  ) : null}
                  <pre className="execution-detail">
                    {step.detail || step.text || "未返回更多详情"}
                  </pre>
                  {step.output ? (
                    <pre className="execution-detail execution-output">
                      {step.output}
                    </pre>
                  ) : null}
                </div>
              )}
            </div>
          ) : null}
        </details>
        {step.failed ? (
          <CredentialAction
            text={[step.detail, step.output, step.text].join("\n")}
          />
        ) : null}
      </div>
    </div>
  );
}
// A dispatched subagent's own thoughts, tool calls and report, listed under
// its dispatch like a nested trace.
export function SubagentProcess({ step, live = false, interrupted = false }) {
  const blocks = processBlocks(step.children || [], { live });
  if (!blocks.length)
    return <p className="execution-empty">子 Agent 还没有产生过程。</p>;
  return (
    <div className="execution-subagent">
      <Narration blocks={blocks} live={live} interrupted={interrupted} expandAll={!live} />
    </div>
  );
}
function StepList({ steps, live, interrupted }) {
  return (
    <div className="execution-list">
      {steps.map((step) => (
        <Step key={step.id} step={step} live={live} interrupted={interrupted} />
      ))}
    </div>
  );
}
// Like Codex, a round's steps sit in a box of fixed height that scrolls; the
// edge with hidden steps fades. A running round follows its newest step until
// the reader scrolls up, and follows again once they scroll back down.
function ScrollList({ steps, live, interrupted, follow = false }) {
  const ref = useRef(null);
  const atEnd = useRef(true);
  const [fade, setFade] = useState({ top: false, bottom: false });
  const measure = () => {
    const el = ref.current;
    if (!el) return;
    const top = el.scrollTop > 1;
    const bottom = el.scrollTop + el.clientHeight < el.scrollHeight - 1;
    atEnd.current = !bottom;
    setFade((prev) =>
      prev.top === top && prev.bottom === bottom ? prev : { top, bottom },
    );
  };
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && follow && atEnd.current) el.scrollTop = el.scrollHeight;
    measure();
  });
  // Opening a step's details changes the height without a render here.
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => measure());
    if (el.firstElementChild) observer.observe(el.firstElementChild);
    return () => observer.disconnect();
  }, []);
  return (
    <div
      ref={ref}
      className={`execution-scroll ${fade.top ? "fade-top" : ""} ${fade.bottom ? "fade-bottom" : ""}`}
      data-follow={follow ? "true" : undefined}
      onScroll={measure}
    >
      <StepList steps={steps} live={live} interrupted={interrupted} />
    </div>
  );
}
// One round of tool calls between two paragraphs. The active round lists each
// step as it runs; once the model moves on it folds into one summary line.
// Opening a finished reply's process shows every round in full. Failed steps
// stay visible under the fold.
function ActivityGroup({ block, live, interrupted, expandAll }) {
  const active = live && !!block.active;
  const [open, setOpen] = useState(active || expandAll);
  useEffect(() => {
    setOpen(active || expandAll);
  }, [active, expandAll]);
  const { steps } = block;
  // A lone tool call is shown as itself; a lone thought still folds so raw
  // reasoning never takes a line of its own.
  if (steps.length === 1 && steps[0].kind !== "thinking")
    return <StepList steps={steps} live={live} interrupted={interrupted} />;
  // The running round has no title, like Codex: its steps are listed under
  // the note and scroll to the newest one.
  if (active && steps.some((step) => step.kind !== "thinking"))
    return (
      <div className="execution-group is-running" data-status="running">
        <ScrollList steps={steps} live={live} interrupted={interrupted} follow />
      </div>
    );
  const Icon = icons[activityGroupKind(steps)] || Wrench;
  const failed = steps.filter((step) => step.failed);
  return (
    <div
      className={`execution-group ${active ? "is-running" : ""}`}
      data-status={active ? "running" : "done"}
    >
      <details
        className="execution-group-fold"
        open={open}
        onToggle={(e) => {
          if (e.target === e.currentTarget) setOpen(e.currentTarget.open);
        }}
      >
        <summary className="execution-group-title">
          <Icon
            size={15}
            strokeWidth={1.7}
            aria-hidden="true"
            motion={active ? "busy" : "idle"}
          />
          <span className="execution-group-label">
            {activityGroupSummary(steps, { active })}
          </span>
          {failed.length ? (
            <span className="execution-group-failed-count">
              {failed.length} 步失败
            </span>
          ) : null}
          <ChevronRight className="execution-group-chevron" size={13} />
        </summary>
        {open ? (
          <div className="execution-group-body">
            <ScrollList steps={steps} live={live} interrupted={interrupted} />
          </div>
        ) : null}
      </details>
      {!open && failed.length ? (
        <div className="execution-group-body is-failures">
          <StepList steps={failed} live={live} interrupted={interrupted} />
        </div>
      ) : null}
    </div>
  );
}
function Narration({ blocks, live, interrupted, expandAll = false }) {
  return blocks.map((block) =>
    block.type === "text" ? (
      <div key={block.id} className="reply-trace-text">
        <RichContent message={block.text} />
      </div>
    ) : (
      <ActivityGroup
        key={block.id}
        block={block}
        live={live}
        interrupted={interrupted}
        expandAll={expandAll}
      />
    ),
  );
}
export function ReplyTimeline({
  steps = [],
  startedAt,
  finishedAt,
  live = false,
  interrupted = false,
}) {
  return (
    <div className="reply-heading">
      <ActivityTrace
        steps={steps}
        startedAt={startedAt}
        finishedAt={finishedAt}
        live={live}
        interrupted={interrupted}
        narration={traceBlocks(steps, { live })}
      />
    </div>
  );
}
// Like Codex: the elapsed time heads the reply with a faint full-width line
// under it. While a reply runs, the model's progress notes and each round of
// tool calls are listed below that line as they happen. A finished reply folds
// the whole process into its elapsed-time line; opening it shows every note,
// thought and step between the line and the answer.
export default function ActivityTrace({
  steps = [],
  startedAt,
  finishedAt,
  live = false,
  interrupted = false,
  narration = null,
}) {
  const [expanded, setExpanded] = useState(false);
  const [now, setNow] = useState(Date.now);
  // Fold the trace once its turn finishes (an SOP node keeps this instance).
  useEffect(() => {
    if (!live) setExpanded(false);
  }, [live]);
  useEffect(() => {
    if (!live) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [live, startedAt]);
  const seconds = elapsedSeconds(
    startedAt,
    live ? new Date(now).toISOString() : finishedAt,
  );
  const elapsed = seconds !== null ? ` ${formatElapsed(seconds)}` : "";
  const blocks = narration || traceBlocks(steps, { live });
  const divider = (
    <span className="reply-divider" role="separator" aria-label="回复正文分隔线" />
  );
  if (live) {
    const status = steps.filter((step) => step.kind === "status").at(-1)?.text;
    return (
      <div className="reply-timing execution-trace is-live">
        <div className="execution-summary-row">
          <span className="live-elapsed">正在处理{elapsed}</span>
        </div>
        {divider}
        <div className="execution-body" aria-label="执行过程">
          {blocks.length ? (
            <Narration blocks={blocks} live interrupted={interrupted} />
          ) : (
            <span className="execution-current" data-kind="status">
              <LoaderCircle size={15} strokeWidth={1.7} aria-hidden="true" />
              <span className="execution-current-text">
                {status || "正在处理…"}
              </span>
            </span>
          )}
        </div>
      </div>
    );
  }
  return (
    <details
      className="reply-timing execution-trace"
      open={expanded}
      onToggle={(e) => {
        if (e.target === e.currentTarget) setExpanded(e.currentTarget.open);
      }}
    >
      <summary>
        <span className="execution-summary-row">
          <span className="execution-total">已处理{elapsed}</span>
          <ChevronRight className="execution-chevron" size={14} />
        </span>
        {divider}
      </summary>
      {expanded ? (
        <div className="execution-body" aria-label="执行过程">
          {blocks.length ? (
            <Narration
              blocks={blocks}
              live={false}
              interrupted={interrupted}
              expandAll
            />
          ) : (
            <p className="execution-empty">这条历史回复未保存执行过程。</p>
          )}
        </div>
      ) : null}
    </details>
  );
}
