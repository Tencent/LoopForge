export const knowledgeCitePrefix = "#knowledge-cite:";

export const knowledgeCitePattern = /\[\[knowledge:([A-Za-z0-9_.-]+)\]\]/g;

const catalogTools = new Set([
  "list_knowledge_bases",
  "kb_list_sources",
  "list_documents",
  "kb_get_source",
]);

const readTools = new Set(["read_document", "kb_fetch"]);

export function isKnowledgeCatalogTool(tool) {
  return catalogTools.has(String(tool || "").trim());
}

export function isKnowledgeReadTool(tool) {
  return readTools.has(String(tool || "").trim());
}

export function filterKnowledgeReadsForAttempt(reads, attempt) {
  const list = Array.isArray(reads) ? reads : [];
  const att = Number(attempt || 0);
  if (att > 0 && list.some((r) => Number(r?.attempt || 0) > 0)) {
    return list.filter((r) => Number(r?.attempt || 0) === att);
  }
  return list;
}

function stripInlineCodeSpans(line) {
  if (!line.includes("`")) return line;
  let out = "";
  for (let i = 0; i < line.length; ) {
    if (line[i] !== "`") {
      out += line[i++];
      continue;
    }
    let tickLen = 0;
    while (i + tickLen < line.length && line[i + tickLen] === "`") tickLen++;
    let closeIdx = -1;
    for (let j = i + tickLen; j < line.length; ) {
      if (line[j] !== "`") {
        j++;
        continue;
      }
      let closeLen = 0;
      while (j + closeLen < line.length && line[j + closeLen] === "`") {
        closeLen++;
      }
      if (closeLen === tickLen) {
        closeIdx = j + closeLen;
        break;
      }
      j += closeLen;
    }
    if (closeIdx >= 0) {
      out += " ";
      i = closeIdx;
    } else {
      out += line.slice(i, i + tickLen);
      i += tickLen;
    }
  }
  return out;
}

export function stripMarkdownCode(text = "") {
  const raw = String(text || "");
  if (!raw.includes("`") && !raw.includes("~")) return raw;
  const lines = raw.split("\n");
  const out = [];
  let fenceChar = "";
  let fenceLen = 0;
  for (const line of lines) {
    const trimmed = line.replace(/^ +/, "");
    const leadingSpaces = line.length - trimmed.length;
    if (!fenceChar) {
      if (
        leadingSpaces <= 3 &&
        trimmed.length >= 3 &&
        (trimmed[0] === "`" || trimmed[0] === "~")
      ) {
        const ch = trimmed[0];
        let cnt = 0;
        while (cnt < trimmed.length && trimmed[cnt] === ch) cnt++;
        if (cnt >= 3 && (ch === "~" || !trimmed.slice(cnt).includes("`"))) {
          fenceChar = ch;
          fenceLen = cnt;
          continue;
        }
      }
      out.push(stripInlineCodeSpans(line));
    } else if (
      leadingSpaces <= 3 &&
      trimmed.length >= fenceLen &&
      trimmed[0] === fenceChar
    ) {
      let cnt = 0;
      while (cnt < trimmed.length && trimmed[cnt] === fenceChar) cnt++;
      if (cnt >= fenceLen && trimmed.slice(cnt).trim() === "") {
        fenceChar = "";
        fenceLen = 0;
      }
    }
  }
  return out.join("\n");
}

function isInOpenMarkdownCode(raw) {
  const lines = raw.split("\n");
  let fenceChar = "";
  let fenceLen = 0;
  for (const line of lines) {
    const trimmed = line.replace(/^ +/, "");
    const leadingSpaces = line.length - trimmed.length;
    if (!fenceChar) {
      if (
        leadingSpaces <= 3 &&
        trimmed.length >= 3 &&
        (trimmed[0] === "`" || trimmed[0] === "~")
      ) {
        const ch = trimmed[0];
        let cnt = 0;
        while (cnt < trimmed.length && trimmed[cnt] === ch) cnt++;
        if (cnt >= 3 && (ch === "~" || !trimmed.slice(cnt).includes("`"))) {
          fenceChar = ch;
          fenceLen = cnt;
        }
      }
    } else if (
      leadingSpaces <= 3 &&
      trimmed.length >= fenceLen &&
      trimmed[0] === fenceChar
    ) {
      let cnt = 0;
      while (cnt < trimmed.length && trimmed[cnt] === fenceChar) cnt++;
      if (cnt >= fenceLen && trimmed.slice(cnt).trim() === "") {
        fenceChar = "";
        fenceLen = 0;
      }
    }
  }
  if (fenceChar) return true;
  const lastLine = lines.at(-1) || "";
  let unclosed = false;
  for (let i = 0; i < lastLine.length; ) {
    if (lastLine[i] !== "`") {
      i++;
      continue;
    }
    let tickLen = 0;
    while (i + tickLen < lastLine.length && lastLine[i + tickLen] === "`") {
      tickLen++;
    }
    let closeIdx = -1;
    for (let j = i + tickLen; j < lastLine.length; ) {
      if (lastLine[j] !== "`") {
        j++;
        continue;
      }
      let closeLen = 0;
      while (j + closeLen < lastLine.length && lastLine[j + closeLen] === "`") {
        closeLen++;
      }
      if (closeLen === tickLen) {
        closeIdx = j + closeLen;
        break;
      }
      j += closeLen;
    }
    if (closeIdx >= 0) {
      i = closeIdx;
    } else {
      unclosed = true;
      break;
    }
  }
  return unclosed;
}

const partialCiteTailPattern =
  /\[\[(?:k|kn|kno|know|knowl|knowle|knowled|knowledg|knowledge(?::[A-Za-z0-9_.-]*\]?)?)$/;

export function maskIncompleteStreamingCitation(text = "") {
  const raw = String(text || "");
  if (!raw.includes("[[") || !partialCiteTailPattern.test(raw)) return raw;
  if (isInOpenMarkdownCode(raw)) return raw;
  return raw.replace(partialCiteTailPattern, "");
}

// remarkKnowledgeCitations turns [[knowledge:<id>]] markers in markdown text
// into #knowledge-cite:<id> links so RichContent can render numbered superscript
// citations instead of raw marker syntax. When resolveDocKey is provided,
// adjacent markers that point to the exact same underlying document (for example,
// a search evidence ID and a read_document evidence ID for the same doc) are
// deduplicated in place while distinct documents remain intact.
export function remarkKnowledgeCitations(options = {}) {
  const resolveDocKey =
    typeof options?.resolveDocKey === "function"
      ? options.resolveDocKey
      : null;
  const walk = (node) => {
    if (
      !Array.isArray(node.children) ||
      node.type === "link" ||
      node.type === "linkReference" ||
      node.type === "code" ||
      node.type === "inlineCode"
    ) {
      return;
    }
    let changed = false;
    const children = [];
    for (const child of node.children) {
      if (child.type !== "text" || !child.value.includes("[[knowledge:")) {
        walk(child);
        children.push(child);
        continue;
      }
      changed = true;
      let last = 0;
      let activeGroupKeys = new Set();
      knowledgeCitePattern.lastIndex = 0;
      for (const match of child.value.matchAll(knowledgeCitePattern)) {
        const id = match[1];
        const between = child.value.slice(last, match.index);
        if (between.length > 0) {
          if (between.trim().length > 0) {
            activeGroupKeys = new Set();
            children.push({
              type: "text",
              value: between,
            });
          } else if (activeGroupKeys.size === 0) {
            children.push({
              type: "text",
              value: between,
            });
          }
        }
        const docKey = (resolveDocKey && resolveDocKey(id)) || `id:${id}`;
        if (!activeGroupKeys.has(docKey)) {
          if (
            between.length > 0 &&
            between.trim().length === 0 &&
            activeGroupKeys.size > 0
          ) {
            children.push({
              type: "text",
              value: between,
            });
          }
          activeGroupKeys.add(docKey);
          children.push({
            type: "link",
            url: `${knowledgeCitePrefix}${id}`,
            children: [{ type: "text", value: id }],
          });
        }
        last = match.index + match[0].length;
      }
      if (last < child.value.length) {
        children.push({ type: "text", value: child.value.slice(last) });
      }
    }
    if (changed) node.children = children;
  };
  return walk;
}

export function extractCitedIdsInOrder(texts = []) {
  const list = Array.isArray(texts) ? texts : [texts];
  const seen = new Set();
  const ordered = [];
  for (const raw of list) {
    const rawStr = String(raw || "");
    if (!rawStr.includes("[[knowledge:")) continue;
    const text = stripMarkdownCode(rawStr);
    if (!text.includes("[[knowledge:")) continue;
    knowledgeCitePattern.lastIndex = 0;
    for (const match of text.matchAll(knowledgeCitePattern)) {
      const id = match[1];
      if (id && !seen.has(id)) {
        seen.add(id);
        ordered.push(id);
      }
    }
  }
  return ordered;
}

export function knowledgeDocLabel(item) {
  const title = String(item?.title || "").trim();
  const fileName = String(item?.fileName || "").trim();
  const docId = String(item?.documentId || "").trim();
  const base = fileName.replace(/\.[^./\\]+$/, "");
  if (fileName && title) {
    if (
      title.toLowerCase() === fileName.toLowerCase() ||
      title.toLowerCase() === base.toLowerCase()
    ) {
      return fileName;
    }
    return `${title} (${fileName})`;
  }
  if (fileName) return fileName;
  if (title) return title;
  if (docId) return docId.split("/").filter(Boolean).at(-1) || docId;
  return String(item?.sourceId || item?.id || "").trim();
}

function docIdentityKey(doc) {
  const primary =
    doc.documentId || doc.title || doc.sourceId || doc.label || doc.id;
  const p = String(primary || "").trim().toLowerCase();
  if (!p) return "";
  const srv = String(doc.serverId || "").trim().toLowerCase();
  const src = String(doc.sourceId || "").trim().toLowerCase();
  const srcPart = src && src !== p ? src : "";
  return [srv, srcPart, p].join("::");
}

function callLevelCiteLabel(read, fallbackName = "") {
  const tool = String(read?.tool || "").trim() || "知识库检索";
  const query = String(read?.query || "").trim();
  if (query) return `${tool} ("${query}")`;
  return fallbackName || tool || read?.serverId || "知识库检索结果";
}

// buildKnowledgeSummary aggregates knowledgeReads strictly into:
// - usedDocs: documents explicitly cited via [[knowledge:<id>]] (numbered 1..N)
// - referencedDocs: all candidate documents retrieved or read in content calls
// - byCiteId: marker id -> { citeIndex, doc, locator }
export function buildKnowledgeSummary(
  knowledgeReads = [],
  texts = [],
  options = {},
) {
  const reads = Array.isArray(knowledgeReads) ? knowledgeReads : [];
  const callCount = reads.length;
  if (callCount === 0) {
    return {
      usedDocs: [],
      referencedDocs: [],
      byCiteId: new Map(),
      callCount: 0,
      hasContentCalls: false,
      hasCatalogOnly: false,
      hasUnavailableCalls: false,
      allContentFailed: false,
      hasUnstructuredOnly: false,
    };
  }

  const { live = false, scopeToTexts = false } = options || {};
  const listTexts = Array.isArray(texts) ? texts : [texts];
  const appearanceIds = extractCitedIdsInOrder(listTexts);
  const appearanceSet = new Set(appearanceIds);

  let hasContentCalls = false;
  let hasCatalogCalls = false;
  let orderCounter = 0;
  const contentReads = [];
  const docMap = new Map();
  // citeEntries maps marker id -> { docKey, locator, exact }
  const citeEntries = new Map();

  const upsertDoc = (candidate) => {
    const key = docIdentityKey(candidate);
    if (!key) return null;
    const prev = docMap.get(key);
    if (!prev) {
      const next = {
        key,
        order: orderCounter++,
        id: candidate.id || "",
        label: candidate.label || candidate.id || "知识库文档",
        title: candidate.title || "",
        fileName: candidate.fileName || "",
        documentId: candidate.documentId || "",
        sourceId: candidate.sourceId || "",
        sourceName: candidate.sourceName || "",
        serverId: candidate.serverId || "",
        locators: candidate.locator ? [candidate.locator] : [],
        citedLocators:
          candidate.used && candidate.locator ? [candidate.locator] : [],
        used: !!candidate.used,
        readFull: !!candidate.readFull,
        citeIndex: 0,
      };
      docMap.set(key, next);
      return next;
    }
    if (!prev.title && candidate.title) {
      prev.title = candidate.title;
    }
    if (!prev.fileName && candidate.fileName) {
      prev.fileName = candidate.fileName;
    }
    const upgraded = knowledgeDocLabel(prev);
    if (upgraded) prev.label = upgraded;
    prev.documentId = prev.documentId || candidate.documentId || "";
    prev.sourceId = prev.sourceId || candidate.sourceId || "";
    prev.sourceName = prev.sourceName || candidate.sourceName || "";
    prev.serverId = prev.serverId || candidate.serverId || "";
    if (!prev.label && candidate.label) prev.label = candidate.label;
    if (candidate.locator && !prev.locators.includes(candidate.locator)) {
      prev.locators.push(candidate.locator);
    }
    if (
      candidate.used &&
      candidate.locator &&
      !prev.citedLocators.includes(candidate.locator)
    ) {
      prev.citedLocators.push(candidate.locator);
    }
    prev.used = prev.used || !!candidate.used;
    prev.readFull = prev.readFull || !!candidate.readFull;
    return prev;
  };

  const recordCiteEntry = (citeId, docKey, locator, exact) => {
    const id = String(citeId || "").trim();
    if (!id || !docKey) return;
    const prev = citeEntries.get(id);
    if (!prev || (exact && !prev.exact)) {
      citeEntries.set(id, { docKey, locator: locator || "", exact: !!exact });
    }
  };

  const isEvUsed = (ev) => {
    if (!ev) return false;
    const id = String(ev.id || "").trim();
    if (scopeToTexts) return !!(id && appearanceSet.has(id));
    if (live) return !!(ev.cited || (id && appearanceSet.has(id)));
    return !!ev.cited;
  };

  const isCallCited = (read) => {
    const id = String(read?.id || "").trim();
    if (scopeToTexts) return !!(id && appearanceSet.has(id));
    if (live) return !!(read?.cited || (id && appearanceSet.has(id)));
    return !!read?.cited;
  };

  for (const read of reads) {
    if (!read) continue;
    if (isKnowledgeCatalogTool(read.tool)) {
      hasCatalogCalls = true;
      continue;
    }
    hasContentCalls = true;
    contentReads.push(read);
    const readFull = Boolean(read.readFull) || isKnowledgeReadTool(read.tool);
    const callCited = isCallCited(read);
    const evidences = Array.isArray(read.evidences) ? read.evidences : [];

    if (evidences.length > 0) {
      const evUsedFlags = evidences.map((ev) => isEvUsed(ev));
      const hasEvCited = evUsedFlags.some(Boolean);
      const singleUnambiguous = !hasEvCited && callCited && evidences.length === 1;

      evidences.forEach((ev, idx) => {
        if (!ev) return;
        const used = evUsedFlags[idx] || singleUnambiguous;
        const label =
          knowledgeDocLabel(ev) || ev.id || read.tool || "知识库文档";
        const doc = upsertDoc({
          id: ev.id || read.id || "",
          label,
          title: ev.title || "",
          fileName: ev.fileName || "",
          documentId: ev.documentId || "",
          sourceId: ev.sourceId || "",
          sourceName: ev.sourceName || "",
          serverId: read.serverId || "",
          locator: ev.locator || "",
          used,
          readFull,
        });
        if (doc && used) {
          if (ev.id && evUsedFlags[idx]) {
            recordCiteEntry(ev.id, doc.key, ev.locator || "", true);
          }
          if (singleUnambiguous && read.id) {
            recordCiteEntry(read.id, doc.key, ev.locator || "", true);
          }
        }
      });

      if (!hasEvCited && callCited && evidences.length > 1 && read.id) {
        const callDoc = upsertDoc({
          id: read.id,
          documentId: `call:${read.id}`,
          label: callLevelCiteLabel(read),
          title: "",
          sourceId: "",
          sourceName: "",
          serverId: read.serverId || "",
          used: true,
          readFull,
        });
        if (callDoc) {
          recordCiteEntry(read.id, callDoc.key, "", true);
        }
      }
      continue;
    }

    const targets = (Array.isArray(read.targets) ? read.targets : [])
      .map((t) => String(t || "").trim())
      .filter(Boolean);
    if (targets.length > 0) {
      const singleTarget = callCited && targets.length === 1;
      let firstDoc = null;
      for (const label of targets) {
        const doc = upsertDoc({
          id: read.id || label,
          label,
          title: label,
          sourceId: "",
          sourceName: "",
          serverId: read.serverId || "",
          used: singleTarget,
          readFull,
        });
        if (!firstDoc) firstDoc = doc;
      }
      if (callCited && read.id) {
        if (singleTarget && firstDoc) {
          recordCiteEntry(read.id, firstDoc.key, "", true);
        } else {
          const callDoc = upsertDoc({
            id: read.id,
            documentId: `call:${read.id}`,
            label: callLevelCiteLabel(read),
            title: "",
            sourceId: "",
            sourceName: "",
            serverId: read.serverId || "",
            used: true,
            readFull,
          });
          if (callDoc) {
            recordCiteEntry(read.id, callDoc.key, "", true);
          }
        }
      }
      continue;
    }

    if (callCited) {
      const doc = upsertDoc({
        id: read.id || "",
        documentId: read.id ? `call:${read.id}` : "",
        label: callLevelCiteLabel(read),
        title: read.query || "",
        sourceId: "",
        sourceName: "",
        serverId: read.serverId || "",
        used: true,
        readFull,
      });
      if (doc && read.id) {
        recordCiteEntry(read.id, doc.key, "", true);
      }
    }
  }

  const usedDocs = [];
  let nextIndex = 1;
  for (const citeId of appearanceIds) {
    const entry = citeEntries.get(citeId);
    if (!entry) continue;
    const doc = docMap.get(entry.docKey);
    if (doc && doc.used && !doc.citeIndex) {
      doc.citeIndex = nextIndex++;
      usedDocs.push(doc);
    }
  }
  for (const doc of docMap.values()) {
    if (doc.used && !doc.citeIndex) {
      doc.citeIndex = nextIndex++;
      usedDocs.push(doc);
    }
  }

  const byCiteId = new Map();
  for (const [citeId, entry] of citeEntries.entries()) {
    const doc = docMap.get(entry.docKey);
    if (doc && doc.citeIndex) {
      const activeLocators = doc.citedLocators?.length
        ? doc.citedLocators
        : doc.locators;
      byCiteId.set(citeId, {
        citeIndex: doc.citeIndex,
        doc,
        locator: entry.locator || activeLocators?.[0] || "",
      });
    }
  }

  const referencedDocs = [...docMap.values()].sort((a, b) => {
    if (a.used !== b.used) return a.used ? -1 : 1;
    if (a.used && b.used) return a.citeIndex - b.citeIndex;
    if (a.readFull !== b.readFull) return a.readFull ? -1 : 1;
    return a.order - b.order;
  });

  const hasUnavailableCalls = contentReads.some(
    (r) => r.status === "unavailable",
  );
  const allContentFailed =
    hasContentCalls && contentReads.every((r) => r.status === "unavailable");
  const hasUnstructuredOnly =
    hasContentCalls &&
    referencedDocs.length === 0 &&
    contentReads.some((r) => r.status === "unstructured");

  return {
    usedDocs,
    referencedDocs,
    byCiteId,
    callCount,
    hasContentCalls,
    hasCatalogOnly: hasCatalogCalls && !hasContentCalls,
    hasUnavailableCalls,
    allContentFailed,
    hasUnstructuredOnly,
  };
}

function replaceCiteMarkersOutsideCode(raw, replacer) {
  if (!raw.includes("[[knowledge:")) return raw;
  const replaceSegment = (seg) => {
    if (!seg.includes("[[knowledge:")) return seg;
    if (!seg.includes("`")) return replacer(seg);
    let out = "";
    let plainStart = 0;
    for (let i = 0; i < seg.length; ) {
      if (seg[i] !== "`") {
        i++;
        continue;
      }
      let tickLen = 0;
      while (i + tickLen < seg.length && seg[i + tickLen] === "`") tickLen++;
      let closeIdx = -1;
      for (let j = i + tickLen; j < seg.length; ) {
        if (seg[j] !== "`") {
          j++;
          continue;
        }
        let closeLen = 0;
        while (j + closeLen < seg.length && seg[j + closeLen] === "`") {
          closeLen++;
        }
        if (closeLen === tickLen) {
          closeIdx = j + closeLen;
          break;
        }
        j += closeLen;
      }
      if (closeIdx >= 0) {
        out += replacer(seg.slice(plainStart, i));
        out += seg.slice(i, closeIdx);
        i = closeIdx;
        plainStart = i;
      } else {
        i += tickLen;
      }
    }
    out += replacer(seg.slice(plainStart));
    return out;
  };

  const lines = raw.split("\n");
  const out = [];
  let fenceChar = "";
  let fenceLen = 0;
  for (const line of lines) {
    const trimmed = line.replace(/^ +/, "");
    const leadingSpaces = line.length - trimmed.length;
    if (!fenceChar) {
      if (
        leadingSpaces <= 3 &&
        trimmed.length >= 3 &&
        (trimmed[0] === "`" || trimmed[0] === "~")
      ) {
        const ch = trimmed[0];
        let cnt = 0;
        while (cnt < trimmed.length && trimmed[cnt] === ch) cnt++;
        if (cnt >= 3 && (ch === "~" || !trimmed.slice(cnt).includes("`"))) {
          fenceChar = ch;
          fenceLen = cnt;
          out.push(line);
          continue;
        }
      }
      out.push(replaceSegment(line));
    } else {
      out.push(line);
      if (
        leadingSpaces <= 3 &&
        trimmed.length >= fenceLen &&
        trimmed[0] === fenceChar
      ) {
        let cnt = 0;
        while (cnt < trimmed.length && trimmed[cnt] === fenceChar) cnt++;
        if (cnt >= fenceLen && trimmed.slice(cnt).trim() === "") {
          fenceChar = "";
          fenceLen = 0;
        }
      }
    }
  }
  return out.join("\n");
}

export function collapseAdjacentDuplicateCitations(text = "", summary = null) {
  const raw = String(text || "");
  if (!raw.includes("[[knowledge:") || !summary?.byCiteId?.size) return raw;
  const runPattern =
    /\[\[knowledge:[A-Za-z0-9_.-]+\]\](?:[ \t]*\[\[knowledge:[A-Za-z0-9_.-]+\]\])+/g;
  return replaceCiteMarkersOutsideCode(raw, (segment) =>
    segment.replace(runPattern, (run) => {
      const seenIndices = new Set();
      const kept = [];
      for (const m of run.matchAll(/\[\[knowledge:([A-Za-z0-9_.-]+)\]\]/g)) {
        const entry = summary.byCiteId.get(m[1]);
        if (entry) {
          if (seenIndices.has(entry.citeIndex)) continue;
          seenIndices.add(entry.citeIndex);
        }
        kept.push(m[0]);
      }
      return kept.join("");
    }),
  );
}

export function formatTextForCopy(
  text,
  knowledgeReads = [],
  textsOrSummary = null,
) {
  const raw = String(text || "");
  if (
    !raw.includes("[[knowledge:") ||
    !Array.isArray(knowledgeReads) ||
    knowledgeReads.length === 0
  ) {
    return raw;
  }
  const summary =
    textsOrSummary &&
    typeof textsOrSummary === "object" &&
    !Array.isArray(textsOrSummary) &&
    "byCiteId" in textsOrSummary
      ? textsOrSummary
      : buildKnowledgeSummary(
          knowledgeReads,
          Array.isArray(textsOrSummary) ? textsOrSummary : [raw],
        );

  const deduped = collapseAdjacentDuplicateCitations(raw, summary);
  const referencedIndices = new Set();
  const formatted = replaceCiteMarkersOutsideCode(deduped, (segment) => {
    let lastEnd = -1;
    let activeIndices = new Set();
    return segment.replace(
      /([ \t]*)\[\[knowledge:([A-Za-z0-9_.-]+)\]\]/g,
      (fullMatch, leadingSpace, id, offset) => {
        if (offset !== lastEnd) {
          activeIndices = new Set();
        }
        lastEnd = offset + fullMatch.length;
        const entry = summary.byCiteId?.get(id);
        if (!entry) return "";
        referencedIndices.add(entry.citeIndex);
        if (activeIndices.has(entry.citeIndex)) {
          return "";
        }
        activeIndices.add(entry.citeIndex);
        return `${leadingSpace}[${entry.citeIndex}]`;
      },
    );
  });

  const usedDocs = (summary.usedDocs || []).filter((doc) =>
    referencedIndices.size > 0 ? referencedIndices.has(doc.citeIndex) : true,
  );
  if (usedDocs.length === 0 || referencedIndices.size === 0) {
    return formatted;
  }
  const refs = usedDocs.map((doc) => {
    const locators = doc.citedLocators?.length
      ? doc.citedLocators
      : doc.locators || [];
    const source = doc.sourceName || doc.sourceId;
    const parts = [];
    if (source && source !== doc.label) parts.push(source);
    if (locators.length) parts.push(locators.join("、"));
    return parts.length
      ? `[${doc.citeIndex}] ${doc.label}（${parts.join(" · ")}）`
      : `[${doc.citeIndex}] ${doc.label}`;
  });
  return `${formatted}\n\n参考来源：\n${refs.join("\n")}`;
}
