import { createContext, useContext, useEffect, useState } from "react";
import { api } from "./api.js";

export const KnowledgeProject = createContext("");

const CITATION_RE =
  /(?<![\w@])([A-Za-z0-9][\w.-]{0,63})@([A-Za-z0-9][\w.-]{0,127}):([^\s#]+\.md)(?:#L(\d+)(?:-L(\d+))?)?/g;

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

export function useCitationStatuses(text) {
  const projectId = useContext(KnowledgeProject);
  const key = findCitations(text).join("\n");
  const [statuses, setStatuses] = useState({});
  useEffect(() => {
    const citations = key ? key.split("\n") : [];
    if (!projectId || citations.length === 0) {
      setStatuses({});
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
          if (item?.citation && (item.status === "changed" || item.status === "deleted")) {
            next[item.citation] = item.status;
          }
        }
        setStatuses(next);
      })
      .catch(() => {
        if (!cancel) setStatuses({});
      });
    return () => {
      cancel = true;
    };
  }, [projectId, key]);
  return statuses;
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
