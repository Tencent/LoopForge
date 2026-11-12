import React, {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import "../../web/app/git-ssh.js";
import HarnessIcon from "./HarnessIcon";
import SlidingTabs from "./SlidingTabs";
import { api, canChatInProject, canManageProjectRepos, write } from "./api";
import { usePanelPresence } from "./usePanelPresence";
import {
  CODEBUDDY_ENVIRONMENTS,
  codeBuddyEnvironment,
  codeBuddyEnvironmentIntent,
  codeBuddyEnvironmentLegacy,
  codeBuddyKeyURL,
  codeBuddyKeyWarning,
  openCredentialSettings,
} from "./credentials";
import "./quick-access-wizard.css";

const DEFAULT_SOP_ID = "generic-backend-delivery";

// Display names for the models users without their own key may use.
export function sharedModelNames(models, fallback = "") {
  const list = Array.isArray(models) && models.length ? models : fallback ? [fallback] : [];
  return list
    .map((id) => (id === "deepseek-v4.1-flash" ? "Deepseek-V4.1-Flash" : id))
    .join("、");
}

// The users API omits an empty (国际站) environment, so a full user response
// must clear one that was switched to 国际站 elsewhere instead of keeping it.
export function mergeFreshUser(prev, fresh) {
  return {
    ...prev,
    ...fresh,
    codeBuddyInternetEnvironment: fresh?.codeBuddyInternetEnvironment || "",
  };
}

const STEPS = [
  {
    id: "auth",
    title: "凭据与授权",
    desc: "GitHub 访问与模型密钥",
  },
  {
    id: "project",
    title: "项目与仓库",
    desc: "选择或新建项目，绑定 GitHub 仓库",
  },
  {
    id: "workflow",
    title: "默认角色与 SOP",
    desc: "使用平台默认角色与通用交付流程",
  },
  {
    id: "done",
    title: "完成",
    desc: "保存配置，直接开始使用",
  },
];

const FALLBACK_ROLES = [
  {
    id: "loopforge-requirement",
    name: "需求定型",
    description: "澄清需求边界与验收标准",
    prompt: "根据需求输入与代码库现状，确认目标、范围、排除项与验收标准，产出结构化需求说明与执行计划。\n\n任务输入：{{input}}",
    artifactId: "requirement-report",
    artifactPath: "requirement-report.md",
  },
  {
    id: "loopforge-design",
    name: "方案设计",
    description: "产出技术方案与实现计划",
    prompt: "分析受影响模块与调用链，设计接口、数据模型与最小改动方案，明确风险与验证范围。\n\n任务输入：{{input}}",
    artifactId: "tech-design",
    artifactPath: "tech-design.md",
  },
  {
    id: "loopforge-implement",
    name: "代码实现",
    description: "在工作分支实现并自测",
    prompt: "按方案与计划完成代码实现，运行受影响模块的单元测试并记录变更报告。\n\n任务输入：{{input}}",
    artifactId: "change-report",
    artifactPath: "change-report.md",
  },
  {
    id: "loopforge-review",
    name: "独立评审",
    description: "审查改动质量与规范",
    prompt: "独立审查代码改动的正确性、回归风险、安全边界与测试证据，给出评审结论。\n\n任务输入：{{input}}",
    artifactId: "review-report",
    artifactPath: "review-report.md",
  },
  {
    id: "loopforge-test",
    name: "测试验证",
    description: "运行测试并给出验收结论",
    prompt: "按验收标准执行自动化或回归测试，核对每条验收项并输出测试报告。\n\n任务输入：{{input}}",
    artifactId: "test-report",
    artifactPath: "test-report.md",
  },
  {
    id: "loopforge-mr",
    name: "发起 MR",
    description: "推送分支并创建合并请求",
    prompt: "将工作分支推送到代码仓库，创建或更新 Pull Request 并整理变更摘要。\n\n任务输入：{{input}}",
    artifactId: "mr-body",
    artifactPath: "mr-body.md",
  },
  {
    id: "loopforge-summary",
    name: "交付汇总",
    description: "汇总产物与沉淀知识",
    prompt: "汇总本次交付的改动、验证结果与 MR 链接，提炼可复用的项目经验。\n\n任务输入：{{input}}",
    artifactId: "summary-report",
    artifactPath: "summary-report.md",
  },
];

const FALLBACK_NODES = [
  { id: "requirement", name: "需求定型", type: "agent", agentId: "loopforge-requirement", metricStage: "clarification" },
  { id: "design", name: "方案设计", type: "agent", agentId: "loopforge-design", metricStage: "development" },
  { id: "implement", name: "代码实现", type: "agent", agentId: "loopforge-implement", metricStage: "development" },
  { id: "review", name: "独立评审", type: "agent", agentId: "loopforge-review", metricStage: "testing" },
  { id: "test", name: "测试验证", type: "agent", agentId: "loopforge-test", metricStage: "testing" },
  { id: "mr", name: "发起 MR", type: "agent", agentId: "loopforge-mr", metricStage: "development" },
  { id: "summary", name: "交付汇总", type: "agent", agentId: "loopforge-summary", metricStage: "none" },
];

const BRANCH_OPTIONS = [
  { value: "main", label: "main", hint: "GitHub 默认主分支" },
  { value: "master", label: "master", hint: "传统主分支" },
  { value: "preview", label: "preview", hint: "预发验证分支" },
  { value: "develop", label: "develop", hint: "开发集成分支" },
];

let closeActiveSelect = null;

/**
 * HarnessSelect: adapted from Harness UI's AppSelect.vue.
 * Supports searchable filtering, custom typed value, keyboard navigation,
 * animated chevron rotation, and a portal dropdown with presence transition.
 */
export function HarnessSelect({
  value,
  options = [],
  onChange,
  placeholder = "请选择",
  ariaLabel,
  searchable = false,
  searchPlaceholder = "搜索选项…",
  allowCustom = false,
  disabled = false,
}) {
  const selectId = `qa-select-${useId().replaceAll(":", "")}`;
  const listboxId = `${selectId}-listbox`;
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const [position, setPosition] = useState({
    top: 0,
    left: 0,
    width: 240,
    maxHeight: 280,
    placement: "bottom",
  });
  const presence = usePanelPresence(open, 160);
  const triggerRef = useRef(null);
  const panelRef = useRef(null);
  const searchRef = useRef(null);

  const matched = useMemo(
    () => options.find((opt) => opt.value === value),
    [options, value],
  );
  const selectedLabel = matched
    ? matched.label
    : allowCustom && value
      ? value
      : placeholder;
  const isPlaceholder = !matched && !(allowCustom && value);

  const rows = useMemo(() => {
    const q = query.trim().toLowerCase();
    const filtered =
      !q || !searchable
        ? options
        : options.filter(
            (opt) =>
              opt.label.toLowerCase().includes(q) ||
              opt.value.toLowerCase().includes(q) ||
              (opt.hint || "").toLowerCase().includes(q),
          );
    const trimmed = query.trim();
    const canCustom =
      allowCustom &&
      searchable &&
      trimmed &&
      !options.some((opt) => opt.value === trimmed);
    const customRow = canCustom
      ? [
          {
            value: trimmed,
            label: `使用 "${trimmed}"`,
            hint: "自定义",
            custom: true,
          },
        ]
      : [];
    return [...customRow, ...filtered];
  }, [options, query, searchable, allowCustom]);

  const close = useCallback(() => {
    setOpen(false);
    setQuery("");
  }, []);

  const place = useCallback(() => {
    const trig = triggerRef.current;
    if (!trig) return;
    const rect = trig.getBoundingClientRect();
    const margin = 8;
    const gap = 6;
    const maxPanelHeight = 280;
    const panelHeight = Math.min(
      panelRef.current?.offsetHeight || maxPanelHeight,
      maxPanelHeight,
    );
    const below = window.innerHeight - rect.bottom - gap - margin;
    const above = rect.top - gap - margin;
    const flipUp = below < panelHeight && above > below;
    const maxHeight = Math.max(
      120,
      Math.min(maxPanelHeight, flipUp ? above : below),
    );
    const visibleHeight = Math.min(panelHeight, maxHeight);
    const top = flipUp
      ? Math.max(margin, rect.top - gap - visibleHeight)
      : Math.min(rect.bottom + gap, window.innerHeight - margin - visibleHeight);
    const width = Math.max(180, rect.width);
    const left = Math.max(
      margin,
      Math.min(rect.left, window.innerWidth - width - margin),
    );
    setPosition({
      top: Math.round(top),
      left: Math.round(left),
      width: Math.round(width),
      maxHeight: Math.round(maxHeight),
      placement: flipUp ? "top" : "bottom",
    });
  }, []);

  useLayoutEffect(() => {
    if (presence.mounted) place();
  }, [presence.mounted, rows.length, place]);

  useEffect(() => {
    if (!open) return undefined;
    if (closeActiveSelect && closeActiveSelect !== close) closeActiveSelect();
    closeActiveSelect = close;
    const idx = Math.max(
      0,
      options.findIndex((opt) => opt.value === value),
    );
    setActiveIndex(idx);
    const frame = requestAnimationFrame(() => {
      if (searchable) searchRef.current?.focus({ preventScroll: true });
    });
    return () => {
      cancelAnimationFrame(frame);
      if (closeActiveSelect === close) closeActiveSelect = null;
    };
  }, [open, close, options, searchable, value]);

  useEffect(() => {
    if (!presence.mounted) return undefined;
    const onOutside = (event) => {
      if (
        triggerRef.current?.contains(event.target) ||
        panelRef.current?.contains(event.target)
      )
        return;
      close();
    };
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    document.addEventListener("pointerdown", onOutside, true);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
      document.removeEventListener("pointerdown", onOutside, true);
    };
  }, [presence.mounted, place, close]);

  function choose(nextValue) {
    if (disabled) return;
    onChange?.(nextValue);
    close();
    triggerRef.current?.focus({ preventScroll: true });
  }

  function onKeyDown(event) {
    if (disabled || event.isComposing) return;
    if (!open) {
      if (["ArrowDown", "ArrowUp", "Enter", " "].includes(event.key)) {
        event.preventDefault();
        setOpen(true);
      }
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      close();
      triggerRef.current?.focus({ preventScroll: true });
      return;
    }
    if (event.key === "Tab") {
      close();
      return;
    }
    if (event.key === "ArrowDown") {
      event.preventDefault();
      if (rows.length) setActiveIndex((i) => (i + 1) % rows.length);
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      if (rows.length)
        setActiveIndex((i) => (i - 1 + rows.length) % rows.length);
      return;
    }
    if (event.key === "Enter" || (event.key === " " && !searchable)) {
      event.preventDefault();
      const target = rows[activeIndex];
      if (target) choose(target.value);
    }
  }

  return (
    <div className="qa-select-root">
      <button
        ref={triggerRef}
        type="button"
        className={`qa-select-trigger ${open ? "is-open" : ""}`}
        disabled={disabled}
        role="combobox"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listboxId : undefined}
        aria-label={ariaLabel}
        onClick={() => !disabled && setOpen((v) => !v)}
        onKeyDown={onKeyDown}
      >
        <span
          className={`qa-select-value ${isPlaceholder ? "is-placeholder" : ""}`}
        >
          {selectedLabel}
        </span>
        {matched?.hint ? (
          <span className="qa-select-trigger-hint">{matched.hint}</span>
        ) : null}
        <HarnessIcon
          name="chevron-down"
          size={14}
          interactive={false}
          className={`qa-select-chevron ${open ? "is-open" : ""}`}
        />
      </button>

      {presence.mounted
        ? createPortal(
            <div
              ref={panelRef}
              className="qa-select-panel"
              data-phase={presence.phase}
              data-placement={position.placement}
              style={{
                top: position.top,
                left: position.left,
                width: position.width,
                maxHeight: position.maxHeight,
              }}
              onKeyDown={onKeyDown}
            >
              {searchable ? (
                <label className="qa-select-search">
                  <HarnessIcon name="search" size={14} interactive={false} />
                  <input
                    ref={searchRef}
                    type="search"
                    value={query}
                    placeholder={searchPlaceholder}
                    aria-label={searchPlaceholder}
                    onChange={(event) => {
                      setQuery(event.target.value);
                      setActiveIndex(0);
                    }}
                  />
                </label>
              ) : null}
              <div
                id={listboxId}
                role="listbox"
                aria-label={ariaLabel}
                className="qa-select-options"
              >
                {rows.length ? (
                  rows.map((row, index) => {
                    const selected = row.value === value;
                    const active = index === activeIndex;
                    return (
                      <button
                        key={(row.custom ? "custom:" : "opt:") + row.value}
                        type="button"
                        role="option"
                        aria-selected={selected}
                        className={[
                          "qa-select-option",
                          selected && "is-selected",
                          active && "is-active",
                        ]
                          .filter(Boolean)
                          .join(" ")}
                        onMouseEnter={() => setActiveIndex(index)}
                        onClick={() => choose(row.value)}
                      >
                        <span className="qa-select-option-label">
                          {row.label}
                        </span>
                        {row.hint ? (
                          <span className="qa-select-option-hint">
                            {row.hint}
                          </span>
                        ) : null}
                        {selected ? (
                          <HarnessIcon
                            name="check"
                            size={14}
                            interactive={false}
                            className="qa-select-option-check"
                          />
                        ) : null}
                      </button>
                    );
                  })
                ) : (
                  <p className="qa-select-empty">没有匹配的选项</p>
                )}
              </div>
            </div>,
            document.body,
          )
        : null}
    </div>
  );
}

/**
 * HarnessSwitch: adapted from Harness UI's AppSwitch.vue.
 */
export function HarnessSwitch({ checked, onChange, disabled = false, label }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      className={`qa-switch ${checked ? "is-on" : ""}`}
      onClick={() => !disabled && onChange?.(!checked)}
    >
      <span className="qa-switch-thumb" aria-hidden="true" />
    </button>
  );
}

/** Pill whose dot pops once each time `ok` flips; alternating keyframe names restart it. */
function StatusPill({ ok, okLabel = "已就绪", needLabel = "待配置" }) {
  const previous = useRef(ok);
  const [pops, setPops] = useState(0);
  useEffect(() => {
    if (previous.current === ok) return;
    previous.current = ok;
    setPops((n) => n + 1);
  }, [ok]);
  return (
    <span
      className={`qa-status-pill ${ok ? "is-ok" : "is-need"}`}
      data-pop={pops ? (pops % 2 ? "a" : "b") : undefined}
    >
      {ok ? okLabel : needLabel}
    </span>
  );
}

function VerifyMessage({ result, okText }) {
  const text = result.valid
    ? okText
    : result.error || `缺少权限：${(result.missing || []).join("、")}`;
  return (
    <p
      key={text}
      className={`qa-verify-msg qa-msg-in ${result.valid ? "is-ok" : "is-err"}`}
    >
      <HarnessIcon
        name={result.valid ? "check" : "info"}
        size={13}
        interactive={false}
      />
      <span>{text}</span>
    </p>
  );
}

/** Both labels share one grid cell so the button keeps its widest width while busy. */
function BusyLabel({ busy, busyLabel, children }) {
  return (
    <span className="qa-btn-label">
      <span className="qa-btn-label-cell" aria-hidden={busy || undefined}>
        {children}
      </span>
      <span className="qa-btn-label-cell" aria-hidden={!busy || undefined}>
        <HarnessIcon
          name="arc-spinner"
          size={14}
          motion={busy ? "busy" : "idle"}
          interactive={false}
        />
        {busyLabel}
      </span>
    </span>
  );
}

function parseGitHubRepo(raw) {
  let value = String(raw || "")
    .trim()
    .replace(/^['"]+|['"]+$/g, "")
    .replace(/^git\s+clone\s+/i, "")
    .replace(/^github:/i, "https://github.com/")
    .replace(/^(?:git@github\.com:|ssh:\/\/git@github\.com(?::22)?\/)/i, "https://github.com/");
  if (/^github\.com\//i.test(value)) value = `https://${value}`;
  let url;
  try {
    url = new URL(value);
  } catch {
    return null;
  }
  if (
    url.hostname !== "github.com" ||
    url.username ||
    url.password ||
    !["http:", "https:"].includes(url.protocol)
  )
    return null;
  const [owner = "", rawName = ""] = url.pathname.split("/").filter(Boolean);
  const name = rawName.replace(/\.git$/i, "");
  const valid = (seg) =>
    /^[A-Za-z0-9_.-]+$/.test(seg) && seg !== "." && seg !== "..";
  if (!valid(owner) || !valid(name)) return null;
  return {
    project: `github:${owner}/${name}`,
    provider: "github",
    name,
    webUrl: `https://github.com/${owner}/${name}`,
    cloneUrl: `https://github.com/${owner}/${name}.git`,
  };
}

// SSH preview first (it leaves github.com to the GitHub parser, matching the
// server); self-hosted legacy repositories keep the git.example.com form.
export function parseGongfengRepo(raw) {
  const sshInfo = globalThis.LoopForgeGitSSH(raw);
  if (sshInfo) return sshInfo;
  const github = parseGitHubRepo(raw);
  if (github) return github;
  const original = String(raw || "")
    .trim()
    .replace(/^['"]+|['"]+$/g, "");
  if (!original) return null;
  const strippedClone = /^git\s+clone\s+/i.test(original);
  let value = original
    .replace(/^git\s+clone\s+/i, "")
    .trim()
    .replace(/^['"]+|['"]+$/g, "");
  if (strippedClone && /\s/.test(value)) value = value.split(/\s+/)[0];
  if (!value || /[\s"'<>()\\]/.test(value)) return null;
  const ssh = /^(git@|ssh:\/\/)/i.test(value);
  value = value.replace(/\/+$/, "");
  const hadScheme = value.includes("://");
  const hadAt = value.includes("@");
  let rest = value;
  if (hadScheme) rest = rest.slice(rest.indexOf("://") + 3);
  if (rest.includes("@")) rest = rest.slice(rest.lastIndexOf("@") + 1);
  let path = "";
  if (!hadScheme && !hadAt && !rest.includes(":")) {
    path = rest;
  } else if (rest.includes("/")) {
    const cut = rest.search(/[:/]/);
    const host = rest.slice(0, cut);
    if (host.toLowerCase() !== "git.example.com") return null;
    path = rest.slice(cut + 1);
    if (rest[cut] === ":") {
      const slash = path.indexOf("/");
      if (slash >= 0 && /^\d+$/.test(path.slice(0, slash)))
        path = path.slice(slash + 1);
    }
  } else {
    return null;
  }
  path = path.replace(/^git\.example\.com\//i, "").replace(/\.git$/i, "");
  for (const marker of [
    "/-/",
    "/tree/",
    "/blob/",
    "/commit/",
    "/commits/",
    "/merge_requests/",
    "/issues/",
    "/snippets/",
    "/wikis/",
    "/releases/",
    "/tags/",
  ]) {
    const index = path.indexOf(marker);
    if (index >= 0) path = path.slice(0, index);
  }
  path = path.replace(/^\/+|\/+$/g, "");
  const parts = path.split("/");
  if (
    parts.length < 2 ||
    path.includes("..") ||
    parts.some((seg) => !seg || /[\s\\?#]/.test(seg))
  )
    return null;
  const name = parts[parts.length - 1];
  return {
    project: path,
    provider: "gongfeng",
    name,
    webUrl: `https://git.example.com/${path}`,
    cloneUrl: ssh
      ? `git@git.example.com:${path}.git`
      : `https://git.example.com/${path}.git`,
  };
}

function slugifyProjectId(value) {
  return String(value || "")
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9-_]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 48);
}

function deriveProjectKey(value) {
  const clean = String(value || "")
    .toUpperCase()
    .replace(/[^A-Z0-9]+/g, "")
    .slice(0, 8);
  return clean || "PROJ";
}

async function provisionFallbackProjectSOP(projectId, workflowId, owner) {
  const sandboxId = `local-${projectId}`.slice(0, 60);
  await write(
    "/api/sandboxes",
    {
      id: sandboxId,
      name: "本地开发沙箱",
      projectId,
      driver: "local",
      shared: false,
    },
    "PUT",
  );

  const agentIdMap = {};
  for (const role of FALLBACK_ROLES) {
    const agentId = `${role.id}-${projectId}`.slice(0, 60);
    agentIdMap[role.id] = agentId;
    await write(
      "/api/agents",
      {
        id: agentId,
        name: role.name,
        projectId,
        version: 1,
        executorId: "codebuddy",
        description: role.description,
        prompt: role.prompt,
        artifactOutputs: [
          {
            name: role.artifactId,
            path: role.artifactPath,
            mediaType: "text/markdown",
            required: true,
          },
        ],
      },
      "PUT",
    );
  }

  const nodes = FALLBACK_NODES.map((node, index) => ({
    id: node.id,
    name: node.name,
    type: "agent",
    agentId: agentIdMap[node.agentId] || node.agentId,
    metricStage: node.metricStage || "development",
    maxAttempts: 1,
    maxReworks: 1,
    position: { x: 80 + index * 220, y: 200 },
    artifactOutputs: [
      {
        name: `${node.id}-report`,
        path: `${node.id}-report.md`,
        mediaType: "text/markdown",
        required: true,
      },
    ],
  }));

  const edges = nodes.slice(1).map((node, index) => ({
    id: `e-${nodes[index].id}-${node.id}`,
    source: nodes[index].id,
    target: node.id,
    when: "success",
  }));

  const draft = await write(
    "/api/workflows",
    {
      id: workflowId,
      name: "后端交付 SOP",
      projectId,
      owner,
      status: "draft",
      version: 1,
      revision: 0,
      sopLane: "backend",
      sandboxProfileId: sandboxId,
      tags: ["快速接入", `generic-source:${DEFAULT_SOP_ID}`],
      description:
        "从需求定型、方案设计、代码实现、独立评审、测试验证到发起 MR 的标准交付流程。",
      nodes,
      edges,
    },
    "PUT",
  );
  return write(
    `/api/workflows/${encodeURIComponent(draft.id)}/publish`,
    {},
  ).catch(() => draft);
}

export default function QuickAccessWizard({
  open,
  user,
  projects = [],
  projectId = "",
  sponsoredModel = "",
  sponsoredModels = [],
  onClose,
  onCompleted,
  onOpenView,
}) {
  const [step, setStep] = useState(0);
  const [phase, setPhase] = useState("wizard");
  const [storedUser, setStoredUser] = useState(user);
  const [sshCredentials, setSSHCredentials] = useState({ userId: null, keys: null, error: false });
  const [catalogProjects, setCatalogProjects] = useState(projects);
  const [repositories, setRepositories] = useState([]);
  const [workflows, setWorkflows] = useState([]);
  const [projectAgents, setProjectAgents] = useState([]);
  const [templateAgents, setTemplateAgents] = useState([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [gitToken, setGitToken] = useState("");
  const [codeBuddyApiKey, setCodeBuddyApiKey] = useState("");
  // null follows the saved setting until the user picks an environment here.
  const [codeBuddyEnvChoice, setCodeBuddyEnvChoice] = useState(null);
  const [verifyingGit, setVerifyingGit] = useState(false);
  const [gitVerify, setGitVerify] = useState(null);
  const [projectMode, setProjectMode] = useState("existing");
  const [selectedProjectId, setSelectedProjectId] = useState(projectId);
  const [newProjectName, setNewProjectName] = useState("");
  const [newProjectId, setNewProjectId] = useState("");
  const [newProjectKey, setNewProjectKey] = useState("");
  const [idTouched, setIdTouched] = useState(false);
  const [keyTouched, setKeyTouched] = useState(false);
  const [repoUrl, setRepoUrl] = useState("");
  const [repoBranch, setRepoBranch] = useState("main");
  const [bindRepoNow, setBindRepoNow] = useState(true);
  const [makeDefaultRepo, setMakeDefaultRepo] = useState(true);
  const [selectedWorkflowId, setSelectedWorkflowId] =
    useState(DEFAULT_SOP_ID);
  const [previewTab, setPreviewTab] = useState("stages");
  const [completedSummary, setCompletedSummary] = useState(null);
  const [direction, setDirection] = useState("forward");
  const [stepAnimated, setStepAnimated] = useState(false);
  const headingRef = useRef(null);
  const bodyRef = useRef(null);
  const presence = usePanelPresence(open, 300);
  const reopenedWhileClosing = presence.phase === "closing";

  const editableProjects = useMemo(
    () =>
      (catalogProjects || []).filter((project) =>
        canChatInProject(project, storedUser || user),
      ),
    [catalogProjects, storedUser, user],
  );

  const projectSelectOptions = useMemo(
    () =>
      editableProjects.map((project) => ({
        value: project.id,
        label: project.name,
        hint: project.key || project.id,
      })),
    [editableProjects],
  );

  const genericWorkflows = useMemo(() => {
    const rows = (workflows || []).filter((workflow) => workflow.generic);
    return rows.length
      ? rows
      : [
          {
            id: DEFAULT_SOP_ID,
            name: "通用后端交付 SOP",
            description:
              "从需求定型、方案设计、代码实现、独立评审、测试验证到发起 MR 的标准交付链路。",
            generic: true,
            nodes: FALLBACK_NODES,
          },
        ];
  }, [workflows]);

  const currentProject = useMemo(
    () =>
      editableProjects.find((project) => project.id === selectedProjectId) ||
      editableProjects[0] ||
      null,
    [editableProjects, selectedProjectId],
  );

  // A member can work in a shared project (e.g. the welcome project) without
  // owning it; binding a repository there would only fail at the last step.
  const repoBindBlocked =
    projectMode === "existing" &&
    Boolean(currentProject) &&
    !canManageProjectRepos(currentProject, storedUser || user);

  const projectRepos = useMemo(
    () =>
      currentProject
        ? (repositories || []).filter(
            (repo) => repo.projectId === currentProject.id,
          )
        : [],
    [currentProject, repositories],
  );

  const defaultRepo = useMemo(() => {
    if (!currentProject) return null;
    return (
      projectRepos.find(
        (repo) => repo.id === currentProject.defaultRepositoryId,
      ) ||
      projectRepos[0] ||
      null
    );
  }, [currentProject, projectRepos]);

  const availableWorkflows = useMemo(() => {
    const projectIdTarget =
      projectMode === "existing" ? currentProject?.id : "";
    const ownedOrShared = (workflows || []).filter(
      (workflow) =>
        !workflow.generic &&
        ((projectIdTarget && workflow.projectId === projectIdTarget) ||
          workflow.shared),
    );
    const seen = new Set(ownedOrShared.map((item) => item.id));
    const merged = [...ownedOrShared];
    for (const generic of genericWorkflows) {
      if (!seen.has(generic.id)) merged.push(generic);
    }
    return merged;
  }, [workflows, genericWorkflows, projectMode, currentProject]);

  const sopSelectOptions = useMemo(
    () =>
      availableWorkflows.map((workflow) => {
        const stageCount = (workflow.nodes || FALLBACK_NODES).filter(
          (n) => n.type === "agent",
        ).length;
        const scopeLabel = workflow.generic
          ? "平台默认模板"
          : projectMode === "existing" &&
              workflow.projectId === currentProject?.id
            ? "本项目已有 SOP（直接复用）"
            : "共享 SOP";
        return {
          value: workflow.id,
          label: workflow.name,
          hint: `${stageCount} 个阶段 · ${scopeLabel}`,
        };
      }),
    [availableWorkflows, currentProject, projectMode],
  );

  const activeWorkflow = useMemo(
    () =>
      availableWorkflows.find(
        (workflow) => workflow.id === selectedWorkflowId,
      ) || availableWorkflows[0],
    [availableWorkflows, selectedWorkflowId],
  );

  const parsedRepo = useMemo(() => parseGongfengRepo(repoUrl), [repoUrl]);

  // Reopening during the exit transition reverses it onto the same content.
  useEffect(() => {
    if (!open || reopenedWhileClosing) return;
    setStep(0);
    setDirection("forward");
    setStepAnimated(false);
    setPhase("wizard");
    setError("");
    setGitToken("");
    setCodeBuddyApiKey("");
    setCodeBuddyEnvChoice(null);
    setGitVerify(null);
    setCompletedSummary(null);
    setBindRepoNow(true);
    setPreviewTab("stages");
    setIdTouched(false);
    setKeyTouched(false);
  }, [open]);

  const verifyStoredGitToken = useCallback(
    async (uid = user?.id) => {
      if (!uid) return null;
      setVerifyingGit(true);
      try {
        const result = await api(
          `/api/users/${encodeURIComponent(uid)}/git-token/verify`,
        );
        setGitVerify(result);
        return result;
      } catch (err) {
        const fallback = { valid: false, error: err.message };
        setGitVerify(fallback);
        return fallback;
      } finally {
        setVerifyingGit(false);
      }
    },
    [user?.id],
  );

  useEffect(() => {
    if (!open) return undefined;
    let active = true;
    async function load() {
      setLoading(true);
      try {
        const [usersRes, projectsRes, reposRes, workflowsRes, agentsRes] =
          await Promise.all([
            api("/api/users").catch(() => []),
            api("/api/projects").catch(() => projects),
            api("/api/repositories").catch(() => []),
            api("/api/workflows").catch(() => []),
            api("/api/agents").catch(() => []),
          ]);
        if (!active) return;
        const freshUser =
          (usersRes || []).find(
            (item) => Number(item.id) === Number(user?.id),
          ) || user;
        setStoredUser(mergeFreshUser(user, freshUser));
        if (freshUser?.gitTokenConfigured && freshUser?.id) {
          void verifyStoredGitToken(freshUser.id);
        }
        const nextProjects = Array.isArray(projectsRes)
          ? projectsRes
          : projects;
        setCatalogProjects(nextProjects);
        setRepositories(Array.isArray(reposRes) ? reposRes : []);
        const nextWorkflows = Array.isArray(workflowsRes) ? workflowsRes : [];
        setWorkflows(nextWorkflows);
        setProjectAgents(Array.isArray(agentsRes) ? agentsRes : []);
        const mine = nextProjects.filter((project) =>
          canChatInProject(project, freshUser),
        );
        const preferred =
          mine.find((project) => project.id === projectId) || mine[0] || null;
        if (preferred) {
          setProjectMode("existing");
          setSelectedProjectId(preferred.id);
          const ownedSop = nextWorkflows.find(
            (w) =>
              !w.generic &&
              w.projectId === preferred.id &&
              (w.id === preferred.workflowId || w.status === "published"),
          );
          if (ownedSop) setSelectedWorkflowId(ownedSop.id);
          else if (preferred.workflowId)
            setSelectedWorkflowId(preferred.workflowId);
        } else {
          setProjectMode("create");
          const login = freshUser?.loginName || "team";
          const suggestedName = `${freshUser?.chineseName || login} 的项目`;
          setNewProjectName(suggestedName);
          setNewProjectId(slugifyProjectId(`${login}-project`) || "my-project");
          setNewProjectKey(deriveProjectKey(login));
        }
      } finally {
        if (active) setLoading(false);
      }
    }
    void load();
    return () => {
      active = false;
    };
  }, [open, projectId, projects, user]);

  useEffect(() => {
    if (!open || !user?.id || user.isGuest) return undefined;
    let active = true;
    let generation = 0;
    const refresh = () => {
      const version = ++generation;
      api(`/api/users/${encodeURIComponent(user.id)}/git-ssh-keys`)
        .then((keys) => {
          if (active && version === generation) setSSHCredentials({ userId: user.id, keys: Array.isArray(keys) ? keys : [], error: false });
        })
        .catch(() => {
          if (active && version === generation) setSSHCredentials({ userId: user.id, keys: [], error: true });
        });
    };
    const credentialsChanged = (event) => {
      if (event.origin === location.origin && event.source === window.parent && event.data?.type === "agentflow-credentials-changed") refresh();
    };
    refresh();
    window.addEventListener("focus", refresh);
    window.addEventListener("message", credentialsChanged);
    return () => {
      active = false;
      window.removeEventListener("focus", refresh);
      window.removeEventListener("message", credentialsChanged);
    };
  }, [open, user?.id, user?.isGuest]);

  useEffect(() => {
    if (!open || !selectedWorkflowId) return undefined;
    // Only platform templates have template roles; a project SOP would 404.
    const known = (workflows || []).find((w) => w.id === selectedWorkflowId);
    if (known && !known.generic) {
      setTemplateAgents([]);
      return undefined;
    }
    let active = true;
    api(`/api/workflows/${encodeURIComponent(selectedWorkflowId)}/agents`)
      .then((rows) => {
        if (active && Array.isArray(rows) && rows.length)
          setTemplateAgents(rows);
      })
      .catch(() => {
        if (active) setTemplateAgents([]);
      });
    return () => {
      active = false;
    };
  }, [open, selectedWorkflowId, workflows]);

  useEffect(() => {
    if (!open) return;
    if (bodyRef.current) bodyRef.current.scrollTop = 0;
    headingRef.current?.focus({ preventScroll: true });
  }, [open, step, phase]);

  useEffect(() => {
    if (!open) return undefined;
    const refreshUser = () => {
      api("/api/users")
        .then((usersRes) => {
          const fresh = (usersRes || []).find(
            (item) => Number(item.id) === Number(user?.id),
          );
          if (fresh) {
            setStoredUser((prev) => mergeFreshUser(prev, fresh));
            if (fresh.gitTokenConfigured) {
              void verifyStoredGitToken(fresh.id);
            }
          }
        })
        .catch(() => {});
    };
    window.addEventListener("focus", refreshUser);
    const credentialsChanged = (event) => {
      if (event.origin === location.origin && event.source === window.parent && event.data?.type === "agentflow-credentials-changed") refreshUser();
    };
    window.addEventListener("message", credentialsChanged);
    return () => {
      window.removeEventListener("focus", refreshUser);
      window.removeEventListener("message", credentialsChanged);
    };
  }, [open, user?.id, verifyStoredGitToken]);

  useEffect(() => {
    if (!open) return undefined;
    const onKeyDown = (event) => {
      if (event.key === "Escape" && !saving && !closeActiveSelect) onClose?.();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, saving, onClose]);

  useEffect(() => {
    if (projectMode !== "create" || !parsedRepo) return;
    if (!newProjectName.trim()) setNewProjectName(parsedRepo.name);
    if (!idTouched)
      setNewProjectId(slugifyProjectId(parsedRepo.name) || "my-project");
    if (!keyTouched) setNewProjectKey(deriveProjectKey(parsedRepo.name));
  }, [parsedRepo, projectMode, newProjectName, idTouched, keyTouched]);

  useEffect(() => {
    if (!open || projectMode !== "existing" || !currentProject) return;
    const ownedSop = (workflows || []).find(
      (w) =>
        !w.generic &&
        w.projectId === currentProject.id &&
        (w.id === currentProject.workflowId || w.status === "published"),
    );
    if (ownedSop) setSelectedWorkflowId(ownedSop.id);
    else if (currentProject.workflowId)
      setSelectedWorkflowId(currentProject.workflowId);
    else setSelectedWorkflowId(DEFAULT_SOP_ID);
  }, [open, projectMode, currentProject?.id]);

  // A new project starts from the platform template; the SOP picked for the
  // previously selected project belongs to that project and is not offered.
  useEffect(() => {
    if (open && projectMode === "create") setSelectedWorkflowId(DEFAULT_SOP_ID);
  }, [open, projectMode]);

  const roles = useMemo(() => {
    if (activeWorkflow && !activeWorkflow.generic) {
      const boundIds = new Set(
        (activeWorkflow.nodes || []).map((n) => n.agentId).filter(Boolean),
      );
      const matched = (projectAgents || []).filter(
        (a) =>
          boundIds.has(a.id) ||
          a.projectId === (activeWorkflow.projectId || currentProject?.id),
      );
      if (matched.length) return matched;
    }
    return templateAgents.length ? templateAgents : FALLBACK_ROLES;
  }, [activeWorkflow, projectAgents, currentProject?.id, templateAgents]);

  if (!presence.mounted) return null;

  const closing = presence.phase === "closing";
  const activeStepIndex = phase === "success" ? STEPS.length - 1 : step;
  const activeStep = STEPS[activeStepIndex] || STEPS[0];
  const wizardStepCount = STEPS.length - 1;
  const gitReady = Boolean(storedUser?.gitTokenConfigured || gitToken.trim());
  const hasSSHKeys = Boolean(storedUser?.gitSshKeyConfigured);
  const selectedSSHRepo = parsedRepo || parseGongfengRepo(defaultRepo?.cloneUrl);
  const sshKeysLoaded = sshCredentials.userId === user?.id && sshCredentials.keys !== null;
  const hasSSHKey = (remote) => sshKeysLoaded && !sshCredentials.error && sshCredentials.keys.some(
    (key) => globalThis.LoopForgeGitSSHHost(key.host) === remote?.host,
  );
  const sshReady = hasSSHKey(selectedSSHRepo);
  const sshRepository = (parsedRepo || defaultRepo)?.provider === "ssh";
  const codeBuddyReady = Boolean(
    storedUser?.codeBuddyApiKeyConfigured || codeBuddyApiKey.trim(),
  );
  // The CLI gets the stored text as-is: an old value such as china preselects
  // what it meant, and still differs from it, so 下一步 rewrites it.
  const storedCodeBuddyEnvRaw = String(
    storedUser?.codeBuddyInternetEnvironment || "",
  ).trim();
  const storedCodeBuddyEnv =
    codeBuddyEnvironmentIntent(storedCodeBuddyEnvRaw)?.value ?? "";
  const codeBuddyEnvLegacy = codeBuddyEnvironmentLegacy(storedCodeBuddyEnvRaw);
  const codeBuddyEnv = codeBuddyEnvChoice ?? storedCodeBuddyEnv;
  const codeBuddyEnvInfo =
    codeBuddyEnvironment(codeBuddyEnv) || CODEBUDDY_ENVIRONMENTS[0];
  const codeBuddyWarning = codeBuddyKeyWarning(codeBuddyApiKey);
  const nodes =
    activeWorkflow?.nodes?.filter((node) => node.type === "agent") ||
    FALLBACK_NODES;

  function goToStep(next) {
    setDirection(next < step ? "back" : "forward");
    setStepAnimated(true);
    setStep(next);
  }

  async function saveTypedCredentials() {
    if (!user?.id) return storedUser;
    const payload = {};
    if (gitToken.trim()) payload.gitToken = gitToken.trim();
    if (codeBuddyApiKey.trim())
      payload.codeBuddyApiKey = codeBuddyApiKey.trim();
    // A key is always saved with the environment it was issued for.
    if (codeBuddyApiKey.trim() || codeBuddyEnv !== storedCodeBuddyEnvRaw)
      payload.codeBuddyInternetEnvironment = codeBuddyEnv;
    if (!Object.keys(payload).length) return storedUser;
    const saved = await write(
      `/api/users/${encodeURIComponent(user.id)}/credentials`,
      payload,
      "PUT",
    );
    const merged = { ...storedUser, ...saved };
    // The server omits an empty environment, so 国际站 must overwrite the old value here.
    if ("codeBuddyInternetEnvironment" in payload)
      merged.codeBuddyInternetEnvironment =
        saved?.codeBuddyInternetEnvironment || "";
    setStoredUser(merged);
    setGitToken("");
    setCodeBuddyApiKey("");
    setCodeBuddyEnvChoice(null);
    window.parent !== window &&
      window.parent.postMessage(
        { type: "agentflow-credentials-changed" },
        location.origin,
      );
    return merged;
  }

  async function verifyGit() {
    if (!user?.id || verifyingGit) return;
    setVerifyingGit(true);
    setGitVerify(null);
    setError("");
    try {
      const typed = gitToken.trim();
      const result = typed
        ? await write(
            `/api/users/${encodeURIComponent(user.id)}/git-token/verify`,
            { token: typed },
          )
        : await api(
            `/api/users/${encodeURIComponent(user.id)}/git-token/verify`,
          );
      setGitVerify(result);
      if (result?.valid && typed) await saveTypedCredentials();
    } catch (err) {
      setGitVerify({ valid: false, error: err.message });
    } finally {
      setVerifyingGit(false);
    }
  }

  async function nextStep() {
    if (saving) return;
    setError("");
    if (activeStep.id === "auth") {
      if (!gitReady && !hasSSHKeys && !codeBuddyReady && !sponsoredModel) {
        setError("请先配置 GitHub Git Token、Git SSH 密钥或 CodeBuddy API Key。");
        return;
      }
      setSaving(true);
      try {
        await saveTypedCredentials();
        goToStep(1);
      } catch (err) {
        setError(err.message);
      } finally {
        setSaving(false);
      }
      return;
    }
    if (activeStep.id === "project") {
      if (projectMode === "create") {
        const id = slugifyProjectId(newProjectId || newProjectName);
        if (!id || !newProjectName.trim()) {
          setError("请填写项目名称和项目标识。");
          return;
        }
        // Saving a project with an existing id replaces it (members, TAPD
        // settings), so "new project" must never land on an existing one.
        if (catalogProjects.some((item) => item.id === id)) {
          setError(
            `项目标识「${id}」已被现有项目使用。请换一个标识，或切换到「选择已有项目」。`,
          );
          return;
        }
      } else if (!currentProject) {
        setError("请选择一个项目，或切换到新建项目。");
        return;
      }
      if (bindRepoNow && repoUrl.trim() && repoBindBlocked) {
        setError(
          `你不是「${currentProject?.name || "该项目"}」的 Owner，不能为它绑定仓库。请新建自己的项目并绑定，或关闭「绑定仓库」继续体验。`,
        );
        return;
      }
      if (bindRepoNow && repoUrl.trim() && !parsedRepo) {
        setError(
          "请输入 GitHub 仓库链接或 Git SSH 克隆地址。CNB 只支持 HTTPS + 访问令牌。",
        );
        return;
      }
      goToStep(2);
      return;
    }
    if (activeStep.id === "workflow") {
      await finishSetup();
    }
  }

  async function finishSetup() {
    setSaving(true);
    setError("");
    try {
      const latestUser = await saveTypedCredentials();
      let targetProject = currentProject;
      // Save the SOP the page shows; a stale id hidden by the current
      // filter must never be bound to the project.
      const sopId = activeWorkflow?.id || DEFAULT_SOP_ID;
      if (projectMode === "create") {
        const id = slugifyProjectId(newProjectId || newProjectName);
        const key = deriveProjectKey(newProjectKey || id);
        if (catalogProjects.some((item) => item.id === id))
          throw new Error(
            `项目标识「${id}」已被现有项目使用，请换一个标识。`,
          );
        targetProject = await write(
          "/api/projects",
          {
            id,
            key,
            name: newProjectName.trim(),
            description: "",
            color: "#536dfe",
            ownerId: Number(user?.id) || 0,
            workflowId: sopId,
            members: [],
          },
          "POST",
        );
      } else if (
        targetProject &&
        targetProject.workflowId !== sopId &&
        Number(targetProject.ownerId) === Number(user?.id)
      ) {
        targetProject = await write(
          "/api/projects",
          { ...targetProject, workflowId: sopId },
          "PUT",
        );
      }

      let boundRepo = defaultRepo;
      if (bindRepoNow && parsedRepo && targetProject?.id && !repoBindBlocked) {
        const existingRepo = (repositories || []).find(
          (repo) =>
            repo.projectId === targetProject.id &&
            (parsedRepo.provider === "ssh"
              ? repo.cloneUrl === parsedRepo.cloneUrl
              : repo.gongfengProjectId === parsedRepo.project ||
                repo.webUrl === parsedRepo.webUrl),
        );
        if (existingRepo) {
          boundRepo = await write(
            "/api/repositories",
            {
              ...existingRepo,
              provider: parsedRepo.provider,
              name: parsedRepo.name,
              gongfengProjectId: parsedRepo.project,
              webUrl: parsedRepo.webUrl || existingRepo.webUrl,
              cloneUrl: parsedRepo.cloneUrl,
              defaultBranch: repoBranch.trim() || "main",
              repoUrl: repoUrl.trim(),
            },
            "PUT",
          );
        } else {
          boundRepo = await write("/api/repositories", {
            projectId: targetProject.id,
            provider: parsedRepo.provider || "gongfeng",
            name: parsedRepo.name,
            gongfengProjectId: parsedRepo.project,
            webUrl: parsedRepo.webUrl,
            cloneUrl: parsedRepo.cloneUrl,
            defaultBranch: repoBranch.trim() || "main",
            repoUrl: repoUrl.trim(),
          });
        }
      }

      let provisionedWorkflow = activeWorkflow;
      if (targetProject?.id) {
        const selectedConcrete = (workflows || []).find(
          (item) => item.id === sopId && !item.generic,
        );
        const existingProjectSop =
          selectedConcrete ||
          (workflows || []).find(
            (item) =>
              !item.generic &&
              item.projectId === targetProject.id &&
              ((item.tags || []).includes(`generic-source:${sopId}`) ||
                item.id === `${sopId}-${targetProject.id}` ||
                item.name === (activeWorkflow?.name || "通用后端交付 SOP")),
          );
        if (existingProjectSop) {
          provisionedWorkflow = existingProjectSop;
        } else {
          const hasStoredGeneric = (workflows || []).some(
            (item) => item.id === sopId && item.generic,
          );
          const cloneId = `${sopId}-${targetProject.id}`.slice(0, 60);
          const ownerName =
            user?.chineseName || user?.loginName || "平台";
          if (hasStoredGeneric) {
            try {
              const cloned = await write(
                `/api/workflows/${encodeURIComponent(sopId)}/clone`,
                {
                  id: cloneId,
                  name: (activeWorkflow?.name || "通用后端交付 SOP").replace(
                    /^通用/,
                    "",
                  ),
                  owner: ownerName,
                  projectId: targetProject.id,
                },
              );
              provisionedWorkflow = await write(
                `/api/workflows/${encodeURIComponent(cloned.id)}/publish`,
                {},
              ).catch(() => cloned);
            } catch {
              provisionedWorkflow = await provisionFallbackProjectSOP(
                targetProject.id,
                cloneId,
                ownerName,
              );
            }
          } else {
            provisionedWorkflow = await provisionFallbackProjectSOP(
              targetProject.id,
              cloneId,
              ownerName,
            );
          }
        }

        if (
          Number(targetProject.ownerId) === Number(user?.id) &&
          (boundRepo?.id || provisionedWorkflow?.id)
        ) {
          const nextDefaultRepoId =
            boundRepo?.id &&
            (projectMode === "create" ||
              !targetProject.defaultRepositoryId ||
              makeDefaultRepo)
              ? boundRepo.id
              : targetProject.defaultRepositoryId || "";
          targetProject = await write(
            "/api/projects",
            {
              ...targetProject,
              workflowId:
                provisionedWorkflow?.id || targetProject.workflowId || sopId,
              defaultRepositoryId: nextDefaultRepoId,
            },
            "PUT",
          );
        }
      }

      window.parent !== window &&
        window.parent.postMessage(
          {
            type: "agentflow-quick-access-completed",
            projectId: targetProject?.id || "",
          },
          location.origin,
        );

      setCompletedSummary({
        project: targetProject,
        repository: boundRepo,
        workflow: provisionedWorkflow || activeWorkflow,
        rolesCount: roles.length,
        gitReady: Boolean(boundRepo?.provider === "ssh" ? hasSSHKey(parseGongfengRepo(boundRepo.cloneUrl)) : latestUser?.gitTokenConfigured),
        codeBuddyReady: Boolean(
          latestUser?.codeBuddyApiKeyConfigured ||
            storedUser?.codeBuddyApiKeyConfigured,
        ),
      });
      setPhase("success");
      await onCompleted?.(targetProject?.id || "");
    } catch (err) {
      setError(err.message || "保存快速接入配置失败");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div
      className="qa-backdrop"
      role="presentation"
      data-phase={presence.phase}
      inert={closing}
      aria-hidden={closing || undefined}
      onClick={(event) => {
        if (event.target === event.currentTarget && !saving && !closing)
          onClose?.();
      }}
    >
      <div
        className="qa-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="qa-wizard-title"
      >
        <aside className="qa-rail" aria-label="快速接入步骤">
          <div className="qa-rail-head">
            <div className="qa-brand-mark">
              <HarnessIcon name="sparkles" size={20} />
            </div>
            <h2 id="qa-wizard-title">快速接入</h2>
            <p>提供最低限度的鉴权与仓库，直接使用默认角色和 SOP。</p>
          </div>
          <ol className="qa-steps">
            {STEPS.map((item, index) => {
              const done = index < activeStepIndex;
              const current = index === activeStepIndex;
              return (
                <li
                  key={item.id}
                  className={[
                    "qa-step",
                    done && "is-done",
                    current && "is-active",
                  ]
                    .filter(Boolean)
                    .join(" ")}
                  aria-current={current ? "step" : undefined}
                >
                  <div className="qa-step-mark">
                    <span
                      className="qa-step-no"
                      aria-hidden={done || undefined}
                    >
                      {item.id === "done" ? (
                        <HarnessIcon name="flag" size={13} interactive={false} />
                      ) : (
                        index + 1
                      )}
                    </span>
                    <span className="qa-step-check" aria-hidden="true">
                      <HarnessIcon name="check" size={13} interactive={false} />
                    </span>
                  </div>
                  <div className="qa-step-copy">
                    <div className="qa-step-title">{item.title}</div>
                    <div className="qa-step-desc">{item.desc}</div>
                  </div>
                </li>
              );
            })}
          </ol>
          <p className="qa-rail-foot">所有配置之后都能在个人配置与项目配置里修改。</p>
        </aside>

        <div className="qa-main">
          <button
            type="button"
            className="qa-close"
            aria-label="关闭快速接入"
            disabled={saving}
            onClick={onClose}
          >
            <HarnessIcon name="close" size={16} />
          </button>

          {phase === "success" ? (
            <>
              <div ref={bodyRef} className="qa-body">
                <div className="qa-success">
                  <div className="qa-success-head">
                    <div className="qa-success-mark" aria-hidden="true">
                      <svg viewBox="0 0 48 48" width="52" height="52">
                        <circle
                          className="qa-success-ring"
                          cx="24"
                          cy="24"
                          r="22"
                          pathLength="1"
                        />
                        <path
                          className="qa-success-tick"
                          d="M15 24.5l6 6 12-13"
                          pathLength="1"
                        />
                      </svg>
                    </div>
                    <h3
                      ref={headingRef}
                      tabIndex={-1}
                      className="qa-title qa-success-item"
                      style={{ "--i": 0 }}
                    >
                      快速接入已完成
                    </h3>
                    <p className="qa-lede qa-success-item" style={{ "--i": 1 }}>
                      已关联默认角色与「
                      {completedSummary?.workflow?.name || "通用后端交付 SOP"}
                      」，现在可以直接在工作台对话或派发需求。
                    </p>
                  </div>

                  <div className="qa-success-grid">
                    <section
                      className="qa-card qa-success-item"
                      style={{ "--i": 2 }}
                    >
                      <div className="qa-card-title">默认角色与流程</div>
                      <ul className="qa-role-list">
                        {roles.slice(0, 6).map((role) => (
                          <li key={role.id}>
                            <span className="qa-avatar">
                              {(role.name || "角").slice(0, 1)}
                            </span>
                            <span>{role.name}</span>
                            <small>默认角色</small>
                          </li>
                        ))}
                        <li>
                          <span className="qa-avatar is-flow">
                            <HarnessIcon name="workflow" size={13} />
                          </span>
                          <span>
                            {completedSummary?.workflow?.name ||
                              "通用后端交付 SOP"}
                          </span>
                          <small>已绑定</small>
                        </li>
                      </ul>
                    </section>

                    <section
                      className="qa-card qa-success-item"
                      style={{ "--i": 3 }}
                    >
                      <div className="qa-card-title">已生效配置</div>
                      <ul className="qa-status-list">
                        <li
                          className={
                            completedSummary?.gitReady ? "is-ok" : "is-warn"
                          }
                        >
                          <HarnessIcon
                            name={completedSummary?.gitReady ? "check" : "info"}
                            size={14}
                          />
                          <span>
                            {completedSummary?.repository?.provider === "ssh" ? "Git SSH 访问：" : "GitHub 访问："}
                            {completedSummary?.gitReady
                              ? "已就绪"
                              : "未配置，克隆/推送私有仓库前可在个人配置补齐"}
                          </span>
                        </li>
                        <li
                          className={
                            completedSummary?.codeBuddyReady || sponsoredModel
                              ? "is-ok"
                              : "is-warn"
                          }
                        >
                          <HarnessIcon
                            name={
                              completedSummary?.codeBuddyReady
                                ? "check"
                                : "info"
                            }
                            size={14}
                          />
                          <span>
                            CodeBuddy API Key：
                            {completedSummary?.codeBuddyReady
                              ? "已配置"
                              : sponsoredModel
                                ? `未配置。对话和 SOP 都会使用平台共享 Key（可用模型：${sharedModelNames(sponsoredModels, sponsoredModel)}），可以直接开始；配置自己的 Key 后可选用任意模型`
                                : "未配置。对话可用平台默认模型，但运行 SOP 时节点需要它调模型，请先在个人配置补齐"}
                          </span>
                        </li>
                        <li className="is-ok">
                          <HarnessIcon name="check" size={14} />
                          <span>
                            项目：
                            {completedSummary?.project?.name || "当前项目"}
                            {completedSummary?.repository?.name
                              ? ` · 仓库 ${completedSummary.repository.name}`
                              : " · 仓库可在运行时指定"}
                          </span>
                        </li>
                      </ul>
                    </section>
                  </div>

                  <div className="qa-actions-grid">
                    <button
                      type="button"
                      className="qa-action is-primary qa-success-item"
                      style={{ "--i": 4 }}
                      onClick={() => onClose?.()}
                    >
                      <span className="qa-action-icon">
                        <HarnessIcon name="play" size={16} />
                      </span>
                      <span className="qa-action-copy">
                        <strong>开始第一条需求</strong>
                        <small>
                          回到工作台输入框，直接描述要做的需求或问题
                        </small>
                      </span>
                      <HarnessIcon
                        name="chevron-right"
                        size={16}
                        className="qa-action-chevron"
                      />
                    </button>
                    <button
                      type="button"
                      className="qa-action qa-success-item"
                      style={{ "--i": 5 }}
                      data-hover-ink-control=""
                      onClick={() => {
                        onClose?.();
                        onOpenView?.("sops");
                      }}
                    >
                      <span className="qa-action-icon">
                        <HarnessIcon name="workflow" size={16} />
                      </span>
                      <span className="qa-action-copy">
                        <strong>查看或修改 SOP</strong>
                        <small>
                          在流程模板里查看默认 SOP，或复制到项目自由调整
                        </small>
                      </span>
                      <HarnessIcon
                        name="chevron-right"
                        size={16}
                        className="qa-action-chevron"
                      />
                    </button>
                  </div>
                </div>
              </div>
              <footer className="qa-footer">
                <div className="qa-footer-spacer" />
                <button
                  type="button"
                  className="qa-btn qa-btn-ghost"
                  data-hover-ink-control=""
                  onClick={onClose}
                >
                  关闭
                </button>
              </footer>
            </>
          ) : (
            <>
              <div ref={bodyRef} className="qa-body">
                <div
                  key={activeStep.id}
                  className={`qa-stage ${stepAnimated ? "is-entering" : ""}`}
                  data-direction={direction}
                >
                  <div className="qa-kicker">
                    第 {activeStepIndex + 1} 步，共 {wizardStepCount} 步
                  </div>
                  <h3 ref={headingRef} tabIndex={-1} className="qa-title">
                    {activeStep.title}
                  </h3>

                  {error ? (
                    <p key={error} className="qa-note is-err" role="alert">
                      <HarnessIcon name="info" size={14} interactive={false} />
                      <span>{error}</span>
                    </p>
                  ) : null}

                  {activeStep.id === "auth" ? (
                    <div className="qa-pane">
                      <section className="qa-cred-card">
                        <header className="qa-cred-head">
                          <div>
                            <strong>GitHub 代码仓库授权</strong>
                            <span>
                              用于克隆私有仓库、创建分支、推送代码与发起 PR
                            </span>
                          </div>
                          <StatusPill ok={gitReady} />
                        </header>
                        <div className="qa-cred-body">
                          <button type="button" className="qa-btn qa-btn-primary qa-btn-sm" onClick={() => openCredentialSettings("gitToken")}>
                            {storedUser?.gitTokenConfigured ? "管理 GitHub 授权" : "连接 GitHub"}
                          </button>
                          <p className="qa-hint">在个人配置中完成授权，返回后继续；无需手动复制令牌。</p>
                          <p className="qa-hint">GitLab 等 SSH 仓库：<button type="button" className="qa-link-btn" onClick={() => openCredentialSettings("gitSshKey")}>{hasSSHKeys ? "管理 Git SSH 密钥" : "配置 Git SSH 密钥"}</button></p>
                          <details>
                            <summary>使用访问令牌（高级）</summary>
                          <div className="qa-input-row">
                            <input
                              type="password"
                              className="qa-input"
                              value={gitToken}
                              onChange={(e) => setGitToken(e.target.value)}
                              placeholder={
                                storedUser?.gitTokenConfigured
                                  ? "已配置，留空保持不变"
                                  : "GitHub personal access token（授予 Contents 与 Pull requests 读写权限）"
                              }
                              autoComplete="new-password"
                            />
                            <button
                              type="button"
                              className="qa-btn qa-btn-outline qa-btn-sm qa-btn-field"
                              data-hover-ink-control=""
                              aria-busy={verifyingGit || undefined}
                              disabled={
                                !verifyingGit &&
                                !gitToken.trim() &&
                                !storedUser?.gitTokenConfigured
                              }
                              onClick={verifyGit}
                            >
                              <BusyLabel
                                busy={verifyingGit}
                                busyLabel="校验中…"
                              >
                                校验
                              </BusyLabel>
                            </button>
                            <a
                              className="qa-link"
                              data-hover-ink-control=""
                              href="https://github.com/settings/tokens"
                              target="_blank"
                              rel="noopener noreferrer"
                            >
                              申请令牌 ↗
                            </a>
                          </div>
                          {gitVerify ? (
                            <VerifyMessage
                              result={gitVerify}
                              okText={`令牌有效${gitVerify.login ? `（${gitVerify.login}）` : ""}`}
                            />
                          ) : null}
                          </details>
                        </div>
                      </section>

                      <section className="qa-cred-card">
                        <header className="qa-cred-head">
                          <div>
                            <strong>CodeBuddy API Key</strong>
                            <span>
                              {sponsoredModel
                                ? "可选：未配置时对话和 SOP 使用平台共享模型，配置后可选用其他模型"
                                : "AI 对话与 SOP 节点调用模型时使用"}
                            </span>
                          </div>
                          <StatusPill ok={codeBuddyReady} />
                        </header>
                        <div className="qa-cred-body">
                          <div className="qa-field">
                            <span className="qa-label">网络环境</span>
                            <HarnessSelect
                              value={codeBuddyEnv}
                              options={CODEBUDDY_ENVIRONMENTS.map((env) => ({
                                value: env.value,
                                label: env.label,
                                hint: env.site,
                              }))}
                              onChange={setCodeBuddyEnvChoice}
                              ariaLabel="CodeBuddy 网络环境"
                            />
                          </div>
                          <div className="qa-input-row">
                            <input
                              type="password"
                              className="qa-input"
                              value={codeBuddyApiKey}
                              onChange={(e) =>
                                setCodeBuddyApiKey(e.target.value)
                              }
                              placeholder={
                                storedUser?.codeBuddyApiKeyConfigured
                                  ? "已配置，留空保持不变"
                                  : "粘贴 CodeBuddy API Key"
                              }
                              aria-label="CodeBuddy API Key"
                              autoComplete="new-password"
                            />
                            <a
                              className="qa-link"
                              data-hover-ink-control=""
                              href={codeBuddyKeyURL(codeBuddyEnv)}
                              target="_blank"
                              rel="noopener noreferrer"
                            >
                              获取 API Key ↗
                            </a>
                          </div>
                          <p
                            className={`qa-hint${codeBuddyWarning ? " is-warn" : ""}`}
                            role="status"
                          >
                            {codeBuddyWarning ||
                              `${codeBuddyEnvLegacy && codeBuddyEnvChoice === null ? `原来填写的「${storedCodeBuddyEnvRaw}」无法识别，CodeBuddy 实际按国际站连接，点下一步改为当前选择。` : ""}Key 只能在创建它的站点使用：当前选择${codeBuddyEnvInfo.label}（${codeBuddyEnvInfo.site}），「获取 API Key」会打开该站点的密钥页。`}
                          </p>
                        </div>
                      </section>
                    </div>
                  ) : null}

                  {activeStep.id === "project" ? (
                    <div className="qa-pane">
                      {editableProjects.length ? (
                        <SlidingTabs
                          className="qa-sliding-tabs qa-sliding-tabs-inline"
                          aria-label="项目选择方式"
                        >
                          <button
                            type="button"
                            role="tab"
                            aria-selected={projectMode === "existing"}
                            onClick={() => setProjectMode("existing")}
                          >
                            <HarnessIcon name="folder" size={14} />
                            选择已有项目
                            <span className="sliding-tab-count">
                              {editableProjects.length}
                            </span>
                          </button>
                          <button
                            type="button"
                            role="tab"
                            aria-selected={projectMode === "create"}
                            onClick={() => setProjectMode("create")}
                          >
                            <HarnessIcon name="sparkles" size={14} />
                            新建项目
                          </button>
                        </SlidingTabs>
                      ) : null}

                      {projectMode === "existing" && editableProjects.length ? (
                        <section
                          key="existing"
                          className="qa-section qa-fade-in"
                        >
                          <div className="qa-field">
                            <span className="qa-label">选择项目</span>
                            <HarnessSelect
                              value={currentProject?.id || ""}
                              options={projectSelectOptions}
                              onChange={setSelectedProjectId}
                              searchable={projectSelectOptions.length > 4}
                              searchPlaceholder="搜索项目名称或 Key…"
                              ariaLabel="选择项目"
                            />
                          </div>
                          {projectRepos.length ? (
                            <div className="qa-bound-repo-list">
                              {projectRepos.map((repo) => {
                                const isDef =
                                  repo.id ===
                                  (currentProject?.defaultRepositoryId ||
                                    defaultRepo?.id);
                                return (
                                  <div key={repo.id} className="qa-bound-repo">
                                    <span
                                      className={`qa-status-pill ${isDef ? "is-ok" : ""}`}
                                    >
                                      {isDef ? "默认仓库" : "已关联仓库"}
                                    </span>
                                    <strong>{repo.name}</strong>
                                    <code>
                                      {repo.provider === "github" && repo.webUrl
                                        ? repo.webUrl.replace(
                                            /^https?:\/\//,
                                            "",
                                          )
                                        : repo.gongfengProjectId || repo.webUrl || repo.cloneUrl}
                                    </code>
                                    <small>
                                      {repo.defaultBranch || "main"}
                                    </small>
                                  </div>
                                );
                              })}
                            </div>
                          ) : null}
                        </section>
                      ) : (
                        <section
                          key="create"
                          className="qa-section qa-grid-3 qa-fade-in"
                        >
                          <label className="qa-field">
                            <span className="qa-label">
                              项目名称 <em>*</em>
                            </span>
                            <input
                              className="qa-input"
                              value={newProjectName}
                              onChange={(e) => {
                                const value = e.target.value;
                                setNewProjectName(value);
                                if (!idTouched)
                                  setNewProjectId(
                                    slugifyProjectId(value) || "my-project",
                                  );
                                if (!keyTouched)
                                  setNewProjectKey(deriveProjectKey(value));
                              }}
                              placeholder="例如：智能网关"
                            />
                          </label>
                          <label className="qa-field">
                            <span className="qa-label">
                              项目标识 <em>*</em>
                            </span>
                            <input
                              className="qa-input qa-mono"
                              value={newProjectId}
                              onChange={(e) => {
                                setIdTouched(true);
                                setNewProjectId(e.target.value);
                              }}
                              placeholder="gateway-service"
                            />
                          </label>
                          <label className="qa-field">
                            <span className="qa-label">项目 Key</span>
                            <input
                              className="qa-input qa-mono"
                              value={newProjectKey}
                              onChange={(e) => {
                                setKeyTouched(true);
                                setNewProjectKey(e.target.value.toUpperCase());
                              }}
                              placeholder="GW"
                            />
                          </label>
                        </section>
                      )}

                      <section
                        className={`qa-cred-card ${bindRepoNow ? "" : "is-collapsed"}`}
                      >
                        <header className="qa-cred-head">
                          <div>
                            <strong>
                              {projectMode === "existing" && projectRepos.length
                                ? "为该项目添加 GitHub 仓库"
                                : "绑定默认 GitHub 仓库"}
                            </strong>
                            <span
                              key={String(bindRepoNow)}
                              className="qa-fade-in"
                            >
                              {projectMode === "existing" && projectRepos.length
                                ? bindRepoNow
                                  ? "旧项目可直接追加新仓库，复用已有流程与角色；留空则沿用现有仓库"
                                  : "沿用项目现有仓库与流程"
                                : bindRepoNow
                                  ? "粘贴 GitHub 链接或 Git SSH 地址，自动设为项目默认仓库"
                                  : "已跳过，可在派发需求或项目配置中再绑定"}
                            </span>
                          </div>
                          <HarnessSwitch
                            checked={bindRepoNow}
                            onChange={setBindRepoNow}
                            label="添加或绑定代码仓库"
                          />
                        </header>

                        <div
                          className={`qa-collapse ${bindRepoNow ? "" : "is-closed"}`}
                          inert={!bindRepoNow}
                          aria-hidden={!bindRepoNow || undefined}
                        >
                          <div className="qa-collapse-inner">
                            <div className="qa-cred-body">
                              {repoBindBlocked ? (
                                <div
                                  className="qa-hint is-warn qa-repo-permission"
                                  role="note"
                                >
                                  <p>
                                    「{currentProject?.name}」是已有项目，只有它的
                                    Owner 或平台管理员可以绑定仓库。你可以：
                                  </p>
                                  <div className="qa-repo-permission-actions">
                                    <button
                                      type="button"
                                      className="qa-link-btn"
                                      onClick={() => {
                                        setError("");
                                        setProjectMode("create");
                                      }}
                                    >
                                      新建自己的项目并绑定
                                    </button>
                                    <button
                                      type="button"
                                      className="qa-link-btn"
                                      onClick={() => {
                                        setError("");
                                        setBindRepoNow(false);
                                      }}
                                    >
                                      跳过绑定，继续体验
                                    </button>
                                  </div>
                                </div>
                              ) : null}
                              <div className="qa-repo-grid">
                                <label className="qa-field">
                                  <span className="qa-label">
                                    {projectMode === "existing" &&
                                    projectRepos.length
                                      ? "新增仓库地址（可选）"
                                      : "GitHub 链接或 Git SSH 地址"}
                                  </span>
                                  <input
                                    className="qa-input qa-mono"
                                    value={repoUrl}
                                    onChange={(e) => setRepoUrl(e.target.value)}
                                    placeholder="https://github.com/owner/repo"
                                  />
                                </label>
                                <div className="qa-field">
                                  <span className="qa-label">默认分支</span>
                                  <HarnessSelect
                                    value={repoBranch}
                                    options={BRANCH_OPTIONS}
                                    onChange={setRepoBranch}
                                    searchable
                                    allowCustom
                                    searchPlaceholder="选择或输入分支名…"
                                    ariaLabel="默认分支"
                                  />
                                </div>
                              </div>
                              {projectMode === "existing" &&
                              projectRepos.length &&
                              parsedRepo ? (
                                <div className="qa-repo-default-row qa-msg-in">
                                  <span>
                                    已识别 <code>{parsedRepo.project}</code>
                                    ，同时设为项目默认仓库
                                  </span>
                                  <HarnessSwitch
                                    checked={makeDefaultRepo}
                                    onChange={setMakeDefaultRepo}
                                    label="设为项目默认仓库"
                                  />
                                </div>
                              ) : parsedRepo ? (
                                <p
                                  key="recognized"
                                  className="qa-hint is-ok qa-msg-in"
                                >
                                  已识别仓库：
                                  <code>{parsedRepo.project}</code>
                                  （仓库名 <code>{parsedRepo.name}</code>）
                                </p>
                              ) : (
                                <p key="help" className="qa-hint">
                                  支持 GitHub 链接或 Git SSH 地址，例如 <code>git@gitlab.com:group/repo.git</code>。
                                </p>
                              )}
                              {selectedSSHRepo?.provider === "ssh" && sshKeysLoaded && !sshReady ? (
                                <p className="qa-hint is-error" role="alert">
                                  {sshCredentials.error ? "无法读取 SSH 密钥配置，请稍后重试。" : `尚未配置 ${selectedSSHRepo.host} 的 SSH 密钥，可稍后在「个人配置 → Git SSH 密钥」添加。`}
                                </p>
                              ) : null}
                            </div>
                          </div>
                        </div>
                      </section>
                    </div>
                  ) : null}

                  {activeStep.id === "workflow" ? (
                    <div className="qa-pane">
                      <div className="qa-field">
                        <span className="qa-label">默认交付 SOP</span>
                        <HarnessSelect
                          value={activeWorkflow?.id || DEFAULT_SOP_ID}
                          options={sopSelectOptions}
                          onChange={setSelectedWorkflowId}
                          ariaLabel="默认交付 SOP"
                        />
                      </div>

                      <section className="qa-card qa-canvas">
                        <div className="qa-canvas-head">
                          <SlidingTabs
                            className="qa-sliding-tabs qa-sliding-tabs-inline"
                            aria-label="流程与角色预览"
                          >
                            <button
                              type="button"
                              role="tab"
                              aria-selected={previewTab === "stages"}
                              onClick={() => setPreviewTab("stages")}
                            >
                              <HarnessIcon name="workflow" size={14} />
                              阶段链路
                              <span className="sliding-tab-count">
                                {nodes.length}
                              </span>
                            </button>
                            <button
                              type="button"
                              role="tab"
                              aria-selected={previewTab === "roles"}
                              onClick={() => setPreviewTab("roles")}
                            >
                              <HarnessIcon name="user" size={14} />
                              默认角色
                              <span className="sliding-tab-count">
                                {roles.length}
                              </span>
                            </button>
                          </SlidingTabs>
                        </div>

                        {previewTab === "stages" ? (
                          <div
                            key="stages"
                            className="qa-flow-track qa-stagger"
                            aria-label="SOP 阶段预览"
                          >
                            <div
                              className="qa-flow-node is-edge"
                              style={{ "--i": 0 }}
                            >
                              <strong>开始</strong>
                              <span>需求输入</span>
                            </div>
                            {nodes.map((node, index) => (
                              <React.Fragment key={node.id || index}>
                                <span
                                  className="qa-flow-arrow"
                                  aria-hidden="true"
                                  style={{ "--i": index * 2 + 1 }}
                                >
                                  →
                                </span>
                                <div
                                  className="qa-flow-node"
                                  style={{ "--i": index * 2 + 2 }}
                                >
                                  <strong>{node.name}</strong>
                                  <span>阶段 {index + 1}</span>
                                </div>
                              </React.Fragment>
                            ))}
                            <span
                              className="qa-flow-arrow"
                              aria-hidden="true"
                              style={{ "--i": nodes.length * 2 + 1 }}
                            >
                              →
                            </span>
                            <div
                              className="qa-flow-node is-edge"
                              style={{ "--i": nodes.length * 2 + 2 }}
                            >
                              <strong>完成</strong>
                              <span>MR 与产物</span>
                            </div>
                          </div>
                        ) : (
                          <div key="roles" className="qa-role-chips qa-stagger">
                            {roles.map((role, index) => (
                              <span
                                key={role.id}
                                className="qa-role-chip"
                                style={{ "--i": index }}
                              >
                                <i>{(role.name || "角").slice(0, 1)}</i>
                                <b>{role.name}</b>
                                {role.description ? (
                                  <small>{role.description}</small>
                                ) : null}
                              </span>
                            ))}
                          </div>
                        )}
                      </section>

                      <section className="qa-card">
                        <div className="qa-card-title">将要生效的配置</div>
                        <dl className="qa-summary">
                          <div>
                            <dt>凭据</dt>
                            <dd>
                              <span
                                className={`qa-dot ${(sshRepository ? sshReady : gitReady) && (codeBuddyReady || sponsoredModel) ? "is-ok" : ""}`}
                              />
                              <span>
                                {sshRepository ? "Git SSH" : "GitHub"} {(sshRepository ? sshReady : gitReady) ? "已配置" : "未配置"} ·
                                CodeBuddy{" "}
                                {codeBuddyReady
                                  ? "已配置"
                                  : sponsoredModel
                                    ? "使用平台共享模型"
                                    : "未配置"}
                              </span>
                            </dd>
                          </div>
                          <div>
                            <dt>项目与仓库</dt>
                            <dd>
                              <span className="qa-dot is-ok" />
                              <span>
                                {projectMode === "create"
                                  ? `新建项目「${newProjectName || "新项目"}」`
                                  : `使用已有项目「${currentProject?.name || "当前项目"}」`}
                                {" · "}
                                {bindRepoNow && parsedRepo && !repoBindBlocked
                                  ? `绑定 ${parsedRepo.name}`
                                  : defaultRepo?.name || "运行时指定仓库"}
                              </span>
                            </dd>
                          </div>
                          <div>
                            <dt>默认 SOP</dt>
                            <dd>
                              <span className="qa-dot is-ok" />
                              <span>
                                {activeWorkflow?.name || "通用后端交付 SOP"}（
                                {roles.length} 个默认角色）
                              </span>
                            </dd>
                          </div>
                        </dl>
                      </section>
                    </div>
                  ) : null}
                </div>
              </div>

              <footer className="qa-footer">
                <button
                  type="button"
                  className="qa-btn qa-btn-ghost"
                  data-hover-ink-control=""
                  disabled={saving}
                  onClick={onClose}
                >
                  稍后再说
                </button>
                <div className="qa-footer-spacer" />
                {step > 0 ? (
                  <button
                    type="button"
                    className={`qa-btn qa-btn-outline ${stepAnimated && step === 1 && direction === "forward" ? "qa-back-enter" : ""}`}
                    data-hover-ink-control=""
                    disabled={saving}
                    onClick={() => {
                      setError("");
                      goToStep(Math.max(0, step - 1));
                    }}
                  >
                    <HarnessIcon name="chevron-left" size={14} />
                    上一步
                  </button>
                ) : null}
                <button
                  type="button"
                  className="qa-btn qa-btn-primary"
                  aria-busy={saving || undefined}
                  disabled={loading && !saving}
                  onClick={nextStep}
                >
                  <BusyLabel busy={saving} busyLabel="正在保存…">
                    {activeStep.id === "workflow" ? "完成接入" : "下一步"}
                    <HarnessIcon
                      name={
                        activeStep.id === "workflow"
                          ? "sparkles"
                          : "chevron-right"
                      }
                      size={14}
                    />
                  </BusyLabel>
                </button>
              </footer>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
