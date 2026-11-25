import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import SlidingTabs from "./SlidingTabs";
import HarnessSelect from "./HarnessSelect";
import { api } from "./api";
import { RichContent, ArtifactPreview } from "./RichContent";
import { IconButton, CredentialAction } from "./ui";
import ActivityTrace from "./ActivityTrace";
import ArtifactFileRow from "./ArtifactFileRow";
import { UserTurn } from "./ChatTurns";
import RunNodeActions from "./RunNodeActions";
import SopNodeChat from "./SopNodeChat";
import {
  groupNodeActivity,
  nodeConversationItems,
} from "./node-conversation.js";
import { approvalWaitReason } from "./runs.js";
import {
  canDiscussNode,
  nodeReviewArtifacts,
  openRunPage,
  runArtifact,
  upstreamNodeIds,
} from "./sop-review.js";
import { ARTIFACT_COMPACT_WIDTH } from "./useArtifactPanelLayout";
import { usePanelPresence } from "./usePanelPresence";
import "./sop-review.css";

const GitBranch = (props) => <HarnessIcon name="workflow" {...props} />;
const X = (props) => <HarnessIcon name="close" {...props} />;
const Check = (props) => <HarnessIcon name="check" {...props} />;
const Circle = (props) => <HarnessIcon name="circle" {...props} />;
const LoaderCircle = (props) => (
  <HarnessIcon name="spinner" motion="busy" {...props} />
);
const AlertCircle = (props) => <HarnessIcon name="alert" {...props} />;
const Clock3 = (props) => <HarnessIcon name="clock" {...props} />;
const ChevronLeft = (props) => <HarnessIcon name="chevron-left" {...props} />;
const ChevronRight = (props) => <HarnessIcon name="chevron-right" {...props} />;
const FileText = (props) => <HarnessIcon name="doc" {...props} />;
const MessageSquare = (props) => <HarnessIcon name="chat" {...props} />;
export function SopNodeList({
  run,
  selectedNodeId,
  onSelectNode,
  showArtifacts = false,
}) {
  const counts = new Map();
  for (const { nodeId } of run.artifacts || [])
    counts.set(nodeId, (counts.get(nodeId) || 0) + 1);
  return (
    <ol className="sop-node-list">
      {(run.workflowSnapshot?.nodes || []).map((definition) => {
        const node = run.nodes?.[definition.id] || { status: "pending" };
        const Icon = statusIcon(node.status);
        const selected = selectedNodeId === definition.id;
        const name = definition.name || definition.id;
        const status = runStatus[node.status] || node.status;
        const count = showArtifacts ? counts.get(definition.id) : 0;
        const brief = [
          status,
          node.status === "running" ? node.activity : "",
          count ? `${count} 项产物` : "",
        ]
          .filter(Boolean)
          .join(" · ");
        return (
          <li key={definition.id} className={`status-${node.status}`}>
            <button
              type="button"
              className={selected ? "is-selected" : undefined}
              aria-current={selected ? "true" : undefined}
              aria-label={
                showArtifacts
                  ? [name, status, count ? `${count} 项产物` : ""]
                      .filter(Boolean)
                      .join("，")
                  : undefined
              }
              onClick={() => onSelectNode(definition.id)}
            >
              <span className="sop-node-marker">
                <Icon size={15} />
              </span>
              <span>
                <strong>{name}</strong>
                {brief ? <small>{brief}</small> : null}
              </span>
              {node.status === "waiting_approval" && !run.pauseRequested ? (
                <span className="sop-node-review-label">审核</span>
              ) : !showArtifacts ? (
                <ChevronRight size={14} />
              ) : null}
            </button>
          </li>
        );
      })}
    </ol>
  );
}

function NodeFiles({ run, files, title, onPreview }) {
  if (!files.length) return null;
  return (
    <section className="artifact-section sop-file-group" aria-label={title}>
      <h3>{title}</h3>
      {files.map((file) => (
        <ArtifactFileRow
          key={file.id}
          artifact={runArtifact(run, file)}
          onSelect={onPreview}
        />
      ))}
    </section>
  );
}

export function SopNodeDetail({ run, nodeId, renderArtifacts, onChanged }) {
  const [tab, setTab] = useState("conversation");
  const [selectedFile, setSelectedFile] = useState(null);
  const [selectedAttempt, setSelectedAttempt] = useState(null);
  const [question, setQuestion] = useState("");
  const [artifactOpen, setArtifactOpen] = useState(null);
  const shell = useRef(null);
  const panel = usePanelPresence(!!artifactOpen);
  const standalone = !!renderArtifacts;
  const definition = run.workflowSnapshot?.nodes?.find(
    ({ id }) => id === nodeId,
  );
  const node = run.nodes?.[nodeId] || { nodeId, status: "pending" };
  const { own, handoffs } = nodeReviewArtifacts(run, node);
  const latestAttempt =
    node.attempt ??
    own.reduce((attempt, file) => Math.max(attempt, file.attempt || 0), 0);
  const attempt = selectedAttempt ?? latestAttempt;
  const latest = attempt === latestAttempt;
  const files = own.filter((file) =>
    file.attempt ? file.attempt === attempt : latest,
  );
  const reviewFiles = latest ? handoffs : [];
  const visibleFiles = [...files, ...reviewFiles];
  const activeFile = visibleFiles.find(({ id }) => id === selectedFile?.id);
  const preview = activeFile ? runArtifact(run, activeFile) : null;

  useLayoutEffect(() => {
    if (shell.current && artifactOpen === null && visibleFiles.length)
      setArtifactOpen(shell.current.clientWidth >= ARTIFACT_COMPACT_WIDTH);
  }, [artifactOpen, visibleFiles.length]);

  if (!definition) return null;
  const attempts = [
    ...new Set([latestAttempt, ...own.map((file) => file.attempt)]),
  ]
    .filter((value) => value > 0)
    .sort((a, b) => b - a);
  const waitingApproval =
    latest && node.status === "waiting_approval" && !run.pauseRequested;
  const discussionBlock = !run.reviewAccess?.canDiscuss
    ? "需要项目编辑权限才能提问"
    : !latest
      ? "切换到当前执行后可提问"
      : !canDiscussNode(node)
        ? "节点执行结束或进入审核后可提问"
        : "";
  const transcript = (
    <SopNodeChat
      key={`${node.attempt}:${node.reworkCount}`}
      standalone={standalone}
      run={run}
      node={node}
      text={question}
      onTextChange={setQuestion}
      disabledReason={discussionBlock}
    >
      <NodeConversation
        run={run}
        node={node}
        definition={definition}
        attempt={attempt}
        historical={!latest}
      />
      {!standalone && waitingApproval ? (
        <NodeFiles
          run={run}
          files={reviewFiles}
          title="上游待审产物"
          onPreview={setSelectedFile}
        />
      ) : null}
      {latest ? (
        <RunNodeActions
          run={run}
          node={node}
          definition={definition}
          onChanged={onChanged}
        />
      ) : null}
    </SopNodeChat>
  );
  const toolbar = (
    <div className="sop-node-toolbar">
      {attempts.length > 1 ? (
        <HarnessSelect
          ariaLabel="查看执行尝试"
          value={String(attempt)}
          onChange={(event) => {
            const next = Number(event.target.value);
            setSelectedAttempt(next === latestAttempt ? null : next);
            setSelectedFile(null);
          }}
          options={attempts.map((value) => ({
            value: String(value),
            label: `第 ${value} 次执行${value === latestAttempt ? "（当前）" : ""}`,
          }))}
        />
      ) : null}
      {node.childRunId ? (
        <button
          type="button"
          className="sop-action-button"
          onClick={() => openRunPage(node.childRunId)}
        >
          查看子 SOP
        </button>
      ) : null}
      {!standalone ? (
        <button
          type="button"
          className="sop-action-button"
          onClick={() => openRunPage(run.id, nodeId)}
        >
          <HarnessIcon name="arrow-right" size={14} />
          运行记录
        </button>
      ) : null}
    </div>
  );

  if (standalone) {
    const className = [
      "chat-shell sidebar-hidden sop-review-node",
      panel.mounted && "with-panel",
      panel.animating && "artifact-transitioning",
      panel.phase === "entering" && "artifact-entering",
      panel.phase === "closing" && "artifact-closing",
    ]
      .filter(Boolean)
      .join(" ");
    return (
      <div className={className} ref={shell}>
        <header className="conversation-header sop-review-node-header">
          <div className="sop-review-node-name">
            <h2>{definition.name || nodeId}</h2>
            {latest ? (
              <span className={`sop-status status-${node.status}`}>
                {runStatus[node.status] || node.status}
              </span>
            ) : (
              <span className="sop-status">历史执行</span>
            )}
            {latest && node.model ? (
              <span className="sop-review-node-model" title={node.model}>
                {node.model}
              </span>
            ) : null}
          </div>
          {toolbar}
          <IconButton
            className="panel-toggle"
            label="查看会话产物"
            aria-expanded={!!artifactOpen}
            aria-controls="conversation-artifacts"
            onClick={() => setArtifactOpen(!artifactOpen)}
          >
            <HarnessIcon name="chat-artifacts" size={16} />
          </IconButton>
        </header>
        {transcript}
        {panel.mounted
          ? renderArtifacts({
              artifacts: visibleFiles.map((file) => {
                const artifact = runArtifact(run, file);
                if (file.nodeId !== nodeId) {
                  const name =
                    run.workflowSnapshot.nodes.find(
                      ({ id }) => file.nodeId === id,
                    )?.name || file.nodeId;
                  artifact.source = `上游待审 · ${name}`;
                }
                return artifact;
              }),
              selected: preview,
              setSelected: setSelectedFile,
              open: !!artifactOpen,
              onClose: () => setArtifactOpen(false),
            })
          : null}
      </div>
    );
  }

  if (preview) {
    return (
      <>
        <div className="artifact-preview-toolbar">
          <button
            type="button"
            className="artifact-preview-back"
            onClick={() => setSelectedFile(null)}
          >
            <ChevronLeft size={14} />
            {tab === "conversation" ? "返回会话" : "返回节点产物"}
          </button>
        </div>
        <div className="artifact-panel-preview">
          <ArtifactPreview artifact={preview} />
        </div>
      </>
    );
  }

  return (
    <>
      <SlidingTabs className="node-tabs" aria-label="节点详情">
        <button
          type="button"
          role="tab"
          aria-selected={tab === "conversation"}
          onClick={() => setTab("conversation")}
        >
          <MessageSquare size={14} />
          会话过程
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "artifacts"}
          onClick={() => setTab("artifacts")}
        >
          <FileText size={14} />
          产物 {visibleFiles.length || ""}
        </button>
      </SlidingTabs>
      {toolbar}
      {tab === "conversation" ? (
        transcript
      ) : (
        <div className="node-artifacts">
          <NodeFiles
            run={run}
            files={files}
            title="本节点产物"
            onPreview={setSelectedFile}
          />
          <NodeFiles
            run={run}
            files={reviewFiles}
            title="上游待审产物"
            onPreview={setSelectedFile}
          />
          {!visibleFiles.length ? <p className="muted">暂无产物</p> : null}
        </div>
      )}
    </>
  );
}

export default function SopRunPanel({
  runs,
  onClose,
  embedded = false,
  selectedNodeId,
  onSelectNode,
  onChanged,
}) {
  const [runId, setRunId] = useState(runs[0]?.id);
  const [localNodeId, setLocalNodeId] = useState(null);
  const run = runs.find((item) => item.id === runId) || runs[0];
  if (!run) return null;
  const nodes = run.workflowSnapshot?.nodes || [];
  const nodeId = onSelectNode ? selectedNodeId : localNodeId;
  const setNodeId = onSelectNode || setLocalNodeId;
  const hasNode = nodes.some(({ id }) => id === nodeId);
  const completed = nodes.filter(({ id }) =>
    ["succeeded", "skipped"].includes(run.nodes?.[id]?.status),
  ).length;
  const Container = embedded ? "section" : "aside";
  return (
    <Container
      className={`${embedded ? "sop-run-embedded" : "side-panel"} sop-run-panel ${hasNode ? "node-conversation-view" : ""}`}
      aria-label="SOP 运行过程"
    >
      {!embedded ? (
        <header className="sop-panel-header">
          <span>
            <GitBranch size={16} />
            SOP 运行
          </span>
          <IconButton label="关闭运行侧边栏" onClick={onClose}>
            <X size={16} />
          </IconButton>
        </header>
      ) : null}
      <div className="sop-panel-scroll">
        {runs.length > 1 ? (
          <HarnessSelect
            ariaLabel="选择会话关联运行"
            value={run.id}
            onChange={(event) => {
              setRunId(event.target.value);
              setNodeId(null);
            }}
            options={runs.map((item) => ({
              value: item.id,
              label: item.workflowSnapshot?.name || item.id,
            }))}
          />
        ) : null}
        {hasNode ? (
          <>
            {!embedded ? (
              <button
                type="button"
                className="back-link"
                onClick={() => setNodeId(null)}
              >
                <ChevronLeft size={14} />
                全部流程节点
              </button>
            ) : null}
            <SopNodeDetail
              key={`${run.id}:${nodeId}`}
              run={run}
              nodeId={nodeId}
              onChanged={onChanged}
            />
          </>
        ) : (
          <>
            <div className="sop-run-heading">
              <h2>{run.workflowSnapshot?.name || "SOP 运行"}</h2>
              <div className="sop-run-heading-actions">
                <span className={`sop-status status-${run.status}`}>
                  {runStatus[run.status] || run.status}
                </span>
                <button
                  type="button"
                  className="sop-action-button"
                  onClick={() => openRunPage(run.id)}
                >
                  <HarnessIcon name="arrow-right" size={14} />
                  查看运行记录
                </button>
              </div>
              <small>
                {completed} / {nodes.length} 个节点已完成
              </small>
              <div className="sop-progress">
                <i
                  style={{
                    width: `${nodes.length ? (completed / nodes.length) * 100 : 0}%`,
                  }}
                />
              </div>
            </div>
            <SopNodeList run={run} onSelectNode={setNodeId} />
            <footer className="sop-run-footnote">{run.id}</footer>
          </>
        )}
      </div>
    </Container>
  );
}

export const runStatus = {
  succeeded: "已完成",
  running: "运行中",
  waiting: "等待审批",
  waiting_approval: "等待审批",
  pending: "待执行",
  queued: "排队中",
  failed: "失败",
  canceled: "已取消",
  cancelled: "已取消",
  skipped: "已跳过",
  paused: "已暂停",
  pending_input: "等待输入",
};
const statusIcon = (status) =>
  status === "succeeded"
    ? Check
    : status === "running"
      ? LoaderCircle
      : status === "failed"
        ? AlertCircle
        : ["waiting", "waiting_approval"].includes(status)
          ? Clock3
          : Circle;

function NodeConversation({ run, node, definition, attempt, historical }) {
  const [events, setEvents] = useState([]),
    [more, setMore] = useState(false),
    [loading, setLoading] = useState(false),
    [error, setError] = useState("");
  const loadRef = useRef(null),
    statusRef = useRef(node.status);
  statusRef.current = node.status;
  useEffect(() => {
    let cancelled = false,
      busy = false,
      cursor = 0;
    setEvents([]);
    setMore(false);
    async function load() {
      if (busy || cancelled) return;
      busy = true;
      setLoading(true);
      try {
        // Token telemetry can fill the first page before publication. Drain a
        // bounded batch automatically, preserving the real publication order.
        for (let batch = 0; batch < 10; batch++) {
          const page = await api(
            `/api/runs/${encodeURIComponent(run.id)}/telemetry?nodeId=${encodeURIComponent(node.nodeId)}&after=${cursor}&limit=500&expanded=true`,
          );
          if (cancelled) return;
          setEvents((old) => {
            const seen = new Set(old.map((e) => e.sequence));
            return [
              ...old,
              ...page.events.filter((e) => !seen.has(e.sequence)),
            ];
          });
          setMore(page.hasMore);
          setError("");
          const next = page.nextAfter || cursor;
          if (!page.hasMore || next <= cursor) {
            cursor = next;
            break;
          }
          cursor = next;
        }
      } catch (e) {
        if (!cancelled) setError(e.message);
      } finally {
        busy = false;
        if (!cancelled) setLoading(false);
      }
    }
    loadRef.current = load;
    void load();
    const timer = setInterval(() => {
      if (
        !["succeeded", "failed", "skipped", "canceled", "cancelled"].includes(
          statusRef.current,
        )
      )
        void load();
    }, 2500);
    return () => {
      cancelled = true;
      clearInterval(timer);
      loadRef.current = null;
    };
  }, [run.id, node.nodeId, node.attempt]);
  useEffect(() => {
    void loadRef.current?.();
  }, [node.status]);
  const matchesAttempt = (value) => (value ? value === attempt : !historical);
  const items = groupNodeActivity(
    nodeConversationItems(
      events.filter((event) => matchesAttempt(event.context?.attempt)),
      [],
      historical ? { nodeId: node.nodeId } : node,
      {
        hasMore: more,
      },
    ),
    {
      live:
        !historical &&
        ["running", "queued", "pending_input"].includes(node.status),
    },
  );
  const sourceIDs = new Set(
    historical ? [] : upstreamNodeIds(run.workflowSnapshot, node.nodeId),
  );
  const handoffs = (run.workflowSnapshot?.nodes || [])
    .filter(
      (source) => sourceIDs.has(source.id) && run.nodes?.[source.id]?.output,
    )
    .map((source) => ({ ...source, output: run.nodes[source.id].output }));
  const opening = { content: run.input, createdAt: run.createdAt };
  const findings = historical ? [] : node.gateResult?.findings || [];
  return (
    <div className="node-conversation">
      {opening.content || handoffs.length || (!historical && node.feedback) ? (
        <article className="message user" aria-label="收到的消息">
          <UserTurn
            message={opening}
            messages={[opening]}
            index={0}
            author={{ name: "SOP 运行" }}
            body={
              <>
                {opening.content}
                {!historical && node.feedback ? (
                  <div className="node-handoff-message">
                    <span className="node-message-sender">本次补充说明</span>
                    <RichContent message={node.feedback} />
                  </div>
                ) : null}
                {handoffs.map((source) => (
                  <div className="node-handoff-message" key={source.id}>
                    <span className="node-message-sender">
                      {source.name || source.id}
                    </span>
                    <RichContent message={source.output} />
                  </div>
                ))}
              </>
            }
          />
        </article>
      ) : null}
      {error ? (
        <p className="error" role="alert">
          {error}{" "}
          <button
            type="button"
            className="quiet-button"
            onClick={() => void loadRef.current?.()}
          >
            重试加载
          </button>
        </p>
      ) : null}
      {items.map((item) =>
        item.type === "reply" ? (
          <article
            className="message assistant"
            aria-label="节点回复"
            key={item.key}
          >
            <div className="message-body">
              <RichContent message={item.message} />
            </div>
          </article>
        ) : (
          <ActivityTrace
            key={item.key}
            steps={item.steps}
            startedAt={item.startedAt}
            finishedAt={item.finishedAt}
            live={item.live}
          />
        ),
      )}
      {more ? (
        <button
          className="quiet-button"
          disabled={loading}
          onClick={() => void loadRef.current?.()}
        >
          {loading ? "正在读取完整会话…" : "加载更多执行记录"}
        </button>
      ) : null}
      {!historical &&
      definition?.type === "approval" &&
      node.status !== "pending" &&
      node.status !== "waiting_approval" ? (
        <article
          className="message assistant node-approval-reason"
          aria-label="人工门禁说明"
        >
          <strong>人工门禁</strong>
          <p>{approvalWaitReason(run, node.nodeId || definition.id)}</p>
        </article>
      ) : !items.length &&
        !loading &&
        (historical || node.status !== "waiting_approval") ? (
        <p className="muted">
          {node.status === "pending"
            ? "节点尚未开始。"
            : "暂无可读取的执行记录。"}
        </p>
      ) : null}
      {findings.length ? (
        <details className="sop-findings">
          <summary>审核发现（{findings.length}）</summary>
          <ul>
            {findings.map((finding, index) => (
              <li key={index}>
                <strong>{finding.title}</strong>
                {finding.evidence ? (
                  <RichContent message={finding.evidence} />
                ) : null}
              </li>
            ))}
          </ul>
        </details>
      ) : null}
      {!historical && node.error ? (
        <p className="error">
          {node.error}
          <CredentialAction text={node.error} />
        </p>
      ) : null}
    </div>
  );
}
