import type { Metadata, Preview, ResourceCatalog, Selection } from "./types";

export type AgentClient = "cursor" | "codebuddy" | "codex" | "workbuddy";
export type Installation = { id: string; expiresAt: number; paths: Record<AgentClient, string> };

export async function createInstallation(selection: Selection): Promise<Installation> {
  const res = await fetch("/api/installations", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(selection),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export async function fetchMetadata(): Promise<Metadata> {
  const res = await fetch("/api/metadata");
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export async function fetchResources(body: {
  secretId: string;
  secretKey: string;
  region: string;
  projectId?: number | null;
  products: string[];
}): Promise<ResourceCatalog> {
  const res = await fetch("/api/resources", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export async function previewSchema(selection: Selection): Promise<Preview> {
  const res = await fetch("/api/preview-schema", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(selection),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export async function downloadZip(selection: Selection): Promise<void> {
  const res = await fetch("/api/starter.zip", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(selection),
  });
  if (!res.ok) throw new Error(await res.text());
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `${selection.project.name || "tcmcp"}.zip`;
  a.click();
  URL.revokeObjectURL(url);
}
