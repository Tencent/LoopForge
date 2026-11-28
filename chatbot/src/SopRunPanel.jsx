import React, {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import HarnessIcon from "./HarnessIcon";
import SlidingTabs from "./SlidingTabs";
import HarnessSelect from "./HarnessSelect";
import { api } from "./api";
import { RichContent, ArtifactPreview } from "./RichContent";
import KnowledgeSources from "./KnowledgeSources";
import {
  buildKnowledgeSummary,
  filterKnowledgeReadsForAttempt,
} from "./knowledge-citations.js";
import { IconButton, CredentialAction } from "./ui";
import ActivityTrace from "./ActivityTrace";
import ArtifactFileRow from "./ArtifactFileRow";
import { ArtifactLinks } from "./artifact-links.js";
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
  canReplayNode,
  decideRunNode,
  nodeReviewArtifacts,
  openRunPage,
  runArtifact,
  runAttentionNode,
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
              ) : node.status === "failed" ? (
                <span className="sop-node-failed-label">失败</span>
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

function NodeFiles({ run, files, title, activeFileId, onPreview }) {
  if (!files.length) return null;
  return (
    <section className="artifact-section sop-file-group" aria-label={title}>
      <h3>{title}</h3>
      {files.map((file) => (
        <ArtifactFileRow
          key={file.id}
          artifact={runArtifact(run, file)}
          selected={file.id === activeFileId}
          onSelect={onPreview}
        />
      ))}
    </section>
  );
}

function NodeDeliverables({
  run,
  files,
  title = "交付产物",
  activeFileId = null,
  onPreview,
  compact = false,
}) {
  if (!files.length) return null;
  return (
    <section
      className={`sop-deliverables${compact ? " is-compact" : ""}`}
      aria-label={title}
    >
      <div className="sop-deliverables-head">
        <FileText size={13} aria-hidden="true" />
        <span>{title}</span>
        <small>{files.length}</small>
      </div>
      <div className="sop-deliverables-list">
        {files.map((file) => (
          <ArtifactFileRow
            key={file.id}
            artifact={runArtifact(run, file)}
            selected={file.id === activeFileId}
            onSelect={onPreview}
          />
        ))}
      </div>
    </section>
  );
}

function CollapsibleMessage({ text, children }) {
  const [expanded, setExpanded] = useState(false);
  const raw = String(text || "");
  const isLong = raw.length > 260 || raw.split("\n").length > 5;
  if (!isLong) return children;
  return (
    <div className={`sop-collapsible${expanded ? " is-expanded" : ""}`}>
      <div className="sop-collapsible-body">{children}</div>
      <button
        type="button"
        className="sop-expand-toggle"
        aria-expanded={expanded}
        onClick={() => setExpanded((v) => !v)}
      >
        {expanded ? "收起内容" : "展开全文"}
      </button>
    </div>
  );
}

export function SopNodeDetail({
  run,
  nodeId,
  renderArtifacts,
  selectedFileId,
  onSelectFileId,
  onChanged,
}) {
  const [tab, setTab] = useState("conversation");
  const [localFile, setLocalFile] = useState(null);
  const [selectedAttempt, setSelectedAttempt] = useState(null);
  const [question, setQuestion] = useState("");
  const [artifactOpen, setArtifactOpen] = useState(null);
  const [replayOpen, setReplayOpen] = useState(false);
  const [replayNote, setReplayNote] = useState("");
  const [replayBusy, setReplayBusy] = useState(false);
  const [replayError, setReplayError] = useState("");
  const shell = useRef(null);
  const panel = usePanelPresence(!!artifactOpen);
  const standalone = !!renderArtifacts;
  const controlledPreview = typeof onSelectFileId === "function";
  const activeFileId = controlledPreview ? selectedFileId : localFile?.id;
  const setSelectedFile = (file) => {
    if (controlledPreview) onSelectFileId(file?.id || null);
    else setLocalFile(file || null);
  };

  const definition = run.workflowSnapshot?.nodes?.find(
    ({ id }) => id === nodeId,
  );
  const node = run.nodes?.[nodeId] || { nodeId, status: "pending" };
  const { own, handoffs } = nodeReviewArtifacts(run, node);
  const latestAttempt =
    node.attempt ??
    own.reduce((attempt, file) => Math.max(attempt, file.attempt || 0), 0);
  // The run overview can select an artifact from any saved node attempt.
  const selectedOwnFile = controlledPreview && selectedFileId
    ? own.find(({ id }) => id === selectedFileId)
    : null;
  const attempt = selectedOwnFile
    ? selectedOwnFile.attempt || latestAttempt
    : (selectedAttempt ?? latestAttempt);
  const latest = attempt === latestAttempt;
  const files = own.filter((file) =>
    file.attempt ? file.attempt === attempt : latest,
  );
  const reviewFiles = latest ? handoffs : [];
  const visibleFiles = [...files, ...reviewFiles];
  const activeFileIndex = activeFileId
    ? visibleFiles.findIndex(({ id }) => id === activeFileId)
    : -1;
  const activeFile = activeFileIndex >= 0 ? visibleFiles[activeFileIndex] : null;
  const preview = activeFile ? runArtifact(run, activeFile) : null;

  useLayoutEffect(() => {
    if (shell.current && artifactOpen === null && visibleFiles.length)
      setArtifactOpen(shell.current.clientWidth >= ARTIFACT_COMPACT_WIDTH);
  }, [artifactOpen, visibleFiles.length]);

  useEffect(() => {
    setReplayOpen(false);
    setReplayError("");
  }, [run.id, nodeId, node.status, node.attempt]);

  if (!definition) return null;
  const attempts = [
    ...new Set([latestAttempt, ...own.map((file) => file.attempt)]),
  ]
    .filter((value) => value > 0)
    .sort((a, b) => b - a);
  const waitingApproval =
    latest && node.status === "waiting_approval" && !run.pauseRequested;
  const hasUrgentDecision =
    latest &&
    (waitingApproval ||
      (node.status === "failed" && run.status === "failed") ||
      run.pauseRequested);
  const showToolbarReplay =
    latest &&
    !hasUrgentDecision &&
    run.reviewAccess?.canOperate &&
    canReplayNode(run, node);
  const discussionBlock = !run.reviewAccess?.canDiscuss
    ? "需要项目编辑权限才能提问"
    : !latest
      ? "切换到当前执行后可提问"
      : !canDiscussNode(node)
        ? "节点执行结束或进入审核后可提问"
        : "";

  async function triggerReplay() {
    if (replayBusy) return;
    setReplayBusy(true);
    setReplayError("");
    const note = replayNote.trim();
    try {
      await decideRunNode(
        run,
        node,
        "retry",
        {
          reason: note || "从当前节点重跑",
          ...(note ? { context: note } : {}),
        },
        () => {},
      );
      setReplayOpen(false);
      setReplayNote("");
      onChanged?.();
    } catch (e) {
      setReplayError(e.message);
    } finally {
      setReplayBusy(false);
    }
  }

  const actionsDock = latest ? (
    <RunNodeActions
      run={run}
      node={node}
      definition={definition}
      onChanged={onChanged}
    />
  ) : null;

  const openFileFromConversation = (artifact) => {
    setSelectedFile(artifact);
    if (standalone) setArtifactOpen(true);
  };

  const replayBox =
    replayOpen && showToolbarReplay ? (
      <div className="sop-replay-box" role="region" aria-label="从本节点重跑">
        <p className="sop-inline-note">
          保留上游节点结果，从「{definition.name || nodeId}」及下游重新执行。
        </p>
        <label className="sop-feedback">
          <span>补充说明（选填）</span>
          <textarea
            rows={2}
            disabled={replayBusy}
            value={replayNote}
            onChange={(e) => setReplayNote(e.target.value)}
            placeholder="告诉 Agent 重跑时需要注意或调整的内容"
          />
        </label>
        <div className="sop-decision-buttons">
          <button
            type="button"
            className="sop-action-button primary"
            disabled={replayBusy}
            onClick={triggerReplay}
          >
            <HarnessIcon name="refresh" size={13} />
            {replayBusy ? "正在重跑…" : "确认从本节点重跑"}
          </button>
          <button
            type="button"
            className="quiet-button"
            disabled={replayBusy}
            onClick={() => setReplayOpen(false)}
          >
            取消
          </button>
        </div>
        {replayError ? (
          <p className="sop-inline-error" role="alert">
            {replayError}
          </p>
        ) : null}
      </div>
    ) : null;

  const transcript = (
    <SopNodeChat
      key={`${node.attempt}:${node.reworkCount}`}
      standalone={standalone}
      run={run}
      node={node}
      text={question}
      onTextChange={setQuestion}
      disabledReason={discussionBlock}
      banner={standalone ? replayBox : null}
      actions={actionsDock}
    >
      <NodeConversation
        run={run}
        node={node}
        definition={definition}
        attempt={attempt}
        historical={!latest}
        files={files}
        reviewFiles={reviewFiles}
        activeFileId={preview?.id}
        onPreview={openFileFromConversation}
      />
    </SopNodeChat>
  );
  const toolbar = (
    <>
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
        {showToolbarReplay ? (
          <button
            type="button"
            className={`sop-action-button${replayOpen ? " is-active" : ""}`}
            onClick={() => {
              setReplayOpen((open) => !open);
              setReplayError("");
            }}
          >
            <HarnessIcon name="refresh" size={13} />
            从本节点重跑
          </button>
        ) : null}
        {node.childRunId ? (
          <button
            type="button"
            className="sop-action-button"
            onClick={() => openRunPage(node.childRunId)}
          >
            查看子 LOOP
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
    </>
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
            className="panel-toggle sop-review-artifacts-toggle"
            label="查看会话产物"
            aria-expanded={!!artifactOpen}
            aria-controls="conversation-artifacts"
            onClick={() => setArtifactOpen(!artifactOpen)}
          >
            <HarnessIcon name="chat-artifacts" size={16} />
            {visibleFiles.length ? (
              <span className="sop-panel-toggle-count">
                {visibleFiles.length}
              </span>
            ) : null}
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
        {!controlledPreview ? (
          <div className="artifact-preview-toolbar">
            <button
              type="button"
              className="artifact-preview-back"
              onClick={() => setSelectedFile(null)}
            >
              <ChevronLeft size={14} />
              {tab === "conversation" ? "返回会话" : "返回节点产物"}
            </button>
            {visibleFiles.length > 1 ? (
              <div className="artifact-preview-nav" role="group" aria-label="切换产物">
                <IconButton
                  label="上一个产物"
                  disabled={activeFileIndex <= 0}
                  onClick={() => setSelectedFile(visibleFiles[activeFileIndex - 1])}
                >
                  <ChevronLeft size={14} />
                </IconButton>
                <span className="artifact-preview-counter">
                  {activeFileIndex + 1} / {visibleFiles.length}
                </span>
                <IconButton
                  label="下一个产物"
                  disabled={activeFileIndex >= visibleFiles.length - 1}
                  onClick={() => setSelectedFile(visibleFiles[activeFileIndex + 1])}
                >
                  <ChevronRight size={14} />
                </IconButton>
              </div>
            ) : null}
          </div>
        ) : null}
        <div className="artifact-panel-preview">
          <ArtifactPreview
            artifact={preview}
            attempt={preview.attempt}
            knowledgeReads={preview.knowledgeReads}
          />
        </div>
      </>
    );
  }

  return (
    <>
      <div className="sop-node-detail-bar">
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
      </div>
      {!standalone ? replayBox : null}
      {tab === "conversation" ? (
        transcript
      ) : (
        <div className="node-artifacts">
          <NodeFiles
            run={run}
            files={files}
            title="本节点产物"
            activeFileId={preview?.id}
            onPreview={setSelectedFile}
          />
          <NodeFiles
            run={run}
            files={reviewFiles}
            title="上游待审产物"
            activeFileId={preview?.id}
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
  selectedFileId,
  onSelectFileId,
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
  const attention = !hasNode ? runAttentionNode(run) : null;
  const runArtifacts = run.artifacts || [];
  const Container = embedded ? "section" : "aside";
  return (
    <Container
      className={`${embedded ? "sop-run-embedded" : "side-panel"} sop-run-panel ${hasNode ? "node-conversation-view" : ""}`}
      aria-label="LOOP 运行过程"
    >
      {!embedded ? (
        <header className="sop-panel-header">
          <span>
            <GitBranch size={16} />
            LOOP 运行
          </span>
          <IconButton label="关闭运行侧边栏" onClick={onClose}>
            <X size={16} />
          </IconButton>
        </header>
      ) : null}
      <div className="sop-panel-scroll">
        {runs.length > 1 && !hasNode ? (
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
              selectedFileId={selectedFileId}
              onSelectFileId={onSelectFileId}
              onChanged={onChanged}
            />
          </>
        ) : (
          <>
            <div className="sop-run-heading">
              <h2>{run.workflowSnapshot?.name || "LOOP 运行"}</h2>
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
            {attention ? (
              <div
                className={`sop-run-attention status-${attention.status}`}
                role="status"
              >
                <div>
                  <strong>
                    「{attention.definition.name || attention.definition.id}」
                    {attention.status === "waiting_approval"
                      ? "等待审核"
                      : "执行失败"}
                  </strong>
                  <span>
                    {attention.status === "waiting_approval"
                      ? "点击右侧进入审核并查看产物"
                      : "点击右侧进入节点排查或重试"}
                  </span>
                </div>
                <button
                  type="button"
                  className="sop-action-button primary"
                  onClick={() => setNodeId(attention.definition.id)}
                >
                  {attention.status === "waiting_approval" ? "去审核" : "去处理"}
                </button>
              </div>
            ) : null}
            <SopNodeList run={run} onSelectNode={setNodeId} showArtifacts />
            {runArtifacts.length ? (
              <section
                className="artifact-section sop-run-artifacts"
                aria-label="本次运行产物"
              >
                <h3>本次运行产物 · {runArtifacts.length}</h3>
                {runArtifacts.map((file) => {
                  const producer =
                    nodes.find(({ id }) => id === file.nodeId)?.name ||
                    file.nodeId;
                  const artifact = runArtifact(run, file);
                  artifact.source = `${producer} · 第 ${file.attempt || 1} 次执行`;
                  return (
                    <ArtifactFileRow
                      key={file.id}
                      artifact={artifact}
                      onSelect={() => {
                        if (onSelectFileId) {
                          onSelectFileId(file.id, file.nodeId);
                        } else {
                          setNodeId(file.nodeId);
                        }
                      }}
                    />
                  );
                })}
              </section>
            ) : null}
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

function NodeConversation({
  run,
  node,
  definition,
  attempt,
  historical,
  files = [],
  reviewFiles = [],
  activeFileId = null,
  onPreview,
}) {
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
  const rawItems = groupNodeActivity(
    nodeConversationItems(
      events.filter((event) => matchesAttempt(event.context?.attempt)),
      files,
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
  // Keep mid-stream artifact cards inline when followed by further work
  // (more tool activity or multiple reply turns); aggregate final-turn
  // deliverables cleanly in one card at the end of the turn.
  const hasLaterWork = (index) => {
    let seenReply = false;
    for (let i = index + 1; i < rawItems.length; i++) {
      if (rawItems[i].type === "trace") return true;
      if (rawItems[i].type === "reply") {
        if (seenReply) return true;
        seenReply = true;
      }
    }
    return false;
  };
  const inlineItems = [];
  for (let i = 0; i < rawItems.length; i++) {
    const item = rawItems[i];
    if (item.type === "artifact") {
      if (!hasLaterWork(i)) continue;
      if (inlineItems.at(-1)?.type === "artifacts") {
        inlineItems.at(-1).artifacts.push(item.artifact);
      } else {
        inlineItems.push({ type: "artifacts", key: item.key, artifacts: [item.artifact] });
      }
    } else {
      inlineItems.push(item);
    }
  }
  const summaryFiles = files;
  const reviewFilesByNode = new Map();
  for (const file of reviewFiles) {
    if (!reviewFilesByNode.has(file.nodeId)) reviewFilesByNode.set(file.nodeId, []);
    reviewFilesByNode.get(file.nodeId).push(file);
  }
  const sourceIDs = new Set(
    historical ? [] : upstreamNodeIds(run.workflowSnapshot, node.nodeId),
  );
  const handoffs = (run.workflowSnapshot?.nodes || [])
    .filter(
      (source) =>
        sourceIDs.has(source.id) &&
        (run.nodes?.[source.id]?.output || reviewFilesByNode.has(source.id)),
    )
    .map((source) => ({
      ...source,
      output: run.nodes?.[source.id]?.output || "",
      artifacts: reviewFilesByNode.get(source.id) || [],
      knowledgeReads: filterKnowledgeReadsForAttempt(
        run.nodes?.[source.id]?.knowledgeReads,
        run.nodes?.[source.id]?.attempt,
      ),
    }));
  const artifactLinksValue = useMemo(
    () => ({
      artifacts: [...files, ...reviewFiles].map((file) => runArtifact(run, file)),
      open: onPreview,
    }),
    [run, files, reviewFiles, onPreview],
  );
  const [highlightedCite, setHighlightedCite] = useState(null);
  const sourcesRef = useRef(null);
  const highlightTimer = useRef(null);
  useEffect(() => () => window.clearTimeout(highlightTimer.current), []);
  const handleCitationClick = (citeIndex) => {
    setHighlightedCite(citeIndex);
    window.clearTimeout(highlightTimer.current);
    highlightTimer.current = window.setTimeout(
      () => setHighlightedCite(null),
      2000,
    );
    const row = sourcesRef.current?.querySelector?.(
      `[data-cite-index="${citeIndex}"]`,
    );
    row?.scrollIntoView?.({ behavior: "smooth", block: "nearest" });
  };
  // A historical attempt only cites the reads recorded for that attempt.
  const readsAttempt = historical ? attempt : node.attempt;
  const nodeReads = useMemo(
    () => filterKnowledgeReadsForAttempt(node.knowledgeReads, readsAttempt),
    [node.knowledgeReads, readsAttempt],
  );
  const nodeLive = !historical && ["running", "queued"].includes(node.status);
  const nodeOutput = historical ? "" : node.output || "";
  const nodeTexts = useMemo(() => {
    const narrationTexts = rawItems.flatMap((item) =>
      Array.isArray(item.steps)
        ? item.steps.filter((s) => s.kind === "text" && s.text).map((s) => s.text)
        : [],
    );
    return [
      nodeOutput,
      ...narrationTexts,
      ...rawItems.filter((i) => i.type === "reply").map((i) => i.message || ""),
    ];
  }, [nodeOutput, rawItems]);
  const nodeSummary = useMemo(
    () =>
      nodeReads.length
        ? buildKnowledgeSummary(nodeReads, nodeTexts, { live: nodeLive })
        : null,
    [nodeReads, nodeTexts, nodeLive],
  );
  const nodeKnowledgeText = nodeTexts.join("\n");
  const opening = { content: run.input, createdAt: run.createdAt };
  const findings = historical ? [] : node.gateResult?.findings || [];
  return (
    <ArtifactLinks.Provider value={artifactLinksValue}>
      <div className="node-conversation">
        {opening.content || handoffs.length || (!historical && node.feedback) ? (
          <article className="message user" aria-label="收到的消息">
            <UserTurn
              message={opening}
              messages={[opening]}
              index={0}
              author={{ name: "LOOP 运行" }}
              body={
                <>
                  {opening.content ? (
                    <CollapsibleMessage text={opening.content}>
                      <RichContent message={opening.content} />
                    </CollapsibleMessage>
                  ) : null}
                  {!historical && node.feedback ? (
                    <div className="node-handoff-message">
                      <span className="node-message-sender">本次补充说明</span>
                      <CollapsibleMessage text={node.feedback}>
                        <RichContent message={node.feedback} />
                      </CollapsibleMessage>
                    </div>
                  ) : null}
                  {handoffs.map((source) => (
                    <div className="node-handoff-message" key={source.id}>
                      <span className="node-message-sender">
                        {source.name || source.id}
                      </span>
                      {source.output ? (
                        <CollapsibleMessage text={source.output}>
                          <RichContent
                            message={source.output}
                            knowledgeReads={source.knowledgeReads}
                          />
                        </CollapsibleMessage>
                      ) : null}
                      {source.artifacts.length ? (
                        <NodeDeliverables
                          run={run}
                          files={source.artifacts}
                          title="产出产物"
                          activeFileId={activeFileId}
                          onPreview={onPreview}
                          compact
                        />
                      ) : null}
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
        {inlineItems.map((item) =>
          item.type === "artifacts" ? (
            <NodeDeliverables
              key={item.key}
              run={run}
              files={item.artifacts}
              title="产出产物"
              activeFileId={activeFileId}
              onPreview={onPreview}
              compact
            />
          ) : item.type === "reply" ? (
            <article
              className="message assistant"
              aria-label="节点回复"
              key={item.key}
            >
              <div className="message-body">
                <RichContent
                  message={item.message}
                  knowledgeReads={nodeReads}
                  knowledgeSummary={nodeSummary}
                  streaming={nodeLive}
                  onCitationClick={handleCitationClick}
                />
              </div>
            </article>
          ) : (
            <ActivityTrace
              key={item.key}
              steps={item.steps}
              startedAt={item.startedAt}
              finishedAt={item.finishedAt}
              live={item.live}
              citationProps={
                nodeReads.length
                  ? {
                      knowledgeReads: nodeReads,
                      knowledgeSummary: nodeSummary,
                      streaming: nodeLive,
                      onCitationClick: handleCitationClick,
                    }
                  : null
              }
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
        ) : !rawItems.length &&
          !files.length &&
          !reviewFiles.length &&
          !handoffs.length &&
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
        {nodeReads.length > 0 ? (
          <KnowledgeSources
            knowledgeReads={nodeReads}
            text={nodeKnowledgeText}
            summary={nodeSummary}
            highlightedIndex={highlightedCite}
            containerRef={sourcesRef}
          />
        ) : null}
        {!historical && node.error ? (
          <p className="error">
            {node.error}
            <CredentialAction text={node.error} />
          </p>
        ) : null}
        {summaryFiles.length ? (
          <NodeDeliverables
            run={run}
            files={summaryFiles}
            title="本节点产物"
            activeFileId={activeFileId}
            onPreview={onPreview}
          />
        ) : null}
        {reviewFiles.length &&
        (handoffs.length !== 1 ||
          handoffs[0]?.artifacts?.length !== reviewFiles.length) ? (
          <NodeDeliverables
            run={run}
            files={reviewFiles}
            title={
              definition?.type === "approval" ? "待审交付产物" : "上游交接产物"
            }
            activeFileId={activeFileId}
            onPreview={onPreview}
          />
        ) : null}
      </div>
    </ArtifactLinks.Provider>
  );
}
