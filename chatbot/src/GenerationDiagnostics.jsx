import React, { useEffect, useRef, useState } from "react";
import { api, sessionPath } from "./api.js";
import HarnessIcon from "./HarnessIcon";
import { CredentialAction } from "./ui";
import { CLIInstallAction } from "./cli-install.jsx";
import "./generation-diagnostics.css";

const phases = {
  setup: "启动与配置",
  execution: "CLI 执行",
  protocol: "结果解析",
  provider: "模型服务",
  connection: "连接模型服务",
  server: "平台服务",
  orchestration: "对话处理",
};
const statuses = {
  running: "执行中",
  completed: "已完成",
  failed: "失败",
  cancelled: "已停止",
  interrupted: "已中断",
  unfinished: "未完成",
};
const processStates = {
  not_started: "未启动",
  running: "运行中",
  start_failed: "启动失败",
  exited: "已退出",
  unknown_after_interruption: "中断后未知",
};
const toolSources = {
  gateway: "平台网关",
  cli: "CLI 协议",
};
const time = (value) =>
  value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";

async function copyText(text) {
  if (navigator.clipboard?.writeText)
    return navigator.clipboard.writeText(text);
  const input = document.createElement("textarea");
  input.value = text;
  input.style.position = "fixed";
  input.style.opacity = "0";
  document.body.append(input);
  try {
    input.select();
    if (!document.execCommand("copy"))
      throw new Error("请手动选择诊断内容复制");
  } finally {
    input.remove();
  }
}

export default function GenerationDiagnostics({
  sessionId,
  generationId,
  failure,
  onCLIInstall,
  compact = false,
  readOnly = false,
}) {
  const [open, setOpen] = useState(false);
  const [report, setReport] = useState(null);
  const [loading, setLoading] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const identity = `${sessionId}/${generationId}`;
  const current = useRef(identity);
  current.current = identity;
  useEffect(() => {
    setOpen(false);
    setReport(null);
    setNotice("");
    setError("");
    setLoading(false);
  }, [identity]);
  // Read-only viewers (shared conversation links) cannot read the report, so
  // they only see the fixed failure summary without detail actions.
  if (!sessionId || !generationId || (readOnly && compact)) return null;
  const path = `${sessionPath(sessionId)}/generations/${encodeURIComponent(generationId)}/diagnostics`;

  async function load() {
    setLoading(true);
    setError("");
    setNotice("");
    try {
      const data = await api(path);
      if (current.current !== identity) return null;
      setReport(data);
      return data;
    } catch (e) {
      if (current.current === identity)
        setError(e.message || "读取诊断信息失败，请重试");
      return null;
    } finally {
      if (current.current === identity) setLoading(false);
    }
  }
  async function toggle() {
    setOpen(!open);
    if (!open) await load();
  }
  async function copy() {
    const data = await load();
    if (!data) return;
    try {
      await copyText(JSON.stringify(data, null, 2));
      if (current.current === identity) setNotice("已复制诊断信息");
    } catch {
      if (current.current === identity)
        setError("复制失败，请下载诊断文件或手动复制详情");
    }
  }
  const shown = failure || report?.failure;
  return (
    <section
      className={`generation-diagnostics${compact ? " compact" : " failure"}`}
      aria-label={failure ? "本轮失败诊断" : "本轮诊断信息"}
    >
      {!compact && shown ? (
        <>
          <div className="diagnostic-heading">
            <HarnessIcon name="warning" size={17} aria-hidden="true" />
            <strong>{shown.message}</strong>
            <code>{shown.code}</code>
          </div>
          {shown.suggestion ? (
            <p className="diagnostic-suggestion">{shown.suggestion}</p>
          ) : null}
          <CredentialAction text={shown.message} />
          <CLIInstallAction text={shown.message} onResult={onCLIInstall} />
        </>
      ) : null}
      {readOnly ? null : (
        <div className="diagnostic-actions">
          <button
            type="button"
            onClick={toggle}
            aria-expanded={open}
            disabled={loading}
          >
            <HarnessIcon
              name={open ? "chevron-down" : "chevron-right"}
              size={14}
              aria-hidden="true"
            />
            {compact ? "诊断信息" : "查看详情"}
          </button>
          {!compact || open ? (
            <>
              <button type="button" onClick={copy} disabled={loading}>
                <HarnessIcon name="copy" size={14} aria-hidden="true" />
                复制诊断
              </button>
              <a href={`${path}?download=1`} download>
                <HarnessIcon name="download" size={14} aria-hidden="true" />
                下载诊断
              </a>
            </>
          ) : null}
          {loading ? (
            <span role="status">读取中…</span>
          ) : notice ? (
            <span role="status">{notice}</span>
          ) : null}
        </div>
      )}
      {error ? (
        <p className="diagnostic-fetch-error" role="alert">
          {error}
        </p>
      ) : null}
      {open && report ? (
        <div className="diagnostic-details">
          {report.assessment ? (
            <div className="diagnostic-assessment">
              <strong>{report.assessment.summary}</strong>
              {report.assessment.evidence?.length ? (
                <ul className="diagnostic-list">
                  {report.assessment.evidence.map((item, i) => (
                    <li key={i}>{item}</li>
                  ))}
                </ul>
              ) : null}
              {report.assessment.unknowns?.length ? (
                <p className="diagnostic-subnote">
                  边界说明：{report.assessment.unknowns.join(" ")}
                </p>
              ) : null}
              {report.assessment.nextSteps?.length ? (
                <p className="diagnostic-subnote">
                  排查建议：{report.assessment.nextSteps.join(" ")}
                </p>
              ) : null}
            </div>
          ) : null}
          <dl className="diagnostic-meta">
            <dt>诊断编号</dt>
            <dd>
              <code>{report.generationId}</code>
            </dd>
            <dt>状态</dt>
            <dd>{statuses[report.status] || report.status}</dd>
            <dt>开始时间</dt>
            <dd>{time(report.startedAt)}</dd>
            {report.termination ? (
              <>
                <dt>结束原因</dt>
                <dd>
                  <code>{report.termination.code}</code> ·{" "}
                  {report.termination.message}
                </dd>
              </>
            ) : null}
            {shown?.phase ? (
              <>
                <dt>失败阶段</dt>
                <dd>{phases[shown.phase] || shown.phase}</dd>
              </>
            ) : null}
            {report.environment ? (
              <>
                <dt>运行实例</dt>
                <dd>
                  <code>{report.environment.instanceId}</code> ·{" "}
                  {report.environment.platformVersion}
                  {report.environment.revision
                    ? ` (${report.environment.revision.slice(0, 8)})`
                    : ""}{" "}
                  · {report.environment.os}/{report.environment.arch}
                </dd>
              </>
            ) : null}
          </dl>
          {(report.attempts || []).map((attempt, index) => {
            const proc = attempt.process;
            const lastOut = proc?.lastStdoutAt || proc?.lastStderrAt;
            return (
              <div className="diagnostic-attempt" key={attempt.id}>
                <div className="diagnostic-attempt-heading">
                  <strong>
                    调用 {attempt.id?.split("/").at(-1) || index + 1} ·{" "}
                    {attempt.engine}
                    {attempt.cliVersion ? ` v${attempt.cliVersion}` : ""}
                  </strong>
                  <span>{attempt.model}</span>
                  <span>{statuses[attempt.status] || attempt.status}</span>
                  {attempt.exitCode != null ? (
                    <code>退出码 {attempt.exitCode}</code>
                  ) : null}
                </div>
                <p className="diagnostic-attempt-meta">
                  {attempt.resume ? "恢复会话" : "新会话"} ·{" "}
                  {phases[attempt.phase] || attempt.phase} ·{" "}
                  {time(attempt.startedAt)}
                  {attempt.endpoint ? ` · ${attempt.endpoint}` : ""}
                  {attempt.protocol ? ` (${attempt.protocol})` : ""}
                </p>
                {proc ? (
                  <p className="diagnostic-attempt-meta">
                    进程：{processStates[proc.state] || proc.state}
                    {proc.pid ? ` · PID ${proc.pid}` : ""}
                    {proc.signal ? ` · 信号 ${proc.signal}` : ""}
                    {` · 输出 ${proc.stdoutLines || 0}/${proc.stderrLines || 0} 行`}
                    {lastOut ? ` · 最近输出 ${time(lastOut)}` : ""}
                  </p>
                ) : null}
                {attempt.firstError && report.dropped?.protocolEvents > 0 ? (
                  <div className="diagnostic-event">
                    <div>
                      <strong>首次异常</strong>
                      <code>{attempt.firstError.kind}</code>
                      {attempt.firstError.statusCode ? (
                        <span>HTTP {attempt.firstError.statusCode}</span>
                      ) : null}
                    </div>
                    {attempt.firstError.message ? (
                      <pre>{attempt.firstError.message}</pre>
                    ) : null}
                  </div>
                ) : null}
                {attempt.error ? (
                  <pre className="diagnostic-excerpt">{attempt.error}</pre>
                ) : null}
                {(attempt.events || []).map((event, i) => (
                  <div className="diagnostic-event" key={i}>
                    <div>
                      <code>{event.kind}</code>
                      {event.statusCode ? (
                        <span>HTTP {event.statusCode}</span>
                      ) : null}
                      {event.code ? <code>{event.code}</code> : null}
                      {event.retryable != null ? (
                        <span>{event.retryable ? "可重试" : "不可重试"}</span>
                      ) : null}
                    </div>
                    {event.message ? <pre>{event.message}</pre> : null}
                    {event.requestId ? (
                      <p>
                        Request ID <code>{event.requestId}</code>
                      </p>
                    ) : null}
                    {event.traceId ? (
                      <p>
                        Trace ID <code>{event.traceId}</code>
                      </p>
                    ) : null}
                  </div>
                ))}
                {attempt.firstStderr?.length &&
                report.dropped?.stderrLines > 0 ? (
                  <details className="diagnostic-stderr">
                    <summary>
                      CLI 起始输出 · 前 {attempt.firstStderr.length} 行
                    </summary>
                    <pre>{attempt.firstStderr.join("\n")}</pre>
                  </details>
                ) : null}
                {attempt.stderr?.length ? (
                  <details className="diagnostic-stderr">
                    <summary>
                      CLI 错误输出 · 最近 {attempt.stderr.length} 行
                    </summary>
                    <pre>{attempt.stderr.join("\n")}</pre>
                  </details>
                ) : null}
              </div>
            );
          })}
          {report.tools?.length ? (
            <details
              className="diagnostic-stderr"
              open={report.tools.some((t) => t.status !== "completed")}
            >
              <summary>工具与网关调用 · {report.tools.length} 次</summary>
              <div className="diagnostic-tools">
                {report.tools.map((tool, i) => (
                  <div className="diagnostic-event" key={`${tool.id}-${i}`}>
                    <div>
                      <code>{tool.name}</code>
                      <span>{toolSources[tool.source] || tool.source}</span>
                      <span>{statuses[tool.status] || tool.status}</span>
                      {tool.exitCode != null ? (
                        <code>退出码 {tool.exitCode}</code>
                      ) : null}
                    </div>
                    {tool.error ? <pre>{tool.error}</pre> : null}
                    {tool.requestId ? (
                      <p>
                        Request ID <code>{tool.requestId}</code>
                      </p>
                    ) : null}
                  </div>
                ))}
              </div>
            </details>
          ) : null}
          {report.timeline?.length ? (
            <details className="diagnostic-stderr">
              <summary>执行时间线 · {report.timeline.length} 条记录</summary>
              <pre>
                {report.timeline
                  .map(
                    (e) =>
                      `[${time(e.at)}] (${e.source || "cli"}) ${e.kind}${e.tool ? ` ${e.tool}` : ""}${e.code ? ` [${e.code}]` : ""}${e.message ? ` — ${e.message}` : ""}`,
                  )
                  .join("\n")}
              </pre>
            </details>
          ) : null}
          {report.error &&
          !report.attempts?.some((attempt) => attempt.error) ? (
            <pre className="diagnostic-excerpt">{report.error}</pre>
          ) : null}
          {!report.attempts?.length && !report.error ? (
            <p className="diagnostic-note">尚未记录 CLI 调用详情。</p>
          ) : null}
          <p className="diagnostic-note">
            已脱敏，包含有限的执行现场与错误片段。Request / Trace ID 仅在 CLI
            或上游实际返回时显示。
            {report.truncated ? "部分记录已按大小限制截断。" : ""}
          </p>
          {report.status === "running" ? (
            <button
              type="button"
              className="diagnostic-refresh"
              onClick={load}
              disabled={loading}
            >
              刷新诊断
            </button>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
