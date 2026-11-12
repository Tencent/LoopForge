import { createContext } from "react";

// Provided by the conversation when it has a workspace: opens a URL in the
// Agent browser of that workspace (the side panel's 浏览器 tab).
export const AgentBrowserLinks = createContext(null);

const localHost = /^(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])$/i;
// Workspace files a browser can show; source files stay plain text.
const viewable = /\.(html?|svg|pdf|png|jpe?g|gif|webp)$/i;

// agentBrowserTarget returns the URL to open in the Agent browser for a link
// or inline code the assistant wrote, or "" when it is not one. Only addresses
// that live on the execution machine qualify: the user's own browser cannot
// reach them, while the Agent browser runs next to the workspace.
export function agentBrowserTarget(raw) {
  const text = String(raw || "").trim();
  if (!text || /\s/.test(text) || text.includes("...") || text.includes("…"))
    return "";
  if (text.startsWith("/workspace/"))
    return viewable.test(text.replace(/[?#].*$/, "")) ? `file://${text}` : "";
  const bare = /^(localhost|127\.0\.0\.1)(:\d+)?(\/|$)/i.test(text);
  let url;
  try {
    url = new URL(bare ? `http://${text}` : text);
  } catch {
    return "";
  }
  if (url.protocol === "file:")
    return !url.host &&
      url.pathname.startsWith("/workspace/") &&
      viewable.test(url.pathname)
      ? url.href
      : "";
  if ((url.protocol === "http:" || url.protocol === "https:") && localHost.test(url.hostname))
    return url.href;
  return "";
}

const workspaceFileURL = /file:\/\/\/workspace\/[^\s<>()[\]{}"'`，。；、）]+/g;

// GFM autolinks http://localhost addresses but not file:// ones; this turns a
// file:///workspace/... address written in plain text into a link too.
export function remarkWorkspaceLinks() {
  const walk = (node) => {
    if (!Array.isArray(node.children) || node.type === "link" || node.type === "linkReference")
      return;
    let changed = false;
    const children = [];
    for (const child of node.children) {
      if (child.type !== "text" || !child.value.includes("file:///workspace/")) {
        walk(child);
        children.push(child);
        continue;
      }
      changed = true;
      let last = 0;
      for (const match of child.value.matchAll(workspaceFileURL)) {
        const url = match[0].replace(/[.,;:!?]+$/, "");
        if (match.index > last)
          children.push({ type: "text", value: child.value.slice(last, match.index) });
        children.push({ type: "link", url, children: [{ type: "text", value: url }] });
        last = match.index + url.length;
      }
      if (last < child.value.length)
        children.push({ type: "text", value: child.value.slice(last) });
    }
    if (changed) node.children = children;
  };
  return walk;
}

// devServerTarget returns the port and path of a plain-HTTP address on the
// execution machine (a dev server), or null. Those pages are shown natively:
// the platform proxies the port, so the user's own browser renders them.
// Workspace files (file:///workspace/…) are served the same way; their
// "port" is "file".
export function devServerTarget(raw) {
  let url;
  try {
    url = new URL(String(raw || "").trim());
  } catch {
    return null;
  }
  if (url.protocol === "file:") {
    if (url.host || !url.pathname || url.pathname === "/") return null;
    return { port: devFile, path: `${url.pathname}${url.search}${url.hash}` };
  }
  if (url.protocol !== "http:" || !localHost.test(url.hostname)) return null;
  const port = Number(url.port || 80);
  if (!Number.isInteger(port) || port <= 0 || port > 65535) return null;
  return { port, path: `${url.pathname || "/"}${url.search}${url.hash}` };
}

const devFile = "file";

// devServerOrigin is how the page's address starts on the execution machine.
export const devServerOrigin = (port) => (port === devFile ? "file://" : `http://localhost:${port}`);

// devServerLabel names the target in the panel.
export const devServerLabel = (port) => (port === devFile ? "工作区文件" : `localhost:${port}`);

export const devServerPrefix = (runId, port) =>
  `/devpreview/${encodeURIComponent(runId)}/${port}`;

// devServerURL is where the platform serves the dev server page.
export const devServerURL = (runId, port, path = "/") =>
  `${devServerPrefix(runId, port)}${path.startsWith("/") ? path : `/${path}`}`;

// devServerAddress maps a proxied page location back to the address the
// dev server has on the execution machine.
export function devServerAddress(runId, port, location) {
  const prefix = devServerPrefix(runId, port);
  const path = location?.pathname || "/";
  const rest = path === prefix ? "/" : path.startsWith(`${prefix}/`) ? path.slice(prefix.length) : path;
  return `${devServerOrigin(port)}${rest}${location?.search || ""}${location?.hash || ""}`;
}
