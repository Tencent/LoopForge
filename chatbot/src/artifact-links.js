import { createContext } from "react";

// Provides artifact lookup and preview opening inside LOOP node transcripts,
// activity traces, and markdown replies so generated files can be opened
// directly where they were created as well as from the deliverables summary
// and side panel.
export const ArtifactLinks = createContext(null);

function normalizePath(value) {
  return String(value || "")
    .trim()
    .replace(/^['"`(<\[]+|['"`.,;:!?)>\]]+$/g, "")
    .replace(/^file:\/\//i, "")
    .replace(/^\/+workspace\//, "")
    .replace(/^\.\//, "");
}

function basename(path) {
  const clean = normalizePath(path);
  const slash = clean.lastIndexOf("/");
  return slash >= 0 ? clean.slice(slash + 1) : clean;
}

export function matchArtifactForPath(raw, artifacts = []) {
  const candidate = normalizePath(raw);
  if (!candidate || !artifacts?.length || /\s/.test(candidate)) return null;
  const exact = artifacts.find(
    (item) =>
      normalizePath(item.path) === candidate ||
      normalizePath(item.name) === candidate,
  );
  if (exact) return exact;
  if (!candidate.includes("/")) {
    const byBase = artifacts.filter(
      (item) => basename(item.path || item.name) === candidate,
    );
    if (byBase.length === 1) return byBase[0];
  }
  return null;
}

export function matchArtifactForStep(step, artifacts = []) {
  if (!step || !artifacts?.length) return null;
  const raw = [step.detail, step.text, step.output]
    .filter(Boolean)
    .join(" ");
  if (!raw) return null;
  for (const artifact of artifacts) {
    const path = normalizePath(artifact.path || artifact.name);
    if (path && raw.includes(path)) return artifact;
  }
  const baseMatches = [];
  for (const artifact of artifacts) {
    const base = basename(artifact.path || artifact.name);
    if (base && base.includes(".") && raw.includes(base)) baseMatches.push(artifact);
  }
  return baseMatches.length === 1 ? baseMatches[0] : null;
}
