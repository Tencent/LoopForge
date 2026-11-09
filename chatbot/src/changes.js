// Workspace diff helpers for the collaboration side panel.
//
// The platform already serves the workspace git state
// (`/api/runs/:id/workspace` for the changed file list and
// `/api/runs/:id/workspace/diff` for the unified patch). These functions turn
// that payload into file rows and line-by-line hunks so the panel can render
// the same changes the agent made without re-reading tool arguments.

const HUNK = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@ ?(.*)$/;

const STATUS = {
  A: { code: "A", label: "新增" },
  M: { code: "M", label: "修改" },
  D: { code: "D", label: "删除" },
  R: { code: "R", label: "重命名" },
  C: { code: "C", label: "复制" },
  U: { code: "U", label: "冲突" },
};

export function statusLabel(code) {
  return (STATUS[code] || STATUS.M).label;
}

// Collapse the two porcelain status columns (index + worktree) into the one
// letter the panel shows next to a file.
export function changeState(change = {}) {
  const both = `${change.index || " "}${change.worktree || " "}`;
  if (both.includes("?")) return STATUS.A;
  if (both.includes("U")) return STATUS.U;
  if (both.includes("R")) return STATUS.R;
  if (both.includes("C")) return STATUS.C;
  if (both.includes("D")) return STATUS.D;
  if (both.includes("A")) return STATUS.A;
  return STATUS.M;
}

export function pathParts(path) {
  const parts = String(path || "").split("/").filter(Boolean);
  const name = parts.pop() || String(path || "");
  return { dir: parts.join("/"), name };
}

// Parse a unified diff, including several files concatenated into one patch
// (one `git diff` per repository). Unknown header lines are ignored so a
// truncated or extended diff still renders.
export function parseUnifiedDiff(text = "") {
  const files = [];
  let file = null;
  let hunk = null;
  let oldNo = 0;
  let newNo = 0;
  const flush = () => {
    if (file) files.push(file);
    file = null;
    hunk = null;
  };
  for (const line of String(text).replace(/\r\n/g, "\n").split("\n")) {
    if (line.startsWith("diff --git ")) {
      flush();
      const match = /^diff --git a\/(.+?) b\/(.+)$/.exec(line);
      file = {
        path: match ? match[2] : "",
        oldPath: match ? match[1] : "",
        status: "M",
        binary: false,
        hunks: [],
        additions: 0,
        deletions: 0,
      };
      continue;
    }
    if (!file) continue;
    if (!line) continue;
    if (line.startsWith("new file mode")) {
      file.status = "A";
      continue;
    }
    if (line.startsWith("deleted file mode")) {
      file.status = "D";
      continue;
    }
    if (line.startsWith("rename from ")) {
      file.oldPath = line.slice(12).trim();
      file.status = "R";
      continue;
    }
    if (line.startsWith("rename to ")) {
      file.path = line.slice(10).trim();
      file.status = "R";
      continue;
    }
    if (line.startsWith("Binary files ") || line.startsWith("GIT binary patch")) {
      file.binary = true;
      hunk = null;
      continue;
    }
    if (
      line.startsWith("--- ") ||
      line.startsWith("+++ ") ||
      line.startsWith("index ") ||
      line.startsWith("similarity index") ||
      line.startsWith("old mode") ||
      line.startsWith("new mode")
    )
      continue;
    const header = HUNK.exec(line);
    if (header) {
      hunk = { header: line, section: (header[3] || "").trim(), lines: [] };
      file.hunks.push(hunk);
      oldNo = Number(header[1]);
      newNo = Number(header[2]);
      continue;
    }
    if (!hunk) continue;
    if (line.startsWith("\\")) continue; // "\ No newline at end of file"
    const mark = line[0];
    const body = line.slice(1);
    if (mark === "+") {
      hunk.lines.push({ kind: "add", text: body, oldNo: 0, newNo });
      newNo += 1;
      file.additions += 1;
      continue;
    }
    if (mark === "-") {
      hunk.lines.push({ kind: "del", text: body, oldNo, newNo: 0 });
      oldNo += 1;
      file.deletions += 1;
      continue;
    }
    if (mark === " ") {
      hunk.lines.push({ kind: "ctx", text: body, oldNo, newNo });
      oldNo += 1;
      newNo += 1;
      continue;
    }
    // A line that is neither a hunk body nor a known header ends this hunk.
    hunk = null;
  }
  flush();
  return files;
}

// Untracked files have no patch: `git diff HEAD` ignores them. The panel shows
// their current content as a fully added file instead of an empty diff.
export function addedFileDiff(path, content = "") {
  const lines = String(content).replace(/\r\n/g, "\n").split("\n");
  if (lines.at(-1) === "") lines.pop();
  return {
    path,
    oldPath: path,
    status: "A",
    binary: false,
    hunks: lines.length
      ? [
          {
            header: `@@ -0,0 +1,${lines.length} @@`,
            section: "",
            lines: lines.map((text, index) => ({
              kind: "add",
              text,
              oldNo: 0,
              newNo: index + 1,
            })),
          },
        ]
      : [],
    additions: lines.length,
    deletions: 0,
  };
}

// Workspace paths may carry a repository label prefix (multi-repo workspaces)
// while `git diff` paths are always repository-relative.
export function findPatch(files = [], path = "") {
  const target = String(path || "").replace(/^\.\//, "");
  if (!target) return null;
  return (
    files.find((file) => file.path === target) ||
    files.find((file) => file.path && target.endsWith(`/${file.path}`)) ||
    files.find((file) => file.path && file.path.endsWith(`/${target}`)) ||
    null
  );
}

export function diffTotals(files = []) {
  return files.reduce(
    (total, file) => ({
      additions: total.additions + (file.additions || 0),
      deletions: total.deletions + (file.deletions || 0),
    }),
    { additions: 0, deletions: 0 },
  );
}

// Merge the porcelain file list with the parsed patch: the list gives paths and
// statuses (including untracked files), the patch gives line counts and hunks.
export function mergeChanges(changes = [], patches = []) {
  const used = new Set();
  const files = [];
  for (const change of changes) {
    const state = changeState(change);
    const patch = findPatch(patches, change.path);
    if (patch) used.add(patch);
    const { dir, name } = pathParts(change.path);
    files.push({
      path: change.path,
      name,
      dir,
      repository: change.repository || "",
      status: state.code,
      label: state.label,
      additions: patch?.additions || 0,
      deletions: patch?.deletions || 0,
      patch: patch || null,
      untracked: state.code === "A" && !patch,
    });
  }
  for (const patch of patches) {
    if (used.has(patch)) continue;
    const { dir, name } = pathParts(patch.path);
    files.push({
      path: patch.path,
      name,
      dir,
      repository: "",
      status: patch.status,
      label: statusLabel(patch.status),
      additions: patch.additions || 0,
      deletions: patch.deletions || 0,
      patch,
      untracked: false,
    });
  }
  return files.sort((a, b) => a.path.localeCompare(b.path));
}

// The panel only ever sees the working tree (`git status` + `git diff HEAD`),
// so edits the agent committed or pushed disappear from it. When the run
// records its branches, a link to the hosting service keeps that part of the
// session reachable instead of leaving a bare "no changes" that reads like a bug.
// Branch names keep their slashes, as both hosts write them in their own
// links; an encoded %2F in the path depends on the gateway and can 404.
const refPath = (ref) => encodeURIComponent(ref).replace(/%2F/gi, "/");

export function branchCompareUrl(repository) {
  const work = String(repository?.workBranch || "").trim();
  if (!work) return "";
  const base = String(repository?.baseBranch || "").trim();
  const same = !base || base === work;
  const github = githubRepoPath(repository?.cloneUrl);
  if (github)
    return same
      ? `https://github.com/${github}/tree/${refPath(work)}`
      : `https://github.com/${github}/compare/${refPath(base)}...${refPath(work)}`;
  const path = gongfengProjectPath(repository?.cloneUrl);
  if (!path) return "";
  if (same) return `https://git.example.com/${path}/-/tree/${refPath(work)}`;
  return `https://git.example.com/${path}/-/compare/${refPath(base)}...${refPath(work)}`;
}

// Only github.com and the legacy git.example.com remotes get a link: the
// token rules in the platform are scoped to those hosts, and other hosts may
// not render the same URL shape.
export function githubRepoPath(remote) {
  const raw = String(remote || "").trim();
  const match =
    /^(?:git@|ssh:\/\/git@)github\.com[:/]([^/]+\/[^/]+?)(?:\.git)?\/?$/.exec(raw) ||
    /^https:\/\/github\.com\/([^/]+\/[^/]+?)(?:\.git)?\/?$/.exec(raw);
  return match ? match[1] : "";
}

export function gongfengProjectPath(remote) {
  const raw = String(remote || "").trim();
  const match =
    /^(?:git@|ssh:\/\/git@)git\.example\.com[:/](.+)$/.exec(raw) ||
    /^https?:\/\/git\.example\.com\/(.+)$/.exec(raw);
  if (!match) return "";
  return match[1].replace(/\.git$/, "").replace(/^\/+|\/+$/g, "");
}
