import React, { useEffect, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import SlidingTabs from "./SlidingTabs";
import ActivityTrace from "./ActivityTrace";
import { RichContent } from "./RichContent";

export function subagentStatus(agent, live) {
  const last = agent.dispatches.at(-1);
  if (last?.failed) return { label: "失败", tone: "failed" };
  if (!last?.finishedAt)
    return live
      ? { label: "运行中", tone: "running" }
      : { label: "已中断", tone: "interrupted" };
  return { label: "已完成", tone: "done" };
}

export function subagentName(agent) {
  return agent.role || agent.subagent || "子 Agent";
}

// The subagent's last paragraph is its report, which also comes back as the
// Agent result; it is shown once, as the reply.
function processSteps(step) {
  const report = (step.output || "").replace(/\s+/g, "");
  return (step.children || []).filter(
    (child) =>
      !(
        report &&
        child.kind === "text" &&
        report.includes((child.text || "").replace(/\s+/g, ""))
      ),
  );
}

// SubagentSession shows one subagent like an SOP node: what Main Bot asked,
// how it worked and what it reported, once per dispatch when it was resumed,
// plus what it published.
export function SubagentSession({ agent, live = false, artifacts = [], onOpenArtifact }) {
  const [tab, setTab] = useState("conversation");
  useEffect(() => setTab("conversation"), [agent.key]);
  const published = agent.published.map((title) => ({
    title,
    artifact: [...artifacts].reverse().find((item) => item.name === title),
  }));
  return (
    <section
      className="sop-run-embedded sop-run-panel node-conversation-view subagent-session"
      aria-label="子 Agent 会话"
    >
      <div className="sop-panel-scroll">
        <SlidingTabs className="node-tabs" aria-label="子 Agent 详情">
          <button
            role="tab"
            aria-selected={tab === "conversation"}
            onClick={() => setTab("conversation")}
          >
            <HarnessIcon name="chat" size={14} />
            会话过程
          </button>
          <button
            role="tab"
            aria-selected={tab === "artifacts"}
            onClick={() => setTab("artifacts")}
          >
            <HarnessIcon name="doc" size={14} />
            产物 {published.length || ""}
          </button>
        </SlidingTabs>
        {tab === "conversation" ? (
          <div className="node-conversation">
            {agent.dispatches.map((step) => {
              const running = live && !step.finishedAt;
              const steps = processSteps(step);
              return (
                <React.Fragment key={step.id}>
                  {step.input || step.detail ? (
                    <article
                      className="node-message node-message-user"
                      aria-label="收到的任务"
                    >
                      <span className="node-message-sender">Main Bot</span>
                      <RichContent message={step.input || step.detail} />
                    </article>
                  ) : null}
                  {steps.length || running ? (
                    <ActivityTrace
                      steps={steps}
                      startedAt={step.startedAt}
                      finishedAt={step.finishedAt !== "done" ? step.finishedAt : null}
                      live={running}
                      interrupted={!live && !step.finishedAt}
                    />
                  ) : null}
                  {step.output ? (
                    <article
                      className="node-message node-message-assistant node-result"
                      aria-label="子 Agent 汇报"
                    >
                      <RichContent message={step.output} />
                    </article>
                  ) : step.failed ? (
                    <p className="error">这次派发失败了。</p>
                  ) : null}
                </React.Fragment>
              );
            })}
          </div>
        ) : (
          <div className="node-artifacts">
            {published.map(({ title, artifact }, index) =>
              artifact ? (
                <button
                  type="button"
                  className="artifact-row"
                  key={`${title}-${index}`}
                  onClick={() => onOpenArtifact?.(artifact)}
                >
                  <HarnessIcon name="doc" size={17} />
                  <span>
                    <strong>{title}</strong>
                    <small>{artifact.source || "已发布"}</small>
                  </span>
                  <HarnessIcon name="chevron-right" size={14} />
                </button>
              ) : (
                <div className="artifact-row" key={`${title}-${index}`}>
                  <HarnessIcon name="doc" size={17} />
                  <span>
                    <strong>{title}</strong>
                    <small>已发布</small>
                  </span>
                </div>
              ),
            )}
            {!published.length ? (
              <p className="muted">这个子 Agent 没有发布产物。</p>
            ) : null}
          </div>
        )}
      </div>
    </section>
  );
}
