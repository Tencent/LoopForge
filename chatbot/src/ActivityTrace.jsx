import React, { useEffect, useMemo, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import { activityFace, activityTarget } from "./activity";
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
  const label = step.failed
    ? "执行失败"
    : stopped
      ? "已中断"
      : pending
        ? face.busy
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
export default function ActivityTrace({
  steps = [],
  startedAt,
  finishedAt,
  live = false,
  interrupted = false,
}) {
  // Like Codex, a running reply lists each step as it happens; a finished
  // reply folds into its elapsed-time summary.
  const [expanded, setExpanded] = useState(live);
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
  const current = useMemo(
    () =>
      [...steps].reverse().find((s) => !s.finishedAt && s.kind !== "status") ||
      [...steps].reverse().find((s) => !s.finishedAt),
    [steps],
  );
  const face = current ? activityFace(current) : null;
  const Icon = face ? icons[face.kind] : LoaderCircle;
  const currentLabel = face?.busy || "正在处理…";
  const target = current ? activityTarget(current) : "";
  const activities = steps.filter(
    (step) =>
      step.kind !== "status" &&
      (step.kind !== "thinking" || step.text || (live && !step.finishedAt)),
  );
  const connections = steps.filter((step) => step.kind === "status");
  const visible = activities.length ? activities : connections.slice(-1);
  return (
    <details
      className={`reply-timing execution-trace ${live ? "is-live" : ""}`}
      open={expanded}
      onToggle={(e) => {
        if (e.target === e.currentTarget) setExpanded(e.currentTarget.open);
      }}
    >
      <summary>
        <span className="execution-summary-row">
          <span className={live ? "live-elapsed" : "execution-total"}>
            已处理{seconds !== null ? ` ${formatElapsed(seconds)}` : ""}
          </span>
          <ChevronRight className="execution-chevron" size={14} />
        </span>
        <span
          className="reply-divider"
          role="separator"
          aria-label="回复正文分隔线"
        />
        {live && !expanded ? (
          <span
            className="execution-current"
            data-kind={face?.kind || "status"}
          >
            <Icon
              size={15}
              strokeWidth={1.7}
              aria-hidden="true"
              motion="busy"
            />
            <span className="execution-current-text">
              {currentLabel}
              {target ? ` ${target}` : ""}
            </span>
          </span>
        ) : null}
      </summary>
      <div className="execution-body" aria-label="执行过程">
        {steps.length ? (
          <div className="execution-list">
            {visible.map((step) => (
              <Step
                key={step.id}
                step={step}
                live={live}
                interrupted={interrupted}
              />
            ))}
          </div>
        ) : (
          <p className="execution-empty">
            {live
              ? "等待模型返回思考或工具活动…"
              : "这条历史回复未保存执行过程。"}
          </p>
        )}
      </div>
    </details>
  );
}
