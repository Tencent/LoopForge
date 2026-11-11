import { createContext, useContext, useEffect, useState } from "react";
import { api } from "./api.js";

export const KnowledgeProject = createContext("");

const CITATION_RE =
  /(?<![\w@])([A-Za-z0-9][\w.-]{0,63})@([A-Za-z0-9][\w.-]{0,127}):([^\s#]+\.md)(?:#L(\d+)(?:-L(\d+))?)?/g;

export const KNOWLEDGE_CITATION_SCHEME = "knowledge-citation:";

export function findCitations(text) {
  if (!text) return [];
  return [...String(text).matchAll(CITATION_RE)].map((match) => match[0]);
}

export function applyCitationMarks(text, statuses) {
  if (!text) return "";
  if (!statuses) return String(text);
  return String(text).replace(CITATION_RE, (full) => {
    if (statuses[full] === "changed") return `${full}（原文已变更）`;
    if (statuses[full] === "deleted") return `${full}（原文已删除）`;
    return full;
  });
}

// Resolve citations to {status, kind, title, url}. The API stays the same as
// the freshness check; new fields are additive so older servers keep working.
export function useCitationInfo(text) {
  const projectId = useContext(KnowledgeProject);
  const key = findCitations(text).join("\n");
  const [info, setInfo] = useState({});
  useEffect(() => {
    const citations = key ? key.split("\n") : [];
    if (!projectId || citations.length === 0) {
      setInfo({});
      return undefined;
    }
    let cancel = false;
    api("/api/knowledge/citations/freshness", {
      method: "POST",
      body: JSON.stringify({ projectId, citations }),
    })
      .then((data) => {
        if (cancel) return;
        const next = {};
        for (const item of data?.items || []) {
          if (!item?.citation) continue;
          next[item.citation] = {
            status: item.status || "",
            kind: item.kind || "",
            title: item.title || "",
            url: item.url || "",
          };
        }
        setInfo(next);
      })
      .catch(() => {
        if (!cancel) setInfo({});
      });
    return () => {
      cancel = true;
    };
  }, [projectId, key]);
  return info;
}

export function useCitationStatuses(text) {
  const info = useCitationInfo(text);
  const statuses = {};
  for (const [citation, item] of Object.entries(info)) {
    if (item.status === "changed" || item.status === "deleted") {
      statuses[citation] = item.status;
    }
  }
  return statuses;
}

// Git citations open the commit line on the host. Hosted (or unresolved)
// citations are routed to the parent Knowledge Center via postMessage.
export function citationHref(citation, info = {}) {
  const item = info[citation];
  if (item?.kind === "git" && item?.url) return item.url;
  return KNOWLEDGE_CITATION_SCHEME + encodeURIComponent(citation);
}

function citationLink(citation, info) {
  return {
    type: "link",
    url: citationHref(citation, info),
    title: citation,
    data: { hProperties: { className: "kb-citation-link", "data-kb-citation": citation } },
    children: [{ type: "text", value: citation }],
  };
}

function suffix(status) {
  if (status === "changed") return "（原文已变更）";
  if (status === "deleted") return "（原文已删除）";
  return "";
}

function splitText(value, info) {
  const out = [];
  const re = new RegExp(CITATION_RE.source, "g");
  let last = 0;
  for (const match of value.matchAll(re)) {
    const index = match.index ?? 0;
    if (index > last) out.push({ type: "text", value: value.slice(last, index) });
    out.push(citationLink(match[0], info));
    const note = suffix(info[match[0]]?.status);
    if (note) out.push({ type: "text", value: note });
    last = index + match[0].length;
  }
  if (last < value.length) out.push({ type: "text", value: value.slice(last) });
  return out;
}

function transformNode(node, info) {
  if (!node?.children) return;
  if (node.type === "link" || node.type === "linkReference") return;
  const next = [];
  for (const child of node.children) {
    if (child.type === "text") {
      next.push(...splitText(child.value || "", info));
      continue;
    }
    if (child.type === "inlineCode") {
      const citations = findCitations(child.value);
      if (citations.length === 1 && citations[0] === child.value.trim()) {
        next.push(citationLink(child.value.trim(), info));
        const note = suffix(info[child.value.trim()]?.status);
        if (note) next.push({ type: "text", value: note });
        continue;
      }
    }
    transformNode(child, info);
    next.push(child);
  }
  node.children = next;
}

// A small remark plugin (no extra dependency) so citations inside inline code
// and plain text both become links before Markdown renders them.
export function remarkKnowledgeCitations(options = {}) {
  const info = options.info || {};
  return (tree) => transformNode(tree, info);
}

export function CitationNotes({ text }) {
  const statuses = useCitationStatuses(text);
  const notes = Object.entries(statuses);
  if (!notes.length) return null;
  return (
    <ul className="kb-cite-notes">
      {notes.map(([citation, status]) => (
        <li key={citation} data-freshness={status}>
          {citation}
          {status === "changed" ? " · 原文已变更" : " · 原文已删除"}
        </li>
      ))}
    </ul>
  );
}
