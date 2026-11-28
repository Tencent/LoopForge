import React, {
  createContext,
  useContext,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";
import Markdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import { createPortal } from "react-dom";
import DOMPurify from "dompurify";
import HarnessIcon from "./HarnessIcon";
import KnowledgeSources from "./KnowledgeSources";
import SlidingTabs from "./SlidingTabs";
import { attachmentURL, attachmentThumbnailURL, visibleText } from "./api";
import { definitionTarget } from "./platform.js";
import {
  AgentBrowserLinks,
  agentBrowserTarget,
  remarkWorkspaceLinks,
} from "./agent-browser-links.js";
import {
  buildKnowledgeSummary,
  collapseAdjacentDuplicateCitations,
  extractCitedIdsInOrder,
  filterKnowledgeReadsForAttempt,
  formatTextForCopy,
  knowledgeCitePrefix,
  maskIncompleteStreamingCitation,
  remarkKnowledgeCitations,
} from "./knowledge-citations.js";
import "./agent-browser.css";
import {
  imageRetryDelay,
  initialImageStatus,
  retryImageURL,
  shouldAutoRetryImage,
} from "./image-load.js";
import {
  presentationPages,
  presentationTitle,
  exportPresentation,
} from "./presentation.js";
import "./artifact-preview.css";

const Check = (props) => <HarnessIcon name="check" {...props} />;
const Copy = (props) => <HarnessIcon name="copy" {...props} />;
const Download = (props) => <HarnessIcon name="download" {...props} />;
const ImageOff = (props) => <HarnessIcon name="image-off" {...props} />;
const Refresh = (props) => <HarnessIcon name="refresh" {...props} />;
const Spinner = (props) => <HarnessIcon name="spinner" {...props} />;
const FileText = (props) => <HarnessIcon name="doc" {...props} />;
const X = (props) => <HarnessIcon name="close" {...props} />;

export function safeSVG(source) {
  return DOMPurify.sanitize(source, {
    USE_PROFILES: { svg: true, svgFilters: true },
    FORBID_TAGS: ["script", "foreignObject", "animate", "set"],
    FORBID_ATTR: ["href", "xlink:href"],
  });
}
export function download(name, content, type = "text/plain") {
  const url = URL.createObjectURL(new Blob([content], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
export function CopyButton({ value, label = "复制", className = "" }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      if (navigator.clipboard?.writeText)
        await navigator.clipboard.writeText(value);
      else {
        const text = document.createElement("textarea");
        text.value = value;
        document.body.append(text);
        text.select();
        document.execCommand("copy");
        text.remove();
      }
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }
  return (
    <button
      className={`icon-button ${className}`}
      type="button"
      aria-label={copied ? "已复制" : label}
      title={copied ? "已复制" : label}
      onClick={copy}
    >
      {copied ? <Check size={15} /> : <Copy size={15} />}
    </button>
  );
}
export function Diagram({ source }) {
  const [result, setResult] = useState({ svg: "", error: "" });
  const id = useId().replace(/[^a-z0-9]/gi, "");
  useEffect(() => {
    let current = true;
    (async () => {
      try {
        const { default: mermaid } = await import("mermaid");
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: "strict",
          htmlLabels: false,
          flowchart: { htmlLabels: false },
          theme:
            document.documentElement.dataset.theme === "dark"
              ? "dark"
              : "neutral",
          fontFamily: "system-ui, sans-serif",
        });
        const rendered = await mermaid.render(`diagram-${id}`, source);
        if (current)
          setResult({
            svg: DOMPurify.sanitize(rendered.svg, {
              USE_PROFILES: { svg: true },
              FORBID_TAGS: ["script", "foreignObject"],
            }),
            error: "",
          });
      } catch {
        if (current)
          setResult({
            svg: "",
            error: "图表语法需要调整，原始定义保留在下方。",
          });
      }
    })();
    return () => {
      current = false;
    };
  }, [source, id]);
  return result.svg ? (
    <div className="diagram" dangerouslySetInnerHTML={{ __html: result.svg }} />
  ) : (
    <div className="diagram-loading">
      {result.error || "正在绘制图表…"}
      {result.error ? <pre>{source}</pre> : null}
    </div>
  );
}
export function Slides({ source }) {
  const [page, setPage] = useState(0);
  const [exporting, setExporting] = useState(false),
    [error, setError] = useState("");
  const slides = presentationPages(source);
  const currentPage = Math.min(page, Math.max(0, slides.length - 1));
  return (
    <div className="slides">
      <div
        className={`slide ${currentPage === 0 ? "slide-cover" : ""}`}
        aria-label={`幻灯片 ${currentPage + 1}`}
      >
        <div className="slide-eyebrow">{presentationTitle(source)}</div>
        <Markdown remarkPlugins={[remarkGfm]}>{slides[currentPage]}</Markdown>
        <small className="slide-page">
          {currentPage + 1} / {slides.length}
        </small>
      </div>
      <div className="slide-controls">
        <button
          disabled={!currentPage}
          onClick={() => setPage(currentPage - 1)}
        >
          上一页
        </button>
        <span>
          {currentPage + 1} / {slides.length}
        </span>
        <button
          disabled={currentPage === slides.length - 1}
          onClick={() => setPage(currentPage + 1)}
        >
          下一页
        </button>
        <button
          className="slide-export"
          disabled={exporting}
          onClick={async () => {
            setExporting(true);
            setError("");
            try {
              await exportPresentation(source);
            } catch {
              setError("PPT 导出失败，请重试；内容已保留。");
            } finally {
              setExporting(false);
            }
          }}
        >
          <Download size={14} />
          {exporting ? "正在导出…" : "下载 PowerPoint"}
        </button>
      </div>
      {error ? (
        <p role="alert" className="error">
          {error}
        </p>
      ) : null}
    </div>
  );
}
export function CodeBlock({ language, source }) {
  const [preview, setPreview] = useState(
    ["svg", "mermaid", "html", "slides", "ppt"].includes(language),
  );
  const visual = ["svg", "mermaid", "html", "slides", "ppt"].includes(language);
  return (
    <div className="code-block">
      <div className="code-toolbar">
        <span>{language || "文本"}</span>
        <div>
          {visual ? (
            <button onClick={() => setPreview(!preview)}>
              {preview ? "查看源码" : "预览"}
            </button>
          ) : null}
          <CopyButton value={source} label="复制代码" />
        </div>
      </div>
      {preview && language === "svg" ? (
        <div
          className="svg-preview"
          dangerouslySetInnerHTML={{ __html: safeSVG(source) }}
        />
      ) : preview && language === "mermaid" ? (
        <Diagram source={source} />
      ) : preview && language === "html" ? (
        <iframe
          title="HTML 产物预览"
          className="html-preview"
          sandbox="allow-scripts"
          key={source}
          src="/chatbot/preview.html"
          onLoad={(event) =>
            event.currentTarget.contentWindow?.postMessage(
              { type: "agentflow-html-preview", source },
              "*",
            )
          }
        />
      ) : preview && ["slides", "ppt"].includes(language) ? (
        <Slides source={source} />
      ) : (
        <pre>
          <code>{source}</code>
        </pre>
      )}
    </div>
  );
}
const MarkdownAssets = createContext({
  assets: [],
  projectId: "",
  knowledgeReads: [],
  knowledgeSummary: null,
  citationsEnabled: false,
  streamingCiteOrder: null,
  onCitationClick: null,
});
// Set inside a link so inline code there does not become a nested control.
const InsideLink = createContext(false);
// file:///workspace/... survives URL sanitizing only to be opened in the Agent
// browser; it is never rendered as an href.
const markdownURL = (url) =>
  /^file:\/\/\/workspace\//i.test(url) ||
  String(url || "").startsWith(knowledgeCitePrefix)
    ? url
    : defaultUrlTransform(url);
function AgentBrowserLink({ target, open, children }) {
  return (
    <InsideLink.Provider value>
      <button
        type="button"
        className="agent-browser-link"
        title={`在 Agent 浏览器中打开 ${target}`}
        onClick={() => open(target)}
      >
        {children}
        <HarnessIcon
          name="stage-browser"
          size={12}
          className="agent-browser-link-icon"
        />
      </button>
    </InsideLink.Provider>
  );
}
const markdownComponents = {
  pre: ({ children }) => <>{children}</>,
  code: ({ className, children }) => {
    const { assets, projectId } = useContext(MarkdownAssets);
    const openInBrowser = useContext(AgentBrowserLinks);
    const insideLink = useContext(InsideLink);
    const source = String(children).replace(/\n$/, "");
    const language = /language-([^\s]+)/.exec(className || "")?.[1];
    const browserTarget =
      openInBrowser && !insideLink && !language && !source.includes("\n")
        ? agentBrowserTarget(source)
        : "";
    if (browserTarget)
      return (
        <AgentBrowserLink target={browserTarget} open={openInBrowser}>
          <code>{source}</code>
        </AgentBrowserLink>
      );
    const target =
      !language && !source.includes("\n")
        ? assets
            .map((a) => definitionTarget(a, projectId))
            .find((a) => a?.id === source)
        : null;
    if (target)
      return (
        <a
          className="definition-code-link"
          href={target.href}
          target="_blank"
          rel="noopener noreferrer"
          title={target.label}
        >
          <code>{children}</code>
        </a>
      );
    return language || source.includes("\n") ? (
      <CodeBlock language={language || ""} source={source} />
    ) : (
      <code>{children}</code>
    );
  },
  a: ({ href, children }) => {
    const {
      knowledgeReads,
      knowledgeSummary,
      citationsEnabled,
      streamingCiteOrder,
      onCitationClick,
    } = useContext(MarkdownAssets);
    const openInBrowser = useContext(AgentBrowserLinks);
    if (citationsEnabled && String(href || "").startsWith(knowledgeCitePrefix)) {
      const citeId = href.slice(knowledgeCitePrefix.length);
      const hasReads = Array.isArray(knowledgeReads) && knowledgeReads.length > 0;
      if (hasReads) {
        const entry = knowledgeSummary?.byCiteId?.get(citeId);
        if (!entry) {
          return (
            <sup
              className="knowledge-cite-sup is-unresolved"
              title="未匹配到有效知识库证据"
            >
              ?
            </sup>
          );
        }
        const tooltip = [
          entry.doc.sourceName || entry.doc.sourceId,
          entry.doc.label,
          entry.locator,
        ]
          .filter(Boolean)
          .join(" · ");
        return (
          <sup className="knowledge-cite-wrap">
            <button
              type="button"
              className="knowledge-cite-sup"
              data-cite-index={entry.citeIndex}
              title={tooltip || entry.doc.label}
              aria-label={`引用 [${entry.citeIndex}] ${entry.doc.label}`}
              onClick={() => onCitationClick?.(entry.citeIndex)}
            >
              {entry.citeIndex}
            </button>
          </sup>
        );
      }
      const streamNum = streamingCiteOrder?.get(citeId) || "?";
      return (
        <sup
          className="knowledge-cite-sup is-streaming"
          title="知识库引用（生成完成后显示文档详情）"
        >
          {streamNum}
        </sup>
      );
    }
    const target = openInBrowser ? agentBrowserTarget(href) : "";
    if (target)
      return (
        <AgentBrowserLink target={target} open={openInBrowser}>
          {children}
        </AgentBrowserLink>
      );
    if (/^file:/i.test(href || "")) return <span>{children}</span>;
    return (
      <InsideLink.Provider value>
        <a href={href} target="_blank" rel="noopener noreferrer">
          {children}
        </a>
      </InsideLink.Provider>
    );
  },
  img: ({ src, alt }) =>
    src?.startsWith("/api/assistant/attachments/") ? (
      <ZoomableImage src={src} alt={alt || "图片"} />
    ) : (
      <span className="unavailable-image">
        {alt || "图片"} · 请在产物中查看
      </span>
    ),
  table: ({ children }) => (
    <div className="table-scroll">
      <table>{children}</table>
    </div>
  ),
};
const emptyAssets = [];
export function RichContent({
  message,
  assets = emptyAssets,
  projectId = "",
  knowledgeReads = [],
  knowledgeSummary: precomputedSummary = null,
  citationsEnabled = false,
  streaming = false,
  onCitationClick = null,
}) {
  const rawText = typeof message === "string" ? message : visibleText(message);
  const hasReads = Array.isArray(knowledgeReads) && knowledgeReads.length > 0;
  const enableCites = hasReads || citationsEnabled;
  const text = useMemo(
    () =>
      streaming && enableCites
        ? maskIncompleteStreamingCitation(rawText)
        : rawText,
    [rawText, streaming, enableCites],
  );
  const knowledgeSummary = useMemo(() => {
    if (!hasReads) return null;
    if (precomputedSummary) return precomputedSummary;
    return buildKnowledgeSummary(knowledgeReads, [text], {
      live: streaming,
    });
  }, [hasReads, precomputedSummary, knowledgeReads, text, streaming]);
  const displayText = useMemo(
    () =>
      hasReads && knowledgeSummary
        ? collapseAdjacentDuplicateCitations(text, knowledgeSummary)
        : text,
    [hasReads, knowledgeSummary, text],
  );
  const streamingCiteOrder = useMemo(() => {
    if (!enableCites || hasReads) return null;
    const ids = extractCitedIdsInOrder([text]);
    const map = new Map();
    ids.forEach((id, idx) => map.set(id, idx + 1));
    return map;
  }, [enableCites, hasReads, text]);
  const plugins = useMemo(() => {
    if (!enableCites) return [remarkGfm, remarkWorkspaceLinks];
    const resolveDocKey = (citeId) => {
      const entry = knowledgeSummary?.byCiteId?.get(citeId);
      if (entry?.citeIndex) return `cite:${entry.citeIndex}`;
      return `id:${citeId}`;
    };
    return [
      remarkGfm,
      remarkWorkspaceLinks,
      [remarkKnowledgeCitations, { resolveDocKey }],
    ];
  }, [enableCites, knowledgeSummary]);
  const contextValue = useMemo(
    () => ({
      assets,
      projectId,
      knowledgeReads,
      knowledgeSummary,
      citationsEnabled: enableCites,
      streamingCiteOrder,
      onCitationClick,
    }),
    [
      assets,
      projectId,
      knowledgeReads,
      knowledgeSummary,
      enableCites,
      streamingCiteOrder,
      onCitationClick,
    ],
  );
  const markdown = useMemo(
    () => (
      <Markdown
        remarkPlugins={plugins}
        components={markdownComponents}
        urlTransform={markdownURL}
      >
        {displayText}
      </Markdown>
    ),
    [plugins, displayText],
  );
  return (
    <MarkdownAssets.Provider value={contextValue}>
      <div className="markdown">{markdown}</div>
    </MarkdownAssets.Provider>
  );
}

// Full-window viewer shared by chat images, artifact previews and LOOP node
// files. Double-click an image to open; Esc closes.
export function ImageLightbox({ src, alt, onClose }) {
  useEffect(() => {
    const onKey = (e) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return createPortal(
    <div
      className="image-lightbox"
      role="dialog"
      aria-modal="true"
      aria-label={`查看大图 ${alt || ""}`}
      onClick={onClose}
    >
      <img src={src} alt={alt} onClick={(e) => e.stopPropagation()} />
      <button
        type="button"
        className="icon-button image-lightbox-close"
        aria-label="关闭大图"
        onClick={onClose}
      >
        <X size={18} />
      </button>
    </div>,
    document.body,
  );
}

export function ZoomableImage({ src, fullSrc = src, alt, className }) {
  const [zoom, setZoom] = useState(false);
  return (
    <>
      <img
        className={className}
        src={src}
        alt={alt}
        title="双击查看大图"
        onDoubleClick={() => setZoom(true)}
      />
      {zoom ? (
        <ImageLightbox src={fullSrc} alt={alt} onClose={() => setZoom(false)} />
      ) : null}
    </>
  );
}

export function Attachment({ attachment, sessionId, onOpen, localPreviewURL }) {
  const image = attachment.mimeType?.startsWith("image/");
  const [zoom, setZoom] = useState(false);
  const [status, setStatus] = useState("loading");
  const [attempt, setAttempt] = useState(0);
  const img = useRef(null);
  const retriedAt = useRef(-Infinity);
  const url = attachmentURL(attachment, sessionId);
  const [failedLocalURL, setFailedLocalURL] = useState(null);
  const local = localPreviewURL && localPreviewURL !== failedLocalURL ? localPreviewURL : null;
  const src = local || retryImageURL(attachmentThumbnailURL(url, attachment.mimeType), attempt);
  // While a silent reload is pending the failure notice stays hidden, so the
  // first fetch of a just-sent message never flashes an error.
  const retrying = image && shouldAutoRetryImage(status, attempt);
  const shown = retrying ? "loading" : status;
  const failed = image && shown === "error";
  useEffect(() => {
    if (image) setStatus(initialImageStatus(img.current));
  }, [image, src]);
  // The just-sent message is on screen before its transcript (and the
  // attachment entry inside it) reaches the server. Reload quietly first so the
  // user does not see a failure they would fix by refreshing the page.
  useEffect(() => {
    if (!image || !shouldAutoRetryImage(status, attempt)) return undefined;
    const timer = setTimeout(() => {
      setStatus("loading");
      setAttempt((n) => n + 1);
    }, imageRetryDelay(attempt));
    return () => clearTimeout(timer);
  }, [image, status, attempt]);
  const retry = (e) => {
    e.preventDefault();
    e.stopPropagation();
    retriedAt.current = e.timeStamp;
    setStatus("loading");
    setAttempt((n) => n + 1);
  };
  // The retry control unmounts on its first click, so the rest of a double-click lands on the card.
  const continuesRetry = (e) =>
    e.detail > 1 && e.timeStamp - retriedAt.current < 1000;
  return (
    <>
      <button
        type="button"
        className={image ? "message-image" : "attachment-chip"}
        // The card shows no caption, so the hover title names the file.
        title={
          image
            ? `${attachment.name}\n${failed ? "单击在产物栏打开" : "单击查看大图"}`
            : undefined
        }
        onClick={(e) => {
          if (continuesRetry(e)) return;
          if (image && !failed) {
            setZoom(true);
            return;
          }
          onOpen({
            ...attachment,
            url,
            kind: "attachment",
            source: "会话附件",
          });
        }}
      >
        {image ? (
          <span
            className="message-image-frame"
            data-status={shown}
            aria-busy={shown === "loading" || undefined}
          >
            <img
              key={attempt}
              ref={img}
              src={src}
              alt={attachment.name}
              onLoad={() => setStatus("loaded")}
              onError={() => {
                if (local) {
                  setFailedLocalURL(local);
                  setStatus("loading");
                } else {
                  setStatus("error");
                }
              }}
            />
            {shown === "loading" ? (
              <span className="message-image-placeholder">
                <Spinner size={22} motion="busy" />
                <span className="message-image-hidden-label">图片加载中</span>
              </span>
            ) : null}
            {failed ? (
              <span className="message-image-placeholder">
                <ImageOff size={22} />
                <span>图片加载失败</span>
                <span
                  className="message-image-retry"
                  role="button"
                  tabIndex={0}
                  aria-label="重试加载图片"
                  onClick={retry}
                  onDoubleClick={(e) => e.stopPropagation()}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") retry(e);
                  }}
                >
                  <Refresh size={14} />
                  <span>重试</span>
                </span>
              </span>
            ) : null}
          </span>
        ) : (
          <>
            <FileText size={18} />
            <span>{attachment.name}</span>
          </>
        )}
      </button>
      {zoom ? (
        <ImageLightbox
          src={local || url}
          alt={attachment.name}
          onClose={() => setZoom(false)}
        />
      ) : null}
    </>
  );
}
// Larger files are offered for download instead of being read into the page.
const PREVIEW_MAX_BYTES = 5 * 1024 * 1024;
// Deliverables without a declared type are stored as Markdown, so the body
// decides: NUL bytes or a mostly undecodable start mean binary content.
function looksBinary(content) {
  if (content.includes("\u0000")) return true;
  const head = content.slice(0, 65536);
  return (head.match(/\uFFFD/g) || []).length > head.length * 0.1;
}
export function ArtifactPreview({ artifact, knowledgeReads = [], attempt }) {
  const [loaded, setLoaded] = useState(null);
  const [sourceView, setSourceView] = useState(false);
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
  };
  const { id, url, localDataUrl } = artifact || {};
  const current =
    loaded?.id === id &&
    loaded?.url === url &&
    loaded?.localDataUrl === localDataUrl
      ? loaded
      : null;
  const error = current?.error;
  const mode = sourceView && current ? "source" : "preview";
  const readable =
    !artifact?.mimeType ||
    /^(text\/|application\/(json|javascript|xml)|image\/svg\+xml)/i.test(
      artifact.mimeType,
    );
  const oversized = Boolean(url) && (artifact?.size || 0) > PREVIEW_MAX_BYTES;
  // An attached HTML file (e.g. a demo from the workspace) previews in the same
  // script-only sandbox as an html code block.
  const kind =
    artifact?.kind === "attachment" &&
    /^text\/html\b/i.test(artifact.mimeType || "")
      ? "html"
      : artifact?.kind;
  useEffect(() => {
    let active = true;
    setSourceView(false);
    const finish = (text, error) => {
      if (active) setLoaded({ id, url, localDataUrl, text, error });
    };
    if (
      readable &&
      id?.startsWith("local-attachment-") &&
      /^data:[^,]*;base64,/.test(localDataUrl || "")
    ) {
      try {
        const encoded = localDataUrl.slice(localDataUrl.indexOf(",") + 1);
        const bytes = Uint8Array.from(atob(encoded), (character) =>
          character.charCodeAt(0),
        );
        finish(new TextDecoder().decode(bytes));
      } catch {
        finish("", "文件读取失败");
      }
    } else if (url && readable && !oversized) {
      fetch(url)
        .then((r) => {
          if (!r.ok) throw Error("文件读取失败");
          return r.text();
        })
        .then((value) => finish(value))
        .catch((e) => finish("", e.message));
    } else {
      // Inline artifacts already carry their body in the message.
      finish("");
    }
    return () => {
      active = false;
    };
  }, [id, url, localDataUrl, readable, oversized]);
  if (!artifact) return null;
  const source =
    (url ? current?.text : (artifact.content ?? current?.text)) ?? "";
  const binary = Boolean(url) && looksBinary(current?.text || "");
  const hasSource =
    typeof source === "string" && source.length > 0 && !error && !binary;
  const isMarkdown =
    artifact.mimeType?.startsWith("text/markdown") ||
    ["markdown", "md"].includes(artifact.language?.toLowerCase()) ||
    /\.(mdx?|markdown)$/i.test(artifact.name || "");
  const isDelimited =
    /\.(csv|tsv)$/i.test(artifact.name || "") ||
    /^(text\/csv|text\/tab-separated-values)/i.test(artifact.mimeType || "");
  const isJSON =
    /\.json$/i.test(artifact.name || "") ||
    /^application\/json/i.test(artifact.mimeType || "");
  const canPreview =
    hasSource &&
    (["svg", "mermaid", "html", "slides", "ppt"].includes(kind) ||
      isMarkdown ||
      isDelimited ||
      isJSON);
  const filteredReads = useMemo(
    () =>
      filterKnowledgeReadsForAttempt(
        knowledgeReads,
        attempt ?? artifact?.attempt,
      ),
    [knowledgeReads, attempt, artifact?.attempt],
  );
  const artSummary = useMemo(() => {
    if (!filteredReads.length || !isMarkdown || !hasSource) return null;
    return buildKnowledgeSummary(filteredReads, [source], {
      scopeToTexts: true,
    });
  }, [filteredReads, isMarkdown, hasSource, source]);
  const copyValue =
    mode === "preview" && isMarkdown && filteredReads.length > 0
      ? formatTextForCopy(source, filteredReads, artSummary)
      : source;
  return (
    <article className="artifact-preview">
      <header className="artifact-preview-header">
        <div className="artifact-preview-file-row">
          <FileText size={17} />
          <div className="artifact-preview-file-identity">
            <p className="artifact-preview-file-name" title={artifact.name}>
              {artifact.name}
            </p>
            {artifact.source ? (
              <p className="artifact-preview-file-meta">{artifact.source}</p>
            ) : null}
          </div>
        </div>
        <div className="artifact-preview-toolbar" aria-label="产物工具栏">
          {canPreview ? (
            <SlidingTabs
              className="artifact-preview-view-options"
              aria-label="产物显示方式"
            >
              <button
                type="button"
                role="tab"
                className={mode === "preview" ? "is-selected" : ""}
                aria-selected={mode === "preview"}
                onClick={() => setSourceView(false)}
              >
                预览
              </button>
              <button
                type="button"
                role="tab"
                className={mode === "source" ? "is-selected" : ""}
                aria-selected={mode === "source"}
                onClick={() => setSourceView(true)}
              >
                源码
              </button>
            </SlidingTabs>
          ) : null}
          <div
            className="artifact-preview-actions"
            role="group"
            aria-label="产物操作"
          >
            {hasSource ? (
              <CopyButton
                className="artifact-preview-icon-action"
                value={copyValue}
                label="复制正文"
              />
            ) : null}
            {artifact.url ? (
              <a
                className="icon-button artifact-preview-icon-action"
                aria-label="下载文件"
                title="下载文件"
                href={artifact.url}
                download={artifact.name}
              >
                <Download size={15} />
              </a>
            ) : (
              <button
                type="button"
                className="icon-button artifact-preview-icon-action"
                aria-label="下载文件"
                title="下载文件"
                onClick={() =>
                  ["slides", "ppt"].includes(artifact.kind)
                    ? exportPresentation(source)
                    : download(
                        artifact.name,
                        artifact.kind === "svg" ? safeSVG(source) : source,
                        artifact.mimeType,
                      )
                }
              >
                <Download size={15} />
              </button>
            )}
          </div>
        </div>
      </header>
      <div className="artifact-preview-stage">
        {error ? (
          <p className="error">{error}</p>
        ) : mode === "source" && canPreview ? (
          <pre className="artifact-source">
            <code>{source}</code>
          </pre>
        ) : artifact.mimeType?.startsWith("image/") && artifact.url ? (
          <ZoomableImage
            className="artifact-image"
            src={attachmentThumbnailURL(artifact.url, artifact.mimeType)}
            fullSrc={artifact.url}
            alt={artifact.name}
          />
        ) : oversized ? (
          <p className="muted">文件较大，不在页面内预览，可通过右上角下载查看。</p>
        ) : (url || localDataUrl) && readable && !current ? (
          <p className="muted" role="status">
            正在读取文件…
          </p>
        ) : binary ? (
          <p className="muted">此类型文件可通过右上角下载查看。</p>
        ) : !hasSource && readable ? (
          <p className="muted">文件为空</p>
        ) : artifact.kind === "svg" ? (
          <div
            className="svg-preview"
            dangerouslySetInnerHTML={{ __html: safeSVG(source) }}
          />
        ) : artifact.kind === "mermaid" ? (
          <Diagram source={source} />
        ) : kind === "html" ? (
          <iframe
            title="HTML 产物预览"
            className="html-preview"
            sandbox="allow-scripts"
            key={source}
            src="/chatbot/preview.html"
            onLoad={(event) =>
              event.currentTarget.contentWindow?.postMessage(
                { type: "agentflow-html-preview", source },
                "*",
              )
            }
          />
        ) : ["slides", "ppt"].includes(artifact.kind) ? (
          <Slides source={source} />
        ) : artifact.url && !readable ? (
          <p className="muted">此类型文件可通过右上角下载查看。</p>
        ) : isMarkdown ? (
          <>
            <RichContent
              message={source}
              knowledgeReads={filteredReads}
              knowledgeSummary={artSummary}
              onCitationClick={handleCitationClick}
            />
            {filteredReads.length > 0 ? (
              <KnowledgeSources
                knowledgeReads={filteredReads}
                summary={artSummary}
                highlightedIndex={highlightedCite}
                containerRef={sourcesRef}
                title="产物引用来源"
                usedLabel="实际用于产出"
              />
            ) : null}
          </>
        ) : hasSource && isDelimited ? (
          <DelimitedArtifact
            source={source}
            label={artifact.name}
            separator={
              /\.tsv$/i.test(artifact.name || "") ||
              artifact.mimeType?.startsWith("text/tab-separated-values")
                ? "\t"
                : ","
            }
          />
        ) : hasSource && isJSON ? (
          <pre className="artifact-source">
            <code>{formattedArtifactJSON(source)}</code>
          </pre>
        ) : (
          <pre className="artifact-source">{source}</pre>
        )}
      </div>
    </article>
  );
}

function formattedArtifactJSON(source) {
  try {
    return JSON.stringify(JSON.parse(source), null, 2);
  } catch {
    return source;
  }
}

function DelimitedArtifact({ source, label, separator }) {
  const rowLimit = 101;
  const columnLimit = 30;
  const rows = [];
  let row = [],
    field = "",
    quoted = false,
    truncated = false;
  const pushField = () => {
    if (row.length < columnLimit) row.push(field);
    else truncated = true;
    field = "";
  };
  let end = source.length;
  while (end && (source[end - 1] === "\n" || source[end - 1] === "\r")) end--;
  for (let i = source.charCodeAt(0) === 0xfeff ? 1 : 0; i < end; i++) {
    const char = source[i];
    if (char === '"') {
      if (quoted && source[i + 1] === '"') {
        field += '"';
        i++;
      } else {
        quoted = !quoted;
      }
    } else if (!quoted && char === separator) {
      pushField();
    } else if (!quoted && (char === "\n" || char === "\r")) {
      pushField();
      rows.push(row);
      row = [];
      if (char === "\r" && source[i + 1] === "\n") i++;
      if (rows.length >= rowLimit) {
        truncated ||= i + 1 < end;
        break;
      }
    } else {
      field += char;
    }
  }
  if (row.length || field) {
    pushField();
    rows.push(row);
  }
  if (!rows.length) return <p className="muted">表格为空</p>;
  const width = Math.max(...rows.map((cells) => cells.length));
  const cells = (values) =>
    Array.from({ length: width }, (_, index) => values[index] ?? "");
  return (
    <div className="artifact-table-wrap">
      <table aria-label={label}>
        <thead>
          <tr>
            {cells(rows[0]).map((name, index) => (
              <th key={index} scope="col">
                {name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.slice(1).map((values, index) => (
            <tr key={index}>
              {cells(values).map((value, column) => (
                <td key={column}>{value}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {truncated ? (
        <p className="muted">
          预览最多显示 100 行、30 列，可下载查看完整数据。
        </p>
      ) : null}
    </div>
  );
}
