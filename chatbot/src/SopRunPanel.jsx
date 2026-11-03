import React, { useEffect, useRef, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import SlidingTabs from "./SlidingTabs";
import HarnessSelect from "./HarnessSelect";
import { api } from "./api";
import { RichContent, ArtifactPreview, ImageLightbox } from "./RichContent";
import { IconButton, CredentialAction } from "./ui";
import ActivityTrace from "./ActivityTrace";
import {
  groupNodeActivity,
  nodeConversationItems,
} from "./node-conversation.js";
import { approvalWaitReason } from "./runs.js";

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
const FileImage = (props) => <HarnessIcon name="image" {...props} />;
const Download = (props) => <HarnessIcon name="download" {...props} />;

function runArtifact(run, artifact) {
  const path = artifact.path || artifact.name;
  const kind = /\.html?$/i.test(path)
    ? "html"
    : /\.svg$/i.test(path)
      ? "svg"
      : /\.mmd$/i.test(path)
        ? "mermaid"
        : "attachment";
  return {
    ...artifact,
    name: path,
    kind,
    url: `/api/runs/${encodeURIComponent(run.id)}/artifacts/${encodeURIComponent(artifact.id)}`,
    mimeType: artifact.mediaType,
    source: "节点产物",
  };
}

function NodeArtifact({ run, artifact, onPreview }) {
  const file = runArtifact(run, artifact);
  const image = file.mimeType?.startsWith("image/");
  const [zoom, setZoom] = useState(false);
  return (
    <div className="node-inline-artifact" data-artifact-id={artifact.id}>
      {zoom ? (
        <ImageLightbox
          src={file.url}
          alt={file.name}
          onClose={() => setZoom(false)}
        />
      ) : null}
      <button
        className="node-artifact-open"
        onClick={() => onPreview(file)}
        onDoubleClick={image ? () => setZoom(true) : undefined}
        title={image ? "单击预览，双击查看大图" : undefined}
        aria-label={`预览 ${file.name}`}
      >
        {image ? <img src={file.url} alt={file.name} loading="lazy" /> : null}
        <span className="node-artifact-file">
          {image ? <FileImage size={18} /> : <FileText size={18} />}
          <span>
            <strong>{file.name}</strong>
            <small>
              {image ? "图片" : "文件"}
              {file.size
                ? ` · ${file.size < 1024 ? `${file.size} B` : `${Math.ceil(file.size / 1024)} KB`}`
                : ""}
            </small>
          </span>
          <ChevronRight size={14} />
        </span>
      </button>
      <a
        className="icon-button node-artifact-download"
        href={file.url}
        download={file.name}
        aria-label={`下载 ${file.name}`}
      >
        <Download size={15} />
      </a>
    </div>
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

function NodeConversation({ run, node, definition, onPreview }) {
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
  const items = groupNodeActivity(
    nodeConversationItems(events, run.artifacts || [], node, {
      hasMore: more,
    }),
    { live: ["running", "queued"].includes(node.status) },
  );
  const task = definition?.taskPrompt || definition?.prompt || "";
  const sourceIDs = new Set(
    (run.workflowSnapshot?.edges || [])
      .filter((edge) => edge.target === node.nodeId)
      .map((edge) => edge.source),
  );
  const handoffs = (run.workflowSnapshot?.nodes || [])
    .filter(
      (source) => sourceIDs.has(source.id) && run.nodes?.[source.id]?.output,
    )
    .map((source) => ({ ...source, output: run.nodes[source.id].output }));
  const opening = [task, run.input !== task ? run.input : ""]
    .filter(Boolean)
    .join("\n\n");
  return (
    <div className="node-conversation">
      {opening || handoffs.length ? (
        <article
          className="node-message node-message-user"
          aria-label="收到的消息"
        >
          {opening ? <RichContent message={opening} /> : null}
          {handoffs.map((source) => (
            <div className="node-handoff-message" key={source.id}>
              <span className="node-message-sender">
                {source.name || source.id}
              </span>
              <RichContent message={source.output} />
            </div>
          ))}
        </article>
      ) : null}
      {error ? (
        <p className="error" role="alert">
          {error}
        </p>
      ) : null}
      {items.map((item) =>
        item.type === "artifact" ? (
          <NodeArtifact
            key={item.key}
            run={run}
            artifact={item.artifact}
            onPreview={onPreview}
          />
        ) : item.type === "reply" ? (
          <article
            className="node-message node-message-assistant node-result"
            aria-label="节点回复"
            key={item.key}
          >
            <RichContent message={item.message} />
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
      {definition?.type === "approval" && node.status !== "pending" ? (
        <article
          className="node-message node-message-assistant node-approval-reason"
          aria-label="人工门禁说明"
        >
          <strong>
            {node.status === "waiting_approval"
              ? "为什么需要人工确认"
              : "人工门禁"}
          </strong>
          <p>{approvalWaitReason(run, node.nodeId || definition.id)}</p>
        </article>
      ) : !items.length && !loading ? (
        <p className="muted">
          {node.status === "pending"
            ? "节点尚未开始。"
            : "暂无可读取的执行记录。"}
        </p>
      ) : null}
      {node.error ? (
        <p className="error">
          {node.error}
          <CredentialAction text={node.error} />
        </p>
      ) : null}
    </div>
  );
}
export default function SopRunPanel({
  runs,
  onClose,
  embedded = false,
  selectedNodeId,
  onSelectNode,
}) {
  const [runId, setRunId] = useState(runs[0]?.id),
    [localNodeId, setLocalNodeId] = useState(null),
    [tab, setTab] = useState("conversation"),
    [artifact, setArtifact] = useState(null);
  const nodeId = onSelectNode ? selectedNodeId : localNodeId;
  const setNodeId = onSelectNode || setLocalNodeId;
  const run = runs.find((r) => r.id === runId) || runs[0];
  useEffect(() => {
    setNodeId(null);
    setArtifact(null);
  }, [run?.id]);
  useEffect(() => {
    setArtifact(null);
    setTab("conversation");
  }, [nodeId]);
  if (!run) return null;
  const nodes = run.workflowSnapshot?.nodes || [];
  const definition = nodes.find((n) => n.id === nodeId);
  const node =
    run.nodes?.[nodeId] || (definition ? { nodeId, status: "pending" } : null);
  const completed = nodes.filter((n) =>
    ["succeeded", "skipped"].includes(run.nodes?.[n.id]?.status),
  ).length;
  const artifacts = (run.artifacts || []).filter(
    (a) => !nodeId || a.nodeId === nodeId,
  );
  const title = run.workflowSnapshot?.name || "SOP 运行";
  const Container = embedded ? "section" : "aside";
  return (
    <Container
      className={`${embedded ? "sop-run-embedded" : "side-panel"} sop-run-panel ${nodeId ? "node-conversation-view" : ""}`}
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
            onChange={(e) => setRunId(e.target.value)}
            options={runs.map((r) => ({ value: r.id, label: r.workflowSnapshot?.name || r.id }))}
          />
        ) : null}
        {!nodeId ? (
          <div className="sop-run-heading">
            <h2>{title}</h2>
            <span className={`sop-status status-${run.status}`}>
              {runStatus[run.status] || run.status}
            </span>
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
        ) : null}
        {artifact ? (
          <>
            <button className="back-link" onClick={() => setArtifact(null)}>
              <ChevronLeft size={14} />
              {tab === "conversation" ? "返回会话" : "返回节点产物"}
            </button>
            <ArtifactPreview artifact={artifact} />
          </>
        ) : nodeId && node ? (
          <>
            {!embedded ? (
              <button className="back-link" onClick={() => setNodeId(null)}>
                <ChevronLeft size={14} />
                全部流程节点
              </button>
            ) : null}
            <SlidingTabs className="node-tabs" aria-label="节点详情">
              <button
                role="tab"
                aria-selected={tab === "conversation"}
                onClick={() => setTab("conversation")}
              >
                <MessageSquare size={14} />
                会话过程
              </button>
              <button
                role="tab"
                aria-selected={tab === "artifacts"}
                onClick={() => setTab("artifacts")}
              >
                <FileText size={14} />
                产物 {artifacts.length || ""}
              </button>
            </SlidingTabs>
            {tab === "conversation" ? (
              <NodeConversation
                key={`${run.id}:${nodeId}`}
                run={run}
                node={node}
                definition={definition}
                onPreview={setArtifact}
              />
            ) : (
              <div className="node-artifacts">
                {artifacts.map((a) => (
                  <button
                    className="artifact-row"
                    key={a.id}
                    onClick={() => setArtifact(runArtifact(run, a))}
                  >
                    <FileText size={17} />
                    <span>
                      <strong>{a.path || a.name}</strong>
                      <small>
                        第 {a.attempt} 次执行 · {a.size} 字节
                      </small>
                    </span>
                    <ChevronRight size={14} />
                  </button>
                ))}
                {!artifacts.length ? (
                  <p className="muted">这个节点尚未生成产物。</p>
                ) : null}
              </div>
            )}
          </>
        ) : (
          <ol className="sop-node-list">
            {nodes.map((definition, index) => {
              const node = run.nodes?.[definition.id] || { status: "pending" };
              const Icon = statusIcon(node.status);
              return (
                <li key={definition.id} className={`status-${node.status}`}>
                  <button
                    onClick={() => {
                      setNodeId(definition.id);
                      setTab("conversation");
                      setArtifact(null);
                    }}
                  >
                    <span className="sop-node-marker">
                      <Icon size={15} />
                    </span>
                    <span>
                      <strong>{definition.name || definition.id}</strong>
                      <small>
                        {runStatus[node.status] || node.status}
                        {node.activity && node.status === "running"
                          ? ` · ${node.activity}`
                          : ""}
                      </small>
                    </span>
                    <ChevronRight size={14} />
                  </button>
                </li>
              );
            })}
          </ol>
        )}
        {!nodeId ? (
          <footer className="sop-run-footnote">{run.id}</footer>
        ) : null}
      </div>
    </Container>
  );
}
