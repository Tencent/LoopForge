import React, { useMemo, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import { buildKnowledgeSummary } from "./knowledge-citations.js";

const DEFAULT_REF_LIMIT = 6;

function formatDocMeta(doc, useCitedLocators = false) {
  const source = doc.sourceName || doc.sourceId;
  const locators = useCitedLocators
    ? doc.citedLocators?.length
      ? doc.citedLocators
      : doc.locators
    : doc.locators;
  const parts = [];
  if (source && source !== doc.label) parts.push(source);
  if (locators?.length) parts.push(locators.join("、"));
  return parts.join(" · ");
}

function docStatusLabel(doc) {
  if (doc.used) return `[${doc.citeIndex}] 已用于回答`;
  if (doc.readFull) return "已读正文 · 未引用";
  return "仅检索未采用";
}

function docStatusClass(doc) {
  if (doc.used) return "is-used";
  if (doc.readFull) return "is-read";
  return "is-retrieved";
}

function emptyUsedMessage({
  allContentFailed,
  hasUnstructuredOnly,
  referencedCount,
  hasCatalogOnly,
}) {
  if (allContentFailed) return "知识库检索调用失败，未能获取可用文档";
  if (referencedCount > 0) {
    return "未显式标注具体引用文档（以下为本轮检索/读取的候选文档）";
  }
  if (hasUnstructuredOnly) {
    return "已检索到非结构化内容，未显式标注引用文档";
  }
  if (hasCatalogOnly) return "仅执行了知识库目录查询，未读取具体文档";
  return "本次检索未命中可用文档";
}

export default function KnowledgeSources({
  knowledgeReads = [],
  text = "",
  summary: precomputedSummary = null,
  highlightedIndex = null,
  containerRef = null,
  title = "知识库来源",
  usedLabel = "实际用于回答",
}) {
  const [expandedRefs, setExpandedRefs] = useState(false);
  const summary = useMemo(
    () => precomputedSummary || buildKnowledgeSummary(knowledgeReads, [text]),
    [precomputedSummary, knowledgeReads, text],
  );

  if (!Array.isArray(knowledgeReads) || knowledgeReads.length === 0) {
    return null;
  }

  const {
    usedDocs,
    referencedDocs,
    hasCatalogOnly,
    allContentFailed,
    hasUnstructuredOnly,
  } = summary;
  const visibleRefs = expandedRefs
    ? referencedDocs
    : referencedDocs.slice(0, DEFAULT_REF_LIMIT);
  const hiddenCount = Math.max(0, referencedDocs.length - DEFAULT_REF_LIMIT);

  return (
    <section
      className="knowledge-sources"
      aria-label={title}
      ref={containerRef}
    >
      <header className="knowledge-sources-head">
        <span className="knowledge-sources-title">
          <HarnessIcon name="layers" size={13} interactive={false} />
          <span>{title}</span>
        </span>
        <span className="knowledge-sources-counts">
          实际使用 {usedDocs.length} 篇 · 检索读取 {referencedDocs.length} 篇
        </span>
      </header>

      <div className="knowledge-sources-group">
        <div className="knowledge-sources-label">{usedLabel}</div>
        {usedDocs.length > 0 ? (
          <ol className="knowledge-doc-list">
            {usedDocs.map((doc) => {
              const meta = formatDocMeta(doc, true);
              return (
                <li
                  key={doc.key}
                  className={`knowledge-doc-row is-used-row ${
                    highlightedIndex === doc.citeIndex ? "is-highlighted" : ""
                  }`}
                  data-cite-index={doc.citeIndex}
                >
                  <span className="knowledge-doc-index">[{doc.citeIndex}]</span>
                  <span className="knowledge-doc-main">
                    <span className="knowledge-doc-name" title={doc.label}>
                      {doc.label}
                    </span>
                    {meta ? (
                      <span className="knowledge-doc-meta" title={meta}>
                        {meta}
                      </span>
                    ) : null}
                  </span>
                  {doc.readFull ? (
                    <span className="knowledge-doc-tag is-read">已读正文</span>
                  ) : null}
                </li>
              );
            })}
          </ol>
        ) : (
          <p className="knowledge-sources-empty">
            {emptyUsedMessage({
              allContentFailed,
              hasUnstructuredOnly,
              referencedCount: referencedDocs.length,
              hasCatalogOnly,
            })}
          </p>
        )}
      </div>

      {referencedDocs.length > 0 ? (
        <div className="knowledge-sources-group">
          <div className="knowledge-sources-label">
            本轮检索/读取文档（{referencedDocs.length}）
          </div>
          <ul className="knowledge-doc-list">
            {visibleRefs.map((doc) => {
              const meta = formatDocMeta(doc, false);
              return (
                <li key={doc.key} className="knowledge-doc-row">
                  <span className="knowledge-doc-main">
                    <span className="knowledge-doc-name" title={doc.label}>
                      {doc.label}
                    </span>
                    {meta ? (
                      <span className="knowledge-doc-meta" title={meta}>
                        {meta}
                      </span>
                    ) : null}
                  </span>
                  <span
                    className={`knowledge-doc-tag ${docStatusClass(doc)}`}
                  >
                    {docStatusLabel(doc)}
                  </span>
                </li>
              );
            })}
          </ul>
          {hiddenCount > 0 ? (
            <button
              type="button"
              className="knowledge-sources-more"
              onClick={() => setExpandedRefs((v) => !v)}
            >
              {expandedRefs ? "收起" : `展开其余 ${hiddenCount} 篇`}
            </button>
          ) : null}
        </div>
      ) : null}

      <details className="knowledge-reads knowledge-reads-trace">
        <summary>检索调用明细 · {knowledgeReads.length}</summary>
        {knowledgeReads.map((read) => {
          const citedEvs = (read.evidences || []).filter((ev) => ev.cited);
          const evText = citedEvs.length
            ? `引用 ${citedEvs.length} 条`
            : read.evidences?.length
              ? `命中 ${read.evidences.length} 条`
              : read.targets?.length
                ? `命中：${read.targets.join("、")}`
                : "";
          return (
            <p key={read.id}>
              {[
                read.serverId,
                read.query ? `${read.tool} ("${read.query}")` : read.tool,
                evText,
                read.status,
                read.cited ? "回答已引用" : "",
                read.warning,
              ]
                .filter(Boolean)
                .join(" · ")}
            </p>
          );
        })}
      </details>
    </section>
  );
}
