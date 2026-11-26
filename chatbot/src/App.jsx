import React, {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useChat } from "./useChat";
import NativeApprovalPanel from "./NativeApprovalPanel";
import { AgentBrowserLinks } from "./agent-browser-links.js";
import ActivityTrace, { ReplyTimeline } from "./ActivityTrace";
import MessageInfo from "./MessageInfo";
import GenerationDiagnostics from "./GenerationDiagnostics";
import { fullTime, messageSentAt, messageTimeLabel } from "./message-info.js";
import {
  activityForReply,
  finalReply,
  generationStored,
  hasTextSegments,
  streamingAnswerOpen,
} from "./activity";
import {
  api,
  attachmentURL,
  parseReply,
  collectArtifacts,
  visibleText,
  elapsedSeconds,
  formatElapsed,
  confirmationCaption,
  isProposalTimeout,
  canChatInProject,
  projectPickerOptions,
} from "./api";
import {
  RichContent,
  CopyButton,
  Attachment,
  ArtifactPreview,
  ImageLightbox,
} from "./RichContent";
import { DraftCard } from "./SidePanel";
import ArtifactPanel from "./ArtifactPanel";
import { useWorkspaceChanges } from "./useWorkspaceChanges";
import { localId } from "./local-id.js";
import RunAlerts from "./RunAlerts";
import { runProblems, useConversationRuns } from "./runs.js";
import { reconcileOperation, visibleSidebarArtifact } from "./platform.js";
import { isGeneratedArtifact, shouldRevealArtifactPanel } from "./artifact-autoreveal.js";
import { IconButton, Dialog, Menu, SelectMenu, CredentialAction } from "./ui";
import { CLIInstallAction } from "./cli-install.jsx";
import { credentialHint, openCredentialSettings } from "./credentials";
import { groupSessionHistory, scheduledSession, sessionProjectName } from "./session-history.js";
import { runSessionState, sessionReplyStatus, sessionTooltip, sidebarReplyStatus } from "./session-status.js";
import { unreadReply, useReadReplies } from "./session-unread.js";
import MentionPicker from "./MentionPicker";
import Participants, { Avatar, authorFor } from "./Participants";
import IssueContext from "./IssueContext";
import { mentionAt, insertMention } from "./mentions.js";
import { elementTitle } from "./element-context.js";
import HarnessIcon from "./HarnessIcon";
import QueuedMessages from "./QueuedMessages";
import "./message-queue.css";
import ConversationStatusIcon from "./ConversationStatusIcon";
import { SidebarSection } from "./SidebarPreview";
import {
  splitSidebarSections,
  PROJECT_PREVIEW_LIMIT,
  previewSessions,
} from "./session-sections.js";
import { useLocalConversations } from "./useLocalConversations.js";
import SessionContextMenu from "./SessionContextMenu";
import { usePanelPresence } from "./usePanelPresence";
import HomeWelcome from "./HomeWelcome";
import ModelPicker from "./ModelPicker";
import ModelProviderIcon, { hasProviderLogo } from "./ModelProviderIcon";
import PermissionPicker, { normalizeApprovalMode } from "./PermissionPicker";
import { useComposerOptions } from "./composer-preferences.js";
import { conversationEngine, engineBrand } from "./engines.js";
import { effortOptions } from "./effort.js";
import QuickAccessWizard from "./QuickAccessWizard";
import HarnessSelect from "./HarnessSelect";
import SandboxPicker from "./SandboxPicker";
import ExecutionMachinePicker from "./ExecutionMachinePicker";
import { shouldRevealTitle, titleRevealTiming } from "./title-reveal.js";
import {
  THEME_KEY,
  readThemePreference,
  resolveTheme,
  writeThemePreference,
} from "./theme.js";

const THEME_OPTIONS = [
  { value: "light", label: "Sáng" },
  { value: "dark", label: "Tối" },
  { value: "system", label: "Theo hệ thống" },
];
const environmentVisibilityKey = "agentflow.native-chat.environmentVisibility.v1";
const sessionApprovalScope = ({ id, projectId }) => `approval.session:${JSON.stringify([id, projectId || ""])}`;
const draftApprovalScope = (slot, projectId) =>
  slot.startsWith("issue:") ? `approval.${JSON.stringify([slot, projectId])}` : `approval.${slot}`;
const draftSnapshot = ({ text, attachments, references }) => JSON.stringify({ text, attachments, references });
const sidebarWidthKey = "agentflow.sidebarWidth";
const sidebarLayoutKey = "agentflow.sidebarLayoutVersion";
const sidebarLayoutVersion = "grasp-reference-v3";
function sidebarViewport() {
  try {
    // The workbench and settings share the host's desktop breakpoint.
    window.parent.matchMedia("(min-width:1001px)");
    return window.parent;
  } catch {
    return window;
  }
}
function readSidebarWidth() {
  // Also migrate standalone workbench loads, before the first React render.
  if (localStorage.getItem(sidebarLayoutKey) !== sidebarLayoutVersion) {
    localStorage.removeItem(sidebarWidthKey);
    localStorage.setItem(sidebarLayoutKey, sidebarLayoutVersion);
  }
  const saved = Number(localStorage.getItem(sidebarWidthKey));
  return sidebarViewport().matchMedia("(min-width:1001px)").matches && saved
    ? Math.round(Math.min(420, Math.max(220, saved)))
    : 304;
}
function useTitleReveal(sessionId, title, revealId) {
  const lastRevealedId = useRef(null);
  const [reveal, setReveal] = useState(null);
  // A layout effect swaps in the per-character spans before the first paint,
  // so the new title never flashes as plain text first.
  useLayoutEffect(() => {
    const reducedMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;
    const play = shouldRevealTitle({
      sessionId,
      revealId,
      lastRevealedId: lastRevealedId.current,
      reducedMotion,
    });
    // Leaving for the home page means the next arrival is a fresh opening.
    lastRevealedId.current = sessionId || null;
    if (!play) return;
    const timing = titleRevealTiming(title);
    setReveal({ id: sessionId, title, timing });
    const timer = setTimeout(() => setReveal(null), timing.total + 30);
    return () => {
      clearTimeout(timer);
      setReveal(null);
    };
  }, [sessionId, title, revealId]);
  return reveal && reveal.id === sessionId && reveal.title === title
    ? reveal.timing
    : null;
}

const homeStarters = [
  {
    id: "quick-access",
    icon: "sparkles",
    tone: "blue",
    title: "Thiết lập nhanh",
    description: "Cung cấp xác thực và repository tối thiểu, rồi dùng luôn vai trò mặc định và SOP.",
    steps: ["Xác thực", "Repository", "SOP mặc định"],
  },
  // New users asked how to hook up a test environment and how roles and SOPs
  // work; these two cards start a guided conversation instead of dropping them
  // on a settings page.
  {
    id: "environment",
    icon: "terminal",
    tone: "violet",
    title: "Kết nối môi trường kiểm thử",
    description: "Trợ lý sẽ kiểm tra máy thực thi, image và sandbox trước, rồi hướng dẫn bạn cấu hình từng phần còn thiếu.",
    steps: ["Máy thực thi", "Image", "Sandbox"],
    prompt:
      "Hãy hướng dẫn tôi kết nối môi trường kiểm thử từng bước: trước hết xem dự án hiện tại đã có những máy thực thi, image container, script môi trường và sandbox nào; cho tôi biết còn thiếu gì, nên cấu hình theo thứ tự nào, mỗi bước thao tác ở đâu; phần cấu hình bạn gửi hộ được thì hãy soạn sẵn cho tôi.",
  },
  {
    id: "sop",
    icon: "workflow",
    tone: "green",
    title: "Chọn SOP và vai trò",
    description: "Tìm hiểu ba bộ SOP bàn giao cho sản phẩm, frontend và backend cùng các vai trò trong đó, rồi được gợi ý một bộ phù hợp với yêu cầu.",
    steps: ["Sản phẩm", "Frontend", "Backend"],
    prompt:
      "Tôi chuẩn bị viết yêu cầu. Ba bộ SOP bàn giao có sẵn trên nền tảng (sản phẩm, frontend, backend) phù hợp với tình huống nào, mạnh ở điểm nào, và gồm những vai trò nào? Hãy căn cứ dự án hiện tại để gợi ý một bộ, rồi cho tôi biết cách gắn và giao yêu cầu đầu tiên. Yêu cầu của tôi là:",
  },
];

const readJSON = (key, fallback) => {
  try {
    return JSON.parse(localStorage.getItem(key)) ?? fallback;
  } catch {
    return fallback;
  }
};
function MessageTime({ messages, index }) {
  const date = messageSentAt(messages, index);
  if (!date) return null;
  return (
    <time
      className="message-time"
      dateTime={date.toISOString()}
      title={fullTime(date)}
    >
      {messageTimeLabel(date)}
    </time>
  );
}
function copiedReply(message, generation, live) {
  const steps = activityForReply(message, generation, live);
  const text = parseReply(message).message;
  return hasTextSegments(steps) ? finalReply(text, steps) : text;
}
function ReplyHeading({ message, steps }) {
  return (
    <div className="reply-heading">
      <ActivityTrace
        steps={steps}
        startedAt={message.startedAt}
        finishedAt={message.createdAt}
        interrupted={!!message.unfinished}
      />
    </div>
  );
}
function AssistantTurn({ message, steps, assets, projectId, sessionId, local, readOnly, onCLIInstall }) {
  const reply = parseReply(message);
  const answer = hasTextSegments(steps)
    ? finalReply(reply.message, steps)
    : message;
  return (
    <>
      {hasTextSegments(steps) ? (
        <ReplyTimeline
          steps={steps}
          startedAt={message.startedAt}
          finishedAt={message.createdAt}
        />
      ) : (
        <ReplyHeading message={message} steps={steps} />
      )}
      <div className="message-body">
        <RichContent message={answer} assets={assets} projectId={projectId} />
      </div>
      {message.failure && message.generationId && !local ? (
        <GenerationDiagnostics sessionId={sessionId} generationId={message.generationId} failure={message.failure} onCLIInstall={onCLIInstall} readOnly={readOnly} />
      ) : message.unfinished ? (
        <p className="reply-stopped">Lượt này chưa xong · {message.unfinished}</p>
      ) : reply.stopped ? (
        <p className="reply-stopped">Đã dừng; đây là nội dung trước khi dừng.</p>
      ) : null}
    </>
  );
}
function LiveMessage({ live, generation, finished = false, local = false, readOnly = false }) {
  const steps = live.activity || [];
  const textual = hasTextSegments(steps);
  // A streamed paragraph followed by tool calls stays in the process above;
  // only the paragraph still being written shows as the answer.
  const answer =
    live.draftMessage ||
    (textual
      ? finished || streamingAnswerOpen(steps)
        ? finalReply(live.answer, steps)
        : ""
      : live.answer);
  return (
    <div
      className={`message assistant ${finished ? "interrupted" : "live"}`}
      aria-live="polite"
    >
      {textual ? (
        <ReplyTimeline
          steps={steps}
          startedAt={generation?.startedAt}
          finishedAt={generation?.updatedAt}
          live={!finished}
          interrupted={finished}
        />
      ) : (
        <div className="reply-heading">
          <ActivityTrace
            steps={steps}
            startedAt={generation?.startedAt}
            finishedAt={generation?.updatedAt}
            live={!finished}
            interrupted={finished}
          />
        </div>
      )}
      {answer ? <RichContent message={answer} /> : null}
      {!finished && !local && !readOnly && generation?.id ? <GenerationDiagnostics sessionId={generation.sessionId} generationId={generation.id} compact /> : null}
      {finished ? (
        <p className="reply-stopped">
          {generation?.status === "failed" && generation?.error
            ? generation.error
            : "Đã dừng; đây là nội dung trước khi dừng."}
        </p>
      ) : null}
    </div>
  );
}

function readIssueMention() {
  const params = new URLSearchParams(location.search);
  const issueId = params.get("mentionIssue") || "";
  return issueId ? { issueId, projectId: params.get("projectId") || "" } : null;
}

export default function App() {
  const realChat = useChat();
  const chat = useLocalConversations(realChat);
  const sessionTitle = chat.session?.displayTitle || chat.session?.title || "Trò chuyện mới";
  const titleReveal = useTitleReveal(
    chat.session?.id,
    sessionTitle,
    chat.revealId,
  );
  const [sidebar, setSidebar] = useState(
      () => sidebarViewport().matchMedia("(min-width:1001px)").matches,
    );
  const [panel, setPanel] = useState(false),
    [panelView, setPanelView] = useState("artifacts"),
    [artifactUnread, setArtifactUnread] = useState(0),
    [sidebarWidth, setSidebarWidth] = useState(readSidebarWidth),
    [selectedArtifact, setSelectedArtifact] = useState(null),
    [menu, setMenu] = useState(null),
    [sessionContext, setSessionContext] = useState(null),
    [dialog, setDialog] = useState(null),
    [issueContextOpen, setIssueContextOpen] = useState(false),
    [query, setQuery] = useState(""),
    [archived, setArchived] = useState(false),
    [mineOnly, setMineOnly] = useState(false),
    [collapsedProjects, setCollapsedProjects] = useState({}),
    [expandedProjects, setExpandedProjects] = useState({}),
    [pinnedOpen, setPinnedOpen] = useState(true),
    [pins, setPins] = useState({}),
    [text, setText] = useState(""),
    [attachments, setAttachments] = useState([]),
    [pendingAttachments, setPendingAttachments] = useState([]),
    [imagePreview, setImagePreview] = useState(null),
    [references, setReferences] = useState([]),
    [issueMention, setIssueMention] = useState(readIssueMention),
    [issueDraft, setIssueDraft] = useState(readIssueMention),
    [mention, setMention] = useState(null),
    [repositorySelections, setRepositorySelections] = useState({}),
    [uploading, setUploading] = useState(false),
    [runsRefresh, setRunsRefresh] = useState(0),
    [quickAccessOpen, setQuickAccessOpen] = useState(
      () => new URLSearchParams(location.search).get("quickAccess") === "1",
    ),
    [theme, setTheme] = useState(() => readThemePreference(localStorage)),
    [environmentVisibility, setEnvironmentVisibility] = useState(() => readJSON(environmentVisibilityKey, {}));
  const environmentPreferenceOwner = String(chat.user?.id || "guest");
  const showEnvironmentPicker = environmentVisibility[environmentPreferenceOwner] === true;
  function changeEnvironmentVisibility(visible) {
    const next = { ...readJSON(environmentVisibilityKey, {}), [environmentPreferenceOwner]: visible };
    setEnvironmentVisibility(next);
    try { localStorage.setItem(environmentVisibilityKey, JSON.stringify(next)); } catch {}
  }
  function startNewChat() {
    setIssueDraft(null);
    setIssueMention(null);
    chat.newChat();
  }
  const input = useRef(),
    sessionPress = useRef(null),
    sessionClickSuppressed = useRef(null),
    mentionPicker = useRef(),
    projectPicker = useRef(null),
    headerProjectPicker = useRef(null),
    fileInput = useRef(),
    scroll = useRef(),
    follow = useRef(true),
    draftReady = useRef(null),
    draftOrigin = useRef(null),
    savingDraftApproval = useRef(null),
    previousArtifacts = useRef({ id: null, ids: new Set(), baselined: false }),
    dialogInput = useRef(),
    composerPreviews = useRef(new Map());
  // A requirement handed over by the workbench drafts in its own slot so the
  // project's unsent new-conversation draft and attachments stay untouched.
  const newDraftSlot = issueDraft && issueDraft.projectId === chat.projectId ? "issue:" + issueDraft.issueId : "new:" + chat.projectId;
  const draftKeyPrefix = `agentflow.native-chat.draft.${chat.user?.id || "guest"}.`;
  const draftKey = draftKeyPrefix + (chat.activeId || newDraftSlot);
  const visibleDraft = useRef(null);
  visibleDraft.current = { key: draftKey, text, attachments, references };
  const approvalOwner = realChat.user?.isGuest ? "guest" : realChat.user?.id;
  const { readOptions: readApproval, saveOptions: saveApproval } = useComposerOptions(approvalOwner);
  const approvalScope = chat.activeId
    ? chat.session?.id === chat.activeId && sessionApprovalScope(chat.session)
    : draftApprovalScope(newDraftSlot, chat.projectId);
  const approval = normalizeApprovalMode(approvalScope && readApproval(approvalScope).mode);
  function setApproval(value) {
    if (!approvalScope) return;
    const mode = normalizeApprovalMode(value);
    const saving = savingDraftApproval.current;
    if (saving?.scope === approvalScope && saving.owner === approvalOwner &&
        draftSnapshot(visibleDraft.current) === saving.snapshot) saving.mode = mode;
    saveApproval(approvalScope, { mode });
  }
  function clearDraftApproval(scope, mode) {
    if (readApproval(scope).mode === mode) saveApproval(scope, null);
  }
  function persistDraft(key, draft) {
    try {
      localStorage.setItem(key, JSON.stringify(draft));
    } catch {
      chat.setError("Bộ nhớ trình duyệt không đủ; bản nháp vẫn còn trên trang này. Hãy gỡ bớt tệp đính kèm lớn rồi thử lại.");
    }
  }
  function clearDraftIfUnchanged(key, snapshot) {
    const visible = visibleDraft.current;
    const inComposer = visible.key === key && draftReady.current === key;
    const stored = readJSON(key, null);
    if ((inComposer && draftSnapshot(visible) !== snapshot) ||
        (stored && draftSnapshot(stored) !== snapshot)) return false;
    try { localStorage.removeItem(key); } catch {}
    if (inComposer) {
      draftOrigin.current = null;
      setMention(null);
      setText("");
      setAttachments([]);
      setReferences([]);
      if (input.current) input.current.style.height = "";
    }
    return true;
  }
  useEffect(() => {
    if (!chat.activeId) return;
    setIssueDraft(null);
    setIssueMention(null);
  }, [chat.activeId]);
  useEffect(() => setMention(null), [draftKey]);
  // The open conversation's reply is reported read; the history list dots
  // the replies the server still counts as unread.
  const readReplies = useReadReplies({
    enabled: realChat.ready,
    sessions: realChat.sessions,
    activeId: realChat.activeId,
    generation: realChat.generation,
  });
  const conversationRuns = useConversationRuns(chat.session, runsRefresh);
  // The workspace of this conversation (opened by workbench_workspace_open).
  // Its git state drives the "改动" view of the side panel.
  // Task conversations keep their workspace on the task run; ordinary
  // conversations bind the temporary workspace opened by Main Bot.
  const workspaceRunId =
    chat.session?.workspaceRunId || chat.session?.runId || "";
  const workspaceChanges = useWorkspaceChanges(workspaceRunId, {
    active: panel && panelView === "changes",
  });
  // A finished turn has changed the workspace; refresh the badge and the diff
  // so the panel never shows the state from before the last edits.
  const generationStatus = chat.generation?.status || "";
  const refreshWorkspaceChanges = workspaceChanges.refresh;
  useEffect(() => {
    if (workspaceRunId && generationStatus && generationStatus !== "running")
      refreshWorkspaceChanges();
  }, [workspaceRunId, generationStatus, refreshWorkspaceChanges]);
  const runAlertKeys = conversationRuns
    .flatMap((run) => runProblems(run).map((p) => p.key))
    .join(",");
  const artifactList = useMemo(
    () =>
      collectArtifacts(chat.session).map((a) =>
        reconcileOperation(a, conversationRuns),
      ),
    [chat.session, conversationRuns],
  );
  const sidebarArtifacts = artifactList.filter(visibleSidebarArtifact);
  useEffect(() => setRepositorySelections({}), [chat.activeId]);
  useEffect(() => setPanelView("artifacts"), [chat.activeId]);
  // Workspace and localhost addresses in replies open in the side panel's
  // Agent browser: the user's own browser cannot reach the execution machine.
  const [browserRequest, setBrowserRequest] = useState(null);
  useEffect(() => setBrowserRequest(null), [chat.activeId]);
  const openAgentBrowser = useCallback((url) => {
    setBrowserRequest({ id: `${Date.now()}-${Math.random()}`, url });
    setPanelView("browser");
    setPanel(true);
  }, []);
  const browserRequestHandled = useCallback(
    (id) => setBrowserRequest((current) => (current?.id === id ? null : current)),
    [],
  );
  useEffect(() => setIssueContextOpen(false), [chat.activeId, chat.projectId]);
  const operationContext = {
    projects: chat.projects,
    projectId: chat.projectId,
    issueId: chat.issueId,
    runId: chat.session?.runId,
    onLinkIssue: chat.linkIssue,
    references:
      chat.session?.messages?.flatMap((message) => message.references || []) ||
      [],
    repositorySelections,
    onRepositorySelection: (id, ids, projectId) =>
      setRepositorySelections((old) => ({ ...old, [id]: { ids, projectId } })),
  };
  const pinned =
    pins[chat.user?.id] ||
    readJSON(`agentflow.native-chat.pins.${chat.user?.id}`, []);
  const projectName =
    chat.projects.find((p) => p.id === chat.projectId)?.name ||
    (chat.projectId && (chat.session?.projectName || chat.projectId)) ||
    "Hội thoại cá nhân";
  const sessionLoading = !!chat.activeId && chat.session?.id !== chat.activeId;
  const composerLoading = sessionLoading || chat.replyStatePending;
  const selectedModel = chat.models.find((m) => m.id === chat.model) || (!chat.local ? chat.modelDetails : null);
  const modelLabel =
    (selectedModel?.name || chat.model)?.replace(/^Claude-/, "").replace(/^GPT-/, "") || "Model";
  // The CLI switch is only for a conversation that has not started. A reply
  // in flight, or one already saved, keeps that CLI; another CLI needs a new chat.
  const lockedExecutorId = chat.session
    ? conversationEngine(chat.session.messages, chat.models, chat.session.executor) ||
      (chat.busy ? chat.executor : "")
    : "";
  const pinnedExecutor = lockedExecutorId
    ? (chat.executors || []).find((item) => item.id === lockedExecutorId)
    : undefined;
  const projectOptions = projectPickerOptions(
    chat.projects,
    chat.user,
    chat.session,
  );
  const projectActions = [
    {
      id: "new-project",
      label: "Tạo dự án",
      icon: "plus",
      onSelect: () => setQuickAccessOpen(true),
    },
  ];
  function chooseProject(id) {
    const project = chat.projects.find((p) => p.id === id);
    if (project && !canChatInProject(project, chat.user)) {
      chat.setError(
        `Bạn chưa phải thành viên của “${project.name}” nên chưa trò chuyện được trong dự án này. Hãy nhờ Owner thêm bạn vào dự án, hoặc dùng “Hội thoại cá nhân” trước.`,
      );
      return;
    }
    void chat.selectProject(id);
  }
  function useStarter(prompt) {
    setText(prompt);
    setMention(null);
    requestAnimationFrame(() => {
      input.current?.focus();
      if (input.current) {
        input.current.style.height = "auto";
        input.current.style.height = `${Math.min(input.current.scrollHeight, 220)}px`;
      }
    });
  }
  function handleStarter(starter) {
    if (starter.id === "quick-access") {
      setQuickAccessOpen(true);
      return;
    }
    if (starter.view) {
      navigate(starter.view);
      return;
    }
    if (starter.prompt) useStarter(starter.prompt);
  }
  useEffect(() => {
    const wide = sidebarViewport().matchMedia("(min-width:1001px)");
    const apply = () => setSidebarWidth(readSidebarWidth());
    const onStorage = (event) => {
      if (
        !event.key ||
        event.key === sidebarWidthKey ||
        event.key === sidebarLayoutKey
      ) apply();
    };
    apply();
    wide.addEventListener("change", apply);
    window.addEventListener("storage", onStorage);
    return () => {
      wide.removeEventListener("change", apply);
      window.removeEventListener("storage", onStorage);
    };
  }, []);
  useEffect(() => {
    const media = matchMedia("(prefers-color-scheme: dark)");
    const apply = () => {
      document.documentElement.dataset.theme = resolveTheme(
        theme,
        media.matches,
      );
    };
    apply();
    writeThemePreference(localStorage, theme);
    media.addEventListener("change", apply);
    return () => media.removeEventListener("change", apply);
  }, [theme]);
  useEffect(() => {
    // The host shell writes the same key; storage events only reach the other documents.
    const onStorage = (event) => {
      if (!event.key || event.key === THEME_KEY)
        setTheme(readThemePreference(localStorage));
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);
  useEffect(() => {
    if (!chat.user) return;
    const value = readJSON(draftKey, {});
    setText(value.text || "");
    setAttachments(value.attachments || []);
    setReferences(value.references || []);
    draftOrigin.current = value.copiedFrom || null;
    draftReady.current = draftKey;
  }, [draftKey, chat.user?.id]);
  useEffect(() => {
    if (!chat.user || draftReady.current !== draftKey) return;
    persistDraft(draftKey, {
      text, attachments, references,
      ...(draftOrigin.current && { copiedFrom: draftOrigin.current }),
    });
  }, [text, attachments, references, draftKey, chat.user?.id]);
  // A requirement handed over by the workbench ("创建并去对话澄清") opens a
  // new conversation with the requirement already @-mentioned. It waits for
  // the new conversation's draft to load so the draft does not overwrite it.
  useEffect(() => {
    const listener = (event) => {
      if (
        event.source !== window.parent ||
        event.origin !== location.origin ||
        event.data?.type !== "agentflow-chat-mention-issue"
      ) return;
      const issueId = String(event.data.issueId || "");
      const projectId = String(event.data.projectId || "");
      if (!issueId || !projectId) return;
      chat.startProjectDraft(projectId);
      setIssueDraft({ issueId, projectId });
      setIssueMention({ issueId, projectId });
    };
    window.addEventListener("message", listener);
    return () => window.removeEventListener("message", listener);
  }, [chat.startProjectDraft]);
  useEffect(() => {
    if (
      !issueMention ||
      !chat.user ||
      chat.activeId ||
      chat.projectId !== issueMention.projectId ||
      newDraftSlot !== "issue:" + issueMention.issueId ||
      draftReady.current !== draftKey
    ) return;
    let current = true;
    api(`/api/issues/${encodeURIComponent(issueMention.issueId)}`)
      .then((data) => {
        if (!current) return;
        const issue = data.issue || data;
        if ((readJSON(draftKey, {}).references || []).some((r) => r.type === "issue" && r.id === issue.id)) return;
        const ref = {
          type: "issue",
          id: issue.id,
          name: issue.title || issue.id,
          projectId: issue.projectId,
        };
        const value = insertMention("", { start: 0, end: 0 }, ref);
        setText(value.text);
        setReferences([ref]);
        setMention(null);
        requestAnimationFrame(() => {
          input.current?.focus();
          input.current?.setSelectionRange(value.caret, value.caret);
        });
      })
      .catch((e) => current && chat.setError(e.message))
      .finally(() => {
        if (!current) return;
        setIssueMention(null);
        const url = new URL(location.href);
        url.searchParams.delete("mentionIssue");
        history.replaceState({}, "", url);
      });
    return () => {
      current = false;
    };
  }, [issueMention, chat.user, chat.activeId, chat.projectId, newDraftSlot, draftKey]);
  useEffect(() => {
    let previous = previousArtifacts.current;
    if (previous.id !== chat.activeId) {
      setSelectedArtifact(null);
      setPanel(false);
      setArtifactUnread(0);
      previous = { id: chat.activeId, ids: new Set(), baselined: false };
      previousArtifacts.current = previous;
    }
    if (!chat.activeId || chat.session?.id !== chat.activeId) return;
    const entries = [...sidebarArtifacts, ...conversationRuns];
    const addedEntries = previous.baselined
      ? entries.filter((entry) => !previous.ids.has(entry.id)) : [];
    const addedArtifacts = previous.baselined
      ? sidebarArtifacts.filter(
          (artifact) => isGeneratedArtifact(artifact) && !previous.ids.has(artifact.id),
        ).length
      : 0;
    previousArtifacts.current = {
      id: chat.activeId,
      ids: new Set(entries.map((entry) => entry.id)),
      baselined: true,
    };
    if (panel) {
      setArtifactUnread(0);
      return;
    }
    if (addedEntries.length)
      setArtifactUnread((count) => count + addedEntries.length);
    // A newly generated artifact reveals the drawer by itself so the user does
    // not have to notice the badge. Restored artifacts (the first snapshot of
    // an existing conversation), conversations without artifacts and layouts
    // where the drawer would cover the answer keep the drawer as it was.
    const revealArtifacts = shouldRevealArtifactPanel({
      addedArtifacts,
      panelOpen: panel,
      viewportWidth: window.innerWidth || document.documentElement.clientWidth,
      sidebarOpen: sidebar,
      sidebarWidth,
    });
    if (revealArtifacts) {
      setArtifactUnread(0);
      setPanelView("artifacts");
      setPanel(true);
    }
  }, [
    chat.activeId,
    chat.session?.id,
    artifactList,
    conversationRuns,
    panel,
    sidebar,
    sidebarWidth,
  ]);
  useEffect(() => {
    if (follow.current && scroll.current)
      scroll.current.scrollTop = scroll.current.scrollHeight;
  }, [
    chat.session?.messages?.length,
    chat.live.answer,
    chat.live.thinking,
    chat.busy,
    runAlertKeys,
  ]);
  useEffect(() => {
    const keyboard = (e) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setDialog({ type: "search" });
        setQuery("");
      }
      if (
        (e.ctrlKey || e.metaKey) &&
        e.shiftKey &&
        e.key.toLowerCase() === "o"
      ) {
        e.preventDefault();
        startNewChat();
      }
      if ((e.ctrlKey || e.metaKey) && e.key === "b") {
        e.preventDefault();
        setSidebar((v) => !v);
      }
      if ((e.ctrlKey || e.metaKey) && e.key === ",") {
        e.preventDefault();
        setDialog({ type: "settings" });
      }
      if (e.key === "Escape") {
        setMenu(null);
      }
    };
    window.addEventListener("keydown", keyboard);
    return () => window.removeEventListener("keydown", keyboard);
  }, [chat.newChat]);
  const closeMenu = () => setMenu(null),
    closeDialog = () => {
      if (dialog?.type === "search") setQuery("");
      setDialog(null);
    };
  function togglePin(id) {
    const updated = pinned.includes(id)
      ? pinned.filter((x) => x !== id)
      : [...pinned, id];
    localStorage.setItem(
      `agentflow.native-chat.pins.${chat.user.id}`,
      JSON.stringify(updated),
    );
    setPins({ ...pins, [chat.user.id]: updated });
    closeMenu();
  }
  function openArtifact(a) {
    setArtifactUnread(0);
    setPanelView("artifacts");
    setPanel(true);
    setSelectedArtifact(a);
  }
  function navigate(view, options = {}) {
    if (window.parent !== window)
      window.parent.postMessage(
        { type: "agentflow-chat-open-view", view, ...options },
        location.origin,
      );
    else location.href = `/${options.projectId ? "?projectId=" + encodeURIComponent(options.projectId) : ""}#/${view}${options.action === "create" ? "/__new__" : ""}`;
    closeDialog();
    closeMenu();
  }
  async function submit() {
    if (uploading || composerLoading || draftReady.current !== draftKey ||
        (!chat.activeId && savingDraftApproval.current)) return;
    follow.current = true;
    const snapshot = draftSnapshot({ text, attachments, references });
    const draftApproval = chat.activeId ? null : { owner: approvalOwner, scope: approvalScope, mode: approval, snapshot };
    const copiedFrom = draftOrigin.current;
    if (draftApproval) savingDraftApproval.current = draftApproval;
    let savedDraftKey, savedApprovalMode;
    const onSessionCreated = draftApproval ? (saved) => {
      savedDraftKey = draftKeyPrefix + saved.id;
      savedApprovalMode = draftApproval.mode;
      saveApproval(sessionApprovalScope(saved), { mode: savedApprovalMode });
    } : undefined;
    const ok = await chat.send({
      text,
      attachments,
      references,
      approvalMode: approval,
      onSessionCreated,
      isDraftActive: draftApproval ? () => visibleDraft.current.key === draftKey &&
        draftSnapshot(visibleDraft.current) === snapshot : undefined,
    }).finally(() => {
      if (savingDraftApproval.current === draftApproval) savingDraftApproval.current = null;
    });
    const visible = visibleDraft.current;
    if (ok) {
      const clearedDraft = clearDraftIfUnchanged(draftKey, snapshot);
      if (clearedDraft && savedDraftKey) clearDraftApproval(draftApproval.scope, savedApprovalMode);
      let clearedVisible = clearedDraft && visible.key === draftKey;
      if (copiedFrom?.snapshot === snapshot && /^(new|issue):/.test(copiedFrom.slot)) {
        const originKey = draftKeyPrefix + copiedFrom.slot;
        if (clearDraftIfUnchanged(originKey, snapshot)) {
          if (copiedFrom.approvalScope) clearDraftApproval(copiedFrom.approvalScope, copiedFrom.mode);
          clearedVisible ||= visible.key === originKey;
        }
      }
      if (!clearedDraft && copiedFrom) {
        // A newer draft can share this slot. Retire the consumed copy's
        // linkage while keeping the newer draft itself.
        const origin = JSON.stringify(copiedFrom);
        if (visible.key === draftKey && JSON.stringify(draftOrigin.current) === origin)
          draftOrigin.current = null;
        const stored = readJSON(draftKey, null);
        if (stored && JSON.stringify(stored.copiedFrom) === origin) {
          const { text, attachments, references } = stored;
          persistDraft(draftKey, { text, attachments, references });
        }
      }
      for (const attachment of attachments) {
        if (clearedVisible || !visible.attachments.some((current) => current.id === attachment.id))
          releaseComposerPreview(attachment.id);
      }
    } else if (visible.key === savedDraftKey && draftReady.current === savedDraftKey &&
        !visible.text && !visible.attachments.length && !visible.references.length) {
      // The session was saved but the first message was rejected. Keep its
      // content in the visible conversation so the user can retry.
      draftOrigin.current = {
        slot: newDraftSlot,
        approvalScope: draftApproval.scope,
        mode: savedApprovalMode,
        snapshot,
      };
      setText(text);
      setAttachments(attachments);
      setReferences(references);
    }
  }
  function editQueued(item) {
    chat.removeQueued(item.id);
    setText((current) =>
      current.trim() ? `${item.text}\n${current}` : item.text,
    );
    setAttachments((old) => [...(item.attachments || []), ...old]);
    setReferences((old) => [
      ...(item.references || []).filter(
        (ref) => !old.some((o) => o.type === ref.type && o.id === ref.id),
      ),
      ...old,
    ]);
    input.current?.focus();
  }
  function composerPreviewURL(a) {
    return (
      composerPreviews.current.get(a.id) ||
      attachmentURL(a, chat.session?.id || chat.activeId || "")
    );
  }
  function releaseComposerPreview(id) {
    const url = composerPreviews.current.get(id);
    if (!url) return;
    URL.revokeObjectURL(url);
    composerPreviews.current.delete(id);
  }
  function removeAttachment(a) {
    releaseComposerPreview(a.id);
    setAttachments((old) => old.filter((x) => x.id !== a.id));
  }
  async function upload(files) {
    if (!files.length) return;
    if (files.length + attachments.length > 8) {
      chat.setError("Mỗi lượt chỉ tải lên tối đa 8 tệp.");
      return;
    }
    setUploading(true);
    try {
      const results = [];
      for (const file of files) {
        if (file.size > 10 * 1024 * 1024) throw Error("Mỗi tệp đính kèm không được quá 10 MB");
        // Show the picture as soon as it is pasted or dropped. A filename
        // chip alone leaves people unable to tell which screenshot is queued.
        const previewURL =
          file.type?.startsWith("image/") && !chat.local
            ? URL.createObjectURL(file)
            : "";
        const slot = previewURL
          ? {
              id: localId("pending"),
              name: file.name || "Ảnh đã dán",
              mimeType: file.type,
              size: file.size,
              previewURL,
            }
          : null;
        if (slot) setPendingAttachments((old) => [...old, slot]);
        try {
          if (chat.local) {
            const localDataUrl = await new Promise((resolve, reject) => {
              const reader = new FileReader();
              reader.onload = () => resolve(reader.result);
              reader.onerror = () =>
                reject(Error("Không đọc được tệp, hãy chọn lại."));
              reader.readAsDataURL(file);
            });
            results.push({
              id: localId("local-attachment"),
              name: file.name,
              mimeType: file.type || "application/octet-stream",
              size: file.size,
              localDataUrl,
            });
            continue;
          }
          const body = new FormData();
          body.append("file", file);
          body.append("projectId", chat.projectId);
          const uploaded = await api("/api/assistant/attachments", {
            method: "POST",
            body,
          });
          if (previewURL) composerPreviews.current.set(uploaded.id, previewURL);
          results.push(uploaded);
        } catch (err) {
          if (previewURL) URL.revokeObjectURL(previewURL);
          throw err;
        } finally {
          if (slot)
            setPendingAttachments((old) =>
              old.filter((x) => x.id !== slot.id),
            );
        }
      }
      setAttachments((old) => [...old, ...results]);
    } catch (e) {
      chat.setError(e.message);
    } finally {
      setUploading(false);
    }
  }
  function openReferences() {
    const caret = input.current?.selectionStart ?? text.length;
    setMention(
      mentionAt(text, caret, references) || {
        start: caret,
        end: caret,
        query: "",
      },
    );
    closeMenu();
    input.current?.focus();
  }
  // addElementReference takes an element picked in the native preview: it
  // rides along as a reference chip, without a token in the text.
  function addElementReference(ref) {
    if (references.length >= 12) {
      chat.setError("Mỗi lượt chỉ trích dẫn tối đa 12 đối tượng.");
      return;
    }
    setReferences((old) => [...old, ref]);
    requestAnimationFrame(() => input.current?.focus());
  }
  function addReference(ref) {
    if (
      references.length >= 12 &&
      !references.some((x) => x.type === ref.type && x.id === ref.id)
    ) {
      chat.setError("Mỗi lượt chỉ trích dẫn tối đa 12 đối tượng.");
      return;
    }
    const value = insertMention(text, mention, ref);
    setText(value.text);
    setReferences((old) =>
      old.some((x) => x.type === ref.type && x.id === ref.id)
        ? old
        : [...old, ref],
    );
    setMention(null);
    requestAnimationFrame(() => {
      input.current?.focus();
      input.current?.setSelectionRange(value.caret, value.caret);
    });
  }
  const sidebarSections = splitSidebarSections(chat.sessions, chat.projects, {
    archived,
    mineOnly,
    currentUserId: chat.user?.id,
    query,
    pinned,
    activeId: chat.activeId,
    hideIdleRuns: true,
  });
  const searchGroups = groupSessionHistory(chat.sessions, chat.projects, {
    query,
    pinned,
  });
  useEffect(() => {
    // A search result or project switch should reveal the selected conversation.
    setCollapsedProjects((previous) => ({
      ...previous,
      [chat.projectId]: false,
    }));
  }, [chat.activeId, chat.projectId]);
  const messages = chat.session?.messages || [];
  const conversationStarted = Boolean(
    chat.activeId && chat.session?.id === chat.activeId &&
    (messages.length || chat.busy || sidebarArtifacts.length || conversationRuns.length),
  );
  const artifactPanelVisible = panel && conversationStarted;
  useEffect(() => {
    if (conversationStarted)
      setMenu((current) => (current?.type === "host" || current?.type === "project" ? null : current));
  }, [conversationStarted]);
  const sidebarPresence = usePanelPresence(sidebar);
  const artifactPresence = usePanelPresence(artifactPanelVisible);
  const previousSidebar = useRef(sidebar);
  useEffect(() => {
    if (previousSidebar.current && !sidebar) {
      const navigation = document.getElementById("conversation-navigation");
      if (navigation?.contains(document.activeElement) || document.activeElement === document.body)
        document.querySelector(".sidebar-toggle")?.focus({ preventScroll: true });
      setMenu(null);
      setSessionContext(null);
    }
    previousSidebar.current = sidebar;
  }, [sidebar]);
  function cancelSessionPress() {
    if (sessionPress.current) window.clearTimeout(sessionPress.current.timer);
    sessionPress.current = null;
  }
  useEffect(() => cancelSessionPress, []);
  function openSessionContext(event, session, keyboard = false) {
    event.preventDefault();
    event.stopPropagation();
    cancelSessionPress();
    if (["touch", "pen"].includes(event.nativeEvent?.pointerType))
      sessionClickSuppressed.current = { id: session.id, until: Date.now() + 1200 };
    const anchor = event.currentTarget.matches("button")
      ? event.currentTarget
      : event.currentTarget.querySelector(".session-link");
    const bounds = anchor.getBoundingClientRect();
    setMenu(null);
    setSessionContext({
      id: session.id,
      key: `${session.id}:${performance.now()}`,
      anchor,
      x: keyboard ? bounds.left + 16 : event.clientX,
      y: keyboard ? bounds.bottom + 4 : event.clientY,
    });
  }
  function startSessionPress(event, session) {
    if (!["touch", "pen"].includes(event.pointerType)) return;
    cancelSessionPress();
    const anchor = event.currentTarget;
    const x = event.clientX;
    const y = event.clientY;
    sessionPress.current = {
      x, y,
      timer: window.setTimeout(() => {
        sessionClickSuppressed.current = { id: session.id, until: Date.now() + 1200 };
        setMenu(null);
        setSessionContext({ id: session.id, key: `${session.id}:${performance.now()}`, anchor, x, y });
        sessionPress.current = null;
      }, 550),
    };
  }
  const renderSession = (s, options = {}) => {
    const status = sidebarReplyStatus(
      sessionReplyStatus(s, s.id === chat.activeId ? {
        generation: chat.generation,
        busy: chat.busy,
      } : undefined),
      unreadReply(s, { activeId: chat.activeId, read: readReplies }),
    );
    const runState = runSessionState(s);
    return (
    <div
      key={s.id}
      className={`session-row ${s.id === chat.activeId ? "active" : ""}`}
      onContextMenu={(event) => openSessionContext(event, s)}
    >
      <button
        type="button"
        className="session-link"
        title={sessionTooltip(s, { projectName: options.projectName, replyLabel: status.label })}
        aria-haspopup="menu"
        aria-expanded={sessionContext?.id === s.id}
        aria-controls={sessionContext?.id === s.id ? "session-context-menu" : undefined}
        onKeyDown={(event) => {
          if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10"))
            openSessionContext(event, s, true);
        }}
        onPointerDown={(event) => startSessionPress(event, s)}
        onPointerMove={(event) => {
          const press = sessionPress.current;
          if (press && Math.hypot(event.clientX - press.x, event.clientY - press.y) > 10)
            cancelSessionPress();
        }}
        onPointerUp={cancelSessionPress}
        onPointerCancel={cancelSessionPress}
        onPointerLeave={cancelSessionPress}
        onClick={(event) => {
          const suppressed = sessionClickSuppressed.current;
          if (suppressed?.id === s.id && Date.now() < suppressed.until) {
            event.preventDefault();
            return;
          }
          setSessionContext(null);
          chat.open(s.id);
          setQuery("");
          if (innerWidth < 850) setSidebar(false);
        }}
      >
        <span>{s.displayTitle || s.title || "Trò chuyện mới"}</span>
        {scheduledSession(s) ? (
          <span className="session-project-tag session-source-tag" title="Phiên do tác vụ định kỳ tạo ra">
            Định kỳ
          </span>
        ) : null}
        {options.projectName ? (
          <span className="session-project-tag">{options.projectName}</span>
        ) : null}
        {runState ? (
          <span
            className="session-run"
            data-run-state={runState.state}
            title={`Trạng thái chạy: ${runState.label}`}
          >
            {runState.label}
          </span>
        ) : null}
        <Participants session={s} users={chat.users} compact />
        {pinned.includes(s.id) ? (
          <HarnessIcon name="pin" size={12} className="pin-mark" />
        ) : null}
        {status.icon || status.dot ? (
          <span className="session-status" data-session-status={status.state} role="img" aria-label={status.label} title={status.label}>
            {status.icon ? (
              <HarnessIcon name={status.icon} size={15} motion={status.busy ? "busy" : "idle"} />
            ) : (
              <span className="session-unread-dot" aria-hidden="true" />
            )}
          </span>
        ) : null}
      </button>
      {sessionContext?.id === s.id ? (
        <SessionContextMenu
          key={sessionContext.key}
          x={sessionContext.x}
          y={sessionContext.y}
          anchor={sessionContext.anchor}
          activeSessionId={chat.activeId}
          onClose={() => setSessionContext((current) => current?.key === sessionContext.key ? null : current)}
        >
          <button role="menuitem" onClick={() => togglePin(s.id)}>
            <HarnessIcon name="pin" size={15} />
            {pinned.includes(s.id) ? "Bỏ ghim" : "Ghim"}
          </button>
          <button
            role="menuitem"
            onClick={() => {
              setDialog({ type: "rename", session: s, value: s.title || "" });
              closeMenu();
            }}
          >
            <HarnessIcon name="edit" size={15} />
            Đổi tên
          </button>
          {!s.shared ? (
            <>
              <button
                role="menuitem"
                onClick={() => {
                  void chat.archive(s.id, !archived);
                  closeMenu();
                }}
              >
                <HarnessIcon name="history" size={15} />
                {archived ? "Khôi phục phiên" : "Lưu trữ"}
              </button>
              <button
                role="menuitem"
                className="danger"
                onClick={() => {
                  setDialog({ type: "delete", session: s });
                  closeMenu();
                }}
              >
                <HarnessIcon name="trash" size={15} />
                Xóa
              </button>
            </>
          ) : null}
        </SessionContextMenu>
      ) : null}
    </div>
    );
  };
  return (
    <AgentBrowserLinks.Provider
      value={conversationStarted && workspaceRunId ? openAgentBrowser : null}
    >
    <div
      className={[
        "chat-shell",
        !chat.activeId && "home-shell",
        !sidebar && "sidebar-hidden",
        sidebarPresence.mounted && "sidebar-mounted",
        sidebarPresence.animating && "sidebar-transitioning",
        sidebarPresence.phase === "entering" && "sidebar-entering",
        sidebarPresence.phase === "closing" && "sidebar-closing",
        artifactPresence.mounted && "with-panel",
        artifactPresence.animating && "artifact-transitioning",
        artifactPresence.phase === "entering" && "artifact-entering",
        artifactPresence.phase === "closing" && "artifact-closing",
      ].filter(Boolean).join(" ")}
      style={{ "--sidebar-width": `${sidebarWidth}px` }}
    >
      <header className="brand">
        <button
          className="brand-home"
          onClick={startNewChat}
          aria-label="Trang chủ LoopForge"
          inert={!sidebar}
          aria-hidden={!sidebar || undefined}
        >
          <span className="brand-icon">
            <HarnessIcon name="workflow" size={20} />
          </span>
          <strong>LoopForge</strong>
        </button>
        <IconButton
          className="sidebar-toggle"
          data-hover-ink-control=""
          label={sidebar ? "Ẩn thanh bên phiên" : "Hiện thanh bên phiên"}
          aria-expanded={sidebar}
          aria-controls="conversation-navigation"
          onClick={() => setSidebar(!sidebar)}
        >
          <HarnessIcon name="panel-left" size={16} />
        </IconButton>
      </header>
      <header className="conversation-header">
        <div className="conversation-title">
          {chat.session ? (
            <>
              <ConversationStatusIcon
                status={sessionReplyStatus(chat.session, {
                  generation: chat.generation,
                  busy: chat.busy,
                })}
              />
              <button
                disabled={chat.readOnly}
                title={sessionTitle}
                aria-label={sessionTitle}
                className={titleReveal ? "title-typing" : undefined}
                style={
                  titleReveal
                    ? {
                        "--title-step": `${titleReveal.step}ms`,
                        "--title-fade": `${titleReveal.fade}ms`,
                      }
                    : undefined
                }
                onClick={() =>
                  setDialog({
                    type: "rename",
                    session: chat.session,
                    value: chat.session.title,
                  })
                }
              >
                {titleReveal
                  ? titleReveal.chars.map((char, i) => (
                      <span
                        key={`${chat.session.id}-${i}`}
                        className="title-ch"
                        aria-hidden="true"
                        style={{ "--i": i }}
                      >
                        {char}
                      </span>
                    ))
                  : sessionTitle}
              </button>
              {!chat.readOnly ? (
                <IconButton
                  label="Đổi tên phiên"
                  onClick={() =>
                    setDialog({
                      type: "rename",
                      session: chat.session,
                      value: chat.session.title,
                    })
                  }
                >
                  <HarnessIcon name="edit" size={13} />
                </IconButton>
              ) : null}
              {pinnedExecutor ? (
                <span
                  className="conversation-engine"
                  title={`Phiên này dùng ${pinnedExecutor.label}; sau khi hội thoại bắt đầu không đổi được CLI. Muốn đổi CLI hãy tạo trò chuyện mới`}
                >
                  <ModelProviderIcon
                    brand={engineBrand(pinnedExecutor.id)}
                    size={12}
                  />
                  {pinnedExecutor.label}
                </span>
              ) : null}
              {chat.local ? <span className="preview-badge">Ví dụ</span> : null}
            </>
          ) : null}
        </div>
        <div className="header-actions">
          {chat.session ? (
            <>
              {conversationStarted ? <ExecutionMachinePicker chat={chat} onManage={() => navigate("runners")} /> : null}
              {projectName ? (
                <div className="menu-anchor conversation-project-anchor">
                  {/* The conversation's project; picking another one opens a
                      new chat there (a personal chat is bound in place). */}
                  <button
                    ref={headerProjectPicker}
                    type="button"
                    className="conversation-project"
                    title={`Dự án hiện tại: ${projectName}, bấm để chuyển`}
                    aria-label={`Dự án hiện tại: ${projectName}, chuyển dự án`}
                    aria-haspopup="listbox"
                    aria-expanded={menu?.type === "header-project"}
                    onClick={() =>
                      setMenu(
                        menu?.type === "header-project"
                          ? null
                          : { type: "header-project" },
                      )
                    }
                  >
                    <HarnessIcon name="folder" size={12} interactive={false} />
                    <span>{projectName}</span>
                    <HarnessIcon name="chevron-down" size={11} interactive={false} />
                  </button>
                  <SelectMenu
                    open={menu?.type === "header-project"}
                    label="Chuyển dự án"
                    value={chat.projectId}
                    options={projectOptions}
                    onSelect={chooseProject}
                    onClose={closeMenu}
                    anchorRef={headerProjectPicker}
                    searchable
                    emptyText="Không có dự án khớp"
                    actions={projectActions}
                  />
                </div>
              ) : null}
              {showEnvironmentPicker ? <SandboxPicker chat={chat} onCreate={() => navigate("sandboxes", { action: "create", projectId: chat.projectId })} /> : null}
              <Participants session={chat.session} users={chat.users} />
            </>
          ) : null}
          {conversationStarted && workspaceRunId ? (
            <button
              type="button"
              className={`panel-toggle changes-toggle ${panel && panelView === "changes" ? "active" : ""}`}
              title="Xem thay đổi code của phiên này"
              aria-label={`Xem thay đổi, tổng ${workspaceChanges.files.length} tệp`}
              aria-expanded={panel && panelView === "changes"}
              aria-controls="conversation-artifacts"
              onClick={() => {
                if (panel && panelView === "changes") {
                  setPanel(false);
                  return;
                }
                setPanelView("changes");
                setPanel(true);
              }}
            >
              <HarnessIcon name="edit" size={15} />
              <span className="changes-toggle-label">Thay đổi</span>
              {workspaceChanges.files.length ? (
                <span className="changes-toggle-count">
                  {workspaceChanges.files.length > 99
                    ? "99+"
                    : workspaceChanges.files.length}
                </span>
              ) : null}
            </button>
          ) : null}
          {conversationStarted ? <button
            type="button"
            className={`panel-toggle ${panel ? "active" : ""}`}
            data-hover-ink-control=""
            aria-label="Xem artifact của phiên"
            aria-expanded={panel}
            aria-controls="conversation-artifacts"
            aria-describedby={!panel && artifactUnread ? "artifact-unread-description" : undefined}
            title="Xem artifact của phiên"
            onClick={() => {
              setArtifactUnread(0);
              if (panel && panelView === "artifacts") {
                setPanel(false);
                return;
              }
              setPanelView("artifacts");
              setPanel(true);
            }}
          >
            <span className="artifact-toggle-glyph">
              <HarnessIcon name="chat-artifacts" size={16} />
              {!panel && artifactUnread ? (
                <span className="artifact-unread-badge" aria-hidden="true">
                  {artifactUnread > 99 ? "99+" : artifactUnread}
                </span>
              ) : null}
            </span>
            {!panel && artifactUnread ? (
              <span className="sr-only" id="artifact-unread-description">{artifactUnread} artifact mới</span>
            ) : null}
          </button> : null}
        </div>
      </header>
      {sidebarPresence.mounted ? (
        <aside
          id="conversation-navigation"
          className="conversation-sidebar"
          aria-label="Điều hướng phiên"
          inert={!sidebar}
          aria-hidden={!sidebar || undefined}
        >
          <div className="sidebar-create-row">
            <button className="new-chat-button" onClick={startNewChat} title="Trò chuyện mới · Ctrl/⌘ ⇧ O">
              <HarnessIcon name="edit" size={17} />
              <span>Trò chuyện mới</span>
            </button>
            <IconButton
              label="Tìm phiên"
              onClick={() => {
                setDialog({ type: "search" });
                setQuery("");
              }}
            >
              <HarnessIcon name="search" size={16} />
            </IconButton>
            <div className="menu-anchor">
              <IconButton
                label="Lọc phiên"
                aria-haspopup="menu"
                aria-expanded={menu?.type === "filter"}
                onClick={() =>
                  setMenu(menu?.type === "filter" ? null : { type: "filter" })
                }
              >
                <HarnessIcon name="filter" size={14} />
              </IconButton>
              {menu?.type === "filter" ? (
                <Menu label="Lọc phiên" className="sidebar-filter-menu" onClose={closeMenu}>
                  <button
                    role="menuitemradio"
                    aria-checked={!archived && !mineOnly}
                    onClick={() => {
                      setArchived(false);
                      setMineOnly(false);
                      closeMenu();
                    }}
                  >
                    Phiên gần đây
                    {!archived && !mineOnly ? <HarnessIcon name="check" size={14} /> : null}
                  </button>
                  <button
                    role="menuitemradio"
                    aria-checked={archived}
                    onClick={() => {
                      setArchived(true);
                      setMineOnly(false);
                      closeMenu();
                    }}
                  >
                    Đã lưu trữ
                    {archived ? <HarnessIcon name="check" size={14} /> : null}
                  </button>
                  <button
                    role="menuitemradio"
                    aria-checked={mineOnly}
                    onClick={() => {
                      setArchived(false);
                      setMineOnly(true);
                      closeMenu();
                    }}
                  >
                    Chỉ hội thoại tôi tham gia
                    {mineOnly ? <HarnessIcon name="check" size={14} /> : null}
                  </button>
                </Menu>
              ) : null}
            </div>
          </div>
          <div className="session-list">
            {archived ? <div className="section-label">Đã lưu trữ</div> : null}
            {!archived && sidebarSections.pinnedSessions.length ? (
              <SidebarSection
                className="sidebar-pinned"
                title="Ghim"
                icon="pin"
                count={sidebarSections.pinnedSessions.length}
                expanded={!!query || pinnedOpen}
                onToggle={() => setPinnedOpen((open) => !open)}
              >
                {sidebarSections.pinnedSessions.map((session) =>
                  renderSession(session, {
                    projectName: sessionProjectName(
                      session.projectId,
                      chat.projects,
                    ),
                  }),
                )}
              </SidebarSection>
            ) : null}
            {sidebarSections.groups.length ? (
              <>
                {sidebarSections.groups.map((group) => {
                  const stored = collapsedProjects[group.projectId];
                  const expanded =
                    !!query ||
                    (stored === undefined
                      ? group.projectId === chat.projectId || !group.projectId
                      : !stored);
                  const showAll = !!query || !!expandedProjects[group.projectId];
                  const sessions = showAll
                    ? group.sessions
                    : previewSessions(
                        group.sessions,
                        PROJECT_PREVIEW_LIMIT,
                        chat.activeId,
                      );
                  const hidden = group.sessions.length - sessions.length;
                  return (
                    <SidebarSection
                      key={group.projectId}
                      title={group.name}
                      icon={group.projectId ? "folder" : "user"}
                      count={group.sessions.length}
                      expanded={expanded}
                      onToggle={() =>
                        setCollapsedProjects((previous) => ({
                          ...previous,
                          [group.projectId]: expanded,
                        }))
                      }
                    >
                      {sessions.map((session) => renderSession(session))}
                      {hidden > 0 ? (
                        <button
                          type="button"
                          className="sidebar-more"
                          onClick={() =>
                            setExpandedProjects((previous) => ({
                              ...previous,
                              [group.projectId]: true,
                            }))
                          }
                        >
                          Mở thêm {hidden} mục
                        </button>
                      ) : null}
                      {showAll && group.sessions.length > PROJECT_PREVIEW_LIMIT ? (
                        <button
                          type="button"
                          className="sidebar-more"
                          onClick={() =>
                            setExpandedProjects((previous) => ({
                              ...previous,
                              [group.projectId]: false,
                            }))
                          }
                        >
                          Thu gọn
                        </button>
                      ) : null}
                    </SidebarSection>
                  );
                })}
              </>
            ) : null}
            {!sidebarSections.pinnedSessions.length &&
            !sidebarSections.groups.length ? (
              <div className="empty-session-list">
                <p className="end-label">
                  {chat.ready
                    ? archived
                      ? "Chưa có phiên đã lưu trữ"
                      : mineOnly
                        ? "Chưa có phiên bạn tham gia"
                        : "Chưa có phiên nào"
                    : "Đang kết nối…"}
                </p>
              </div>
            ) : null}
          </div>
        </aside>
      ) : null}
      <main
        className={`conversation-main ${!chat.activeId ? "empty-conversation" : ""}`}
      >
        {chat.activeId ? (
          <div
            className="message-scroll"
            ref={scroll}
            onScroll={(e) => {
              const el = e.currentTarget;
              follow.current =
                el.scrollHeight - el.scrollTop - el.clientHeight < 110;
            }}
          >
            <div className="message-column">
              {!chat.session && !chat.error ? (
                <div className="loading-session">Đang khôi phục hội thoại…</div>
              ) : null}
              {messages.map((message, index) =>
                isProposalTimeout(message, messages[index - 1]) ? null : confirmationCaption(message, messages[index - 1]) ? (
                  <div
                    key={index}
                    className="confirmation-note"
                    data-message-index={index}
                  >
                    <HarnessIcon name="check" size={14} aria-hidden="true" />
                    {confirmationCaption(message, messages[index - 1])}
                  </div>
                ) : (
                  <article
                    key={index}
                    className={`message ${message.role}`}
                    data-message-index={index}
                  >
                    {message.role === "assistant" ? (
                      <AssistantTurn
                        message={message}
                        steps={activityForReply(
                          message,
                          chat.generation,
                          chat.live.activity,
                        )}
                        assets={artifactList}
                        projectId={chat.session?.projectId}
                        sessionId={chat.session?.id}
                        local={chat.local}
                        readOnly={chat.readOnly}
                        onCLIInstall={chat.adoptCLIInstall}
                      />
                    ) : null}
                    {message.role === "user" ? (
                    <div className="message-body">
                        <>
                          <div className="message-author">
                            <Avatar
                              person={authorFor(
                                message,
                                chat.session,
                                chat.users,
                              )}
                            />
                            <span>
                              {
                                authorFor(message, chat.session, chat.users)
                                  .name
                              }
                            </span>
                            <MessageTime messages={messages} index={index} />
                          </div>
                          <div className="user-bubble">{message.content}</div>
                          {message.references?.length ? (
                            <div className="message-references">
                              {message.references.map((ref) => (
                                <span
                                  key={ref.type + ref.id}
                                  title={ref.type === "element" ? elementTitle(ref) : undefined}
                                >
                                  {ref.type === "element" ? (
                                    <HarnessIcon name="crosshair" size={12} />
                                  ) : (
                                    "@"
                                  )}
                                  {ref.name || ref.id}
                                </span>
                              ))}
                            </div>
                          ) : null}
                          <div className="user-actions">
                            {!chat.readOnly ? (
                              <>
                                <IconButton
                                  label="Sửa tin nhắn"
                                  disabled={chat.busy}
                                  onClick={() =>
                                    setDialog({
                                      type: "edit",
                                      index,
                                      value: message.content,
                                      fork: false,
                                    })
                                  }
                                >
                                  <HarnessIcon name="edit" size={14} />
                                </IconButton>
                                <IconButton
                                  label="Tạo nhánh từ đây"
                                  disabled={chat.busy}
                                  onClick={() =>
                                    setDialog({
                                      type: "edit",
                                      index,
                                      value: message.content,
                                      fork: true,
                                    })
                                  }
                                >
                                  <HarnessIcon name="branch" size={14} />
                                </IconButton>
                              </>
                            ) : null}
                            <CopyButton
                              value={message.content}
                              label="Sao chép tin nhắn"
                            />
                          </div>
                        </>
                    </div>
                    ) : null}
                    {message.attachments?.length ? (
                      <div className="message-attachments">
                        {message.attachments.map((a) => (
                          <Attachment
                            key={a.id}
                            attachment={a}
                            sessionId={chat.session.id}
                            onOpen={openArtifact}
                          />
                        ))}
                      </div>
                    ) : null}
                    {message.role === "assistant" ? (
                      <>
                        <div className="message-drafts">
                          {artifactList
                            .filter((a) => a.draft && a.messageIndex === index)
                            .map((a) => (
                              <DraftCard
                                key={a.id}
                                artifact={a}
                                context={operationContext}
                                onApply={chat.readOnly ? null : chat.applyDraft}
                                onDecide={
                                  chat.readOnly
                                    ? null
                                    : (messageIndex, draftIndex, decision) =>
                                        chat.decideProposal(messageIndex, draftIndex, decision, approval)
                                }
                                onPreview={openArtifact}
                              />
                            ))}
                        </div>
                        <div className="message-actions">
                          <MessageTime messages={messages} index={index} />
                          <CopyButton
                            value={copiedReply(
                              message,
                              chat.generation,
                              chat.live.activity,
                            )}
                            label="Sao chép câu trả lời"
                          />
                          <MessageInfo message={message} sessionExecutor={chat.session?.executor} />
                          {!chat.local && !chat.readOnly && message.generationId && !message.failure ? <GenerationDiagnostics sessionId={chat.session.id} generationId={message.generationId} compact /> : null}
                        </div>
                      </>
                    ) : null}
                  </article>
                ),
              )}
              {/* The live reply belongs right under its question. Alerts stay
                  last, so a finished reply does not jump above them. */}
              {chat.busy ? (
                <LiveMessage live={chat.live} generation={chat.generation} local={chat.local} readOnly={chat.readOnly} />
              ) : chat.generation &&
                !generationStored(messages, chat.generation) &&
                !["completed", "running"].includes(chat.generation.status) &&
                !messages.at(-1)?.unfinished &&
                (chat.live.activity?.length || chat.live.answer) ? (
                <LiveMessage
                  live={chat.live}
                  generation={chat.generation}
                  finished
                  local={chat.local}
                  readOnly={chat.readOnly}
                />
              ) : null}
              <NativeApprovalPanel chat={chat} onChange={() => {
                if (follow.current && scroll.current)
                  scroll.current.scrollTop = scroll.current.scrollHeight;
              }} />
              {!chat.local && !chat.busy && chat.generation?.failure && !messages.some((m) => m.generationId === chat.generation.id && m.failure) ? <GenerationDiagnostics sessionId={chat.session?.id} generationId={chat.generation.id} failure={chat.generation.failure} onCLIInstall={chat.adoptCLIInstall} readOnly={chat.readOnly} /> : null}
              {!chat.local && !chat.readOnly && !chat.busy && chat.generation?.id && chat.generation.status === "cancelled" && !chat.generation.failure && !messages.some((m) => m.generationId === chat.generation.id) ? <GenerationDiagnostics sessionId={chat.session?.id} generationId={chat.generation.id} compact /> : null}
              <RunAlerts
                runs={conversationRuns}
                user={chat.user}
                users={chat.users}
                readOnly={chat.readOnly}
                onChanged={() => setRunsRefresh((n) => n + 1)}
                onOpenNode={(run, nodeId) =>
                  openArtifact({
                    kind: "run",
                    id: run.id,
                    runId: run.id,
                    name: run.workflowSnapshot?.name || "Lần chạy SOP",
                    nodeId,
                  })
                }
                onAsk={(item) => {
                  setText(
                    `Lần chạy ${item.runId}, bước “${item.nodeName}”${item.kind === "failed" ? " đã thất bại" : " đang chờ phê duyệt thủ công"}. Hãy phân tích đang kẹt ở đâu, nguyên nhân là gì, và ai cần làm gì.`,
                  );
                  document
                    .querySelector('textarea[aria-label="Tin nhắn"]')
                    ?.focus();
                }}
              />
            </div>
          </div>
        ) : (
          <HomeWelcome />
        )}
        <div className="composer-region">
          <div className="composer-column">
            {chat.error && !(chat.generation?.failure && chat.error === chat.generation.error) ? (
              <div role="alert" className="chat-error">
                <span>{chat.error}</span>
                <CredentialAction text={chat.error} />
                <CLIInstallAction text={chat.error} onResult={chat.adoptCLIInstall} />
                <IconButton label="Đóng gợi ý" onClick={() => chat.setError("")}>
                  <HarnessIcon name="close" size={15} />
                </IconButton>
              </div>
            ) : null}
            {chat.ready &&
            !chat.local &&
            chat.user &&
            !chat.user.isGuest &&
            !chat.user.codeBuddyApiKeyConfigured &&
            !chat.sponsoredModel &&
            chat.executor === "codebuddy" &&
            !chat.readOnly &&
            credentialHint(chat.error)?.field !== "codeBuddyApiKey" ? (
              <div className="credential-notice" role="status">
                <HarnessIcon name="key" size={15} aria-hidden="true" />
                <span>
                  Executor CodeBuddy hiện tại cần cấu hình CodeBuddy API Key trước.
                  {chat.user.codexApiKeyConfigured ||
                  chat.user.claudeCodeApiKeyConfigured
                    ? "Bạn đã cấu hình key Codex / Claude Code; cũng có thể đổi executor ngay dưới ô nhập."
                    : ""}
                </span>
                <button
                  type="button"
                  onClick={() => openCredentialSettings("codeBuddyApiKey")}
                >
                  Đi cấu hình
                </button>
              </div>
            ) : null}
            {chat.readOnly ? (
              <div className="readonly-notice" role="status">
                <HarnessIcon name="eye" size={16} aria-hidden="true" />
                <span>
                  Bạn không phải thành viên của “{projectName}
                  ” nên chỉ xem được phiên này. Tham gia dự án xong thì có thể tiếp tục trò chuyện.
                </span>
              </div>
            ) : (
              <div
                className="composer-stack"
                onDragOver={(e) => e.preventDefault()}
                onDrop={(e) => {
                  e.preventDefault();
                  void upload([...e.dataTransfer.files]);
                }}
              >
                {!conversationStarted ? (
                <div className="composer-context">
                  <ExecutionMachinePicker chat={chat} onManage={() => navigate("runners")} />
                  <ChevronRightSmall />
                  <div className="menu-anchor">
                    <button
                      ref={projectPicker}
                      className={`context-pill project-picker${chat.projectId ? "" : " is-unset"}`}
                      title="Dự án của hội thoại này: yêu cầu, repository, vai trò và SOP đều tách theo dự án"
                      aria-label="Chọn dự án"
                      aria-haspopup="listbox"
                      aria-expanded={menu?.type === "project"}
                      onClick={() =>
                        setMenu(
                          menu?.type === "project" ? null : { type: "project" },
                        )
                      }
                    >
                      <HarnessIcon name="folder" size={14} />
                      <span>
                        {chat.projectId
                          ? projectName
                          : chat.activeId
                            ? "Gắn dự án"
                            : "Chọn dự án"}
                      </span>
                      <HarnessIcon name="chevron-down" size={12} />
                    </button>
                    <SelectMenu
                      open={menu?.type === "project"}
                      label="Dự án"
                      value={chat.projectId}
                      options={projectOptions}
                      onSelect={chooseProject}
                      onClose={closeMenu}
                      anchorRef={projectPicker}
                      searchable
                      emptyText="Không có dự án khớp"
                      actions={projectActions}
                    />
                  </div>
                  {showEnvironmentPicker && chat.projectId ? <ChevronRightSmall /> : null}
                  {showEnvironmentPicker ? <SandboxPicker chat={chat} onCreate={() => navigate("sandboxes", { action: "create", projectId: chat.projectId })} /> : null}
                </div>
                ) : null}
                <QueuedMessages
                  items={chat.queue}
                  steering={chat.steering}
                  held={chat.queueHeld}
                  busy={chat.busy}
                  readOnly={chat.readOnly}
                  onEdit={editQueued}
                  onRemove={chat.removeQueued}
                  onResume={chat.resumeQueue}
                  onSendNow={chat.sendNow}
                />
                <div className="composer">
                  <MentionPicker
                    ref={mentionPicker}
                    key={chat.projectId}
                    projectId={chat.projectId}
                    trigger={mention}
                    editor={input}
                    onSelect={addReference}
                    onClose={() => setMention(null)}
                    issueId={chat.issueId}
                    canManageIssue={!!chat.projectId && !chat.busy && (!chat.activeId || !!chat.session) && !chat.session?.shared && !chat.readOnly}
                    onManageIssue={() => {
                      setMention(null);
                      setIssueContextOpen(true);
                    }}
                  />
                  {attachments.length || pendingAttachments.length || references.length ? (
                    <div className="composer-attachments">
                      {pendingAttachments.map((a) => (
                        <span
                          key={a.id}
                          className="composer-attachment-thumb is-uploading"
                          title={`Đang tải ${a.name}`}
                        >
                          <span className="composer-attachment-preview">
                            <img src={a.previewURL} alt={a.name} />
                          </span>
                          <span className="composer-attachment-progress" />
                        </span>
                      ))}
                      {attachments.map((a) => {
                        const image = a.mimeType?.startsWith("image/");
                        const url = composerPreviewURL(a);
                        return image ? (
                          <span key={a.id} className="composer-attachment-thumb">
                            <button
                              type="button"
                              className="composer-attachment-preview"
                              title={`${a.name} · bấm để xem ảnh lớn`}
                              aria-label={`Xem ảnh ${a.name}`}
                              onClick={() =>
                                setImagePreview({ src: url, name: a.name })
                              }
                            >
                              <img src={url} alt={a.name} />
                            </button>
                            <button
                              type="button"
                              className="composer-attachment-remove"
                              aria-label={`Gỡ ${a.name}`}
                              title={`Gỡ ${a.name}`}
                              onClick={() => removeAttachment(a)}
                            >
                              <HarnessIcon name="close" size={11} />
                            </button>
                          </span>
                        ) : (
                          <span
                            key={a.id}
                            className="composer-attachment-file"
                          >
                            <HarnessIcon name="paperclip" size={13} />
                            <span className="composer-attachment-name">
                              {a.name}
                            </span>
                            {a.size ? (
                              <small>{formatFileSize(a.size)}</small>
                            ) : null}
                            <button
                              aria-label={`Gỡ ${a.name}`}
                              onClick={() => removeAttachment(a)}
                            >
                              <HarnessIcon name="close" size={13} />
                            </button>
                          </span>
                        );
                      })}
                      {references.map((ref) => (
                        <span
                          key={ref.type + ref.id}
                          className={`composer-attachment-file${ref.type === "element" ? " is-element" : ""}`}
                          title={ref.type === "element" ? elementTitle(ref) : undefined}
                        >
                          {ref.type === "element" ? (
                            <HarnessIcon name="crosshair" size={13} />
                          ) : (
                            "@"
                          )}
                          {ref.name || ref.id}
                          <button
                            aria-label={`Gỡ trích dẫn ${ref.name || ref.id}`}
                            onClick={() => {
                              setReferences((old) =>
                                old.filter(
                                  (x) => x.id !== ref.id || x.type !== ref.type,
                                ),
                              );
                              setText((old) =>
                                old.replace(`@${ref.name || ref.id} `, ""),
                              );
                              setMention(null);
                            }}
                          >
                            <HarnessIcon name="close" size={13} />
                          </button>
                        </span>
                      ))}
                    </div>
                  ) : null}
                  <textarea
                    ref={input}
                    aria-label="Tin nhắn"
                    rows={2}
                    placeholder={
                      chat.activeId
                        ? "Cho tôi biết bạn muốn làm gì tiếp"
                        : "Chia sẻ ý tưởng, hoặc giao cho tôi một việc"
                    }
                    value={text}
                    aria-expanded={!!mention}
                    onChange={(e) => {
                      setText(e.target.value);
                      setReferences((old) =>
                        old.filter(
                          (ref) =>
                            !text.includes(`@${ref.name || ref.id}`) ||
                            e.target.value.includes(`@${ref.name || ref.id}`),
                        ),
                      );
                      setMention(
                        e.nativeEvent.isComposing
                          ? null
                          : mentionAt(
                              e.target.value,
                              e.target.selectionStart,
                              references,
                            ),
                      );
                      e.target.style.height = "auto";
                      e.target.style.height =
                        Math.min(e.target.scrollHeight, 220) + "px";
                    }}
                    onCompositionEnd={(e) =>
                      setMention(
                        mentionAt(
                          e.target.value,
                          e.target.selectionStart,
                          references,
                        ),
                      )
                    }
                    onClick={(e) =>
                      setMention(
                        mentionAt(
                          e.target.value,
                          e.target.selectionStart,
                          references,
                        ),
                      )
                    }
                    onPaste={(e) => {
                      const files = [...e.clipboardData.items]
                        .filter((i) => i.kind === "file")
                        .map((i) => i.getAsFile())
                        .filter(Boolean);
                      if (files.length) {
                        e.preventDefault();
                        void upload(files);
                      }
                    }}
                    onKeyDown={(e) => {
                      if (mention && mentionPicker.current?.keyDown(e)) return;
                      if (
                        e.key === "Enter" &&
                        !e.shiftKey &&
                        !e.nativeEvent.isComposing
                      ) {
                        e.preventDefault();
                        void submit();
                      }
                    }}
                  />
                  <footer className="composer-toolbar">
                    <div className="menu-anchor">
                      <IconButton
                        label="Thêm"
                        disabled={uploading}
                        aria-haspopup="menu"
                        aria-expanded={menu?.type === "add"}
                        onClick={() =>
                          setMenu(menu?.type === "add" ? null : { type: "add" })
                        }
                      >
                        <HarnessIcon name="plus" size={20} />
                      </IconButton>
                      {menu?.type === "add" ? (
                        <Menu
                          label="Thêm vào tin nhắn"
                          className="above"
                          onClose={closeMenu}
                        >
                          <button
                            role="menuitem"
                            onClick={() => {
                              fileInput.current.click();
                              closeMenu();
                            }}
                          >
                            <HarnessIcon name="paperclip" size={15} />
                            Tải lên ảnh hoặc tệp
                          </button>
                          <button role="menuitem" onClick={openReferences}>
                            <HarnessIcon name="connector" size={15} />
                            Trích dẫn tài sản dự án
                          </button>
                          <button role="menuitem" onClick={() => {
                            closeMenu();
                            setDialog({ type: "advanced" });
                          }}>
                            <HarnessIcon name="settings" size={15} />
                            Cài đặt nâng cao
                          </button>
                        </Menu>
                      ) : null}
                    </div>
                    <input
                      ref={fileInput}
                      type="file"
                      multiple
                      hidden
                      onChange={(e) => {
                        void upload([...e.target.files]);
                        e.target.value = "";
                      }}
                    />
                    {chat.executors.length > 1 && !pinnedExecutor && !chat.submitting ? (
                      <PermissionPicker
                        label="CLI"
                        disabled={composerLoading}
                        options={chat.executors.map((executor) => {
                          const brand = engineBrand(executor.id);
                          return {
                            ...executor,
                            glyph: hasProviderLogo(brand)
                              ? <ModelProviderIcon brand={brand} size={16} />
                              : null,
                          };
                        })}
                        value={chat.executor}
                        onSelect={(id) => {
                          chat.setExecutor(id);
                          input.current?.focus();
                        }}
                        open={menu?.type === "executor"}
                        onOpenChange={(open) => setMenu(open ? { type: "executor" } : null)}
                      />
                    ) : null}
                    <ModelPicker
                      models={chat.models}
                      disabled={composerLoading}
                      value={chat.model}
                      label={modelLabel}
                      open={menu?.type === "model"}
                      onOpenChange={(open) => setMenu(open ? { type: "model" } : null)}
                      onSelect={(id) => {
                        chat.setModel(id);
                        input.current?.focus();
                      }}
                    />
                    <PermissionPicker
                      label="Cường độ suy luận"
                      disabled={composerLoading}
                      options={effortOptions(selectedModel)}
                      value={chat.effort}
                      onSelect={chat.setEffort}
                      open={menu?.type === "effort"}
                      onOpenChange={(open) => setMenu(open ? { type: "effort" } : null)}
                    />
                    <PermissionPicker
                      disabled={sessionLoading}
                      value={approval}
                      onSelect={setApproval}
                      open={menu?.type === "approval"}
                      onOpenChange={(open) => setMenu(open ? { type: "approval" } : null)}
                    />
                    <span className="toolbar-spacer" />
                    {uploading ? (
                      <span className="upload-status">Đang tải lên…</span>
                    ) : null}
                    {chat.busy ? (
                      <button
                        className="send-button stop"
                        aria-label="Dừng tạo"
                        title="Dừng và giữ phần đã tạo; tin nhắn đang xếp hàng sẽ tạm ngừng gửi"
                        disabled={
                          chat.submitting || chat.stopping || chat.generation?.status !== "running"
                        }
                        onClick={chat.stop}
                      >
                        <HarnessIcon name="chat-stop" size={14} />
                      </button>
                    ) : null}
                    {!chat.busy || text.trim() || attachments.length ? (
                      <button
                        className="send-button"
                        aria-label={chat.busy ? "Gửi theo hàng chờ" : "Gửi tin nhắn"}
                        title={chat.busy ? "Tự gửi khi câu trả lời hiện tại kết thúc" : undefined}
                        disabled={
                          !chat.ready ||
                          chat.submitting ||
                          chat.machineSaving ||
                          composerLoading ||
                          (!text.trim() && !attachments.length) ||
                          uploading
                        }
                        onClick={submit}
                      >
                        <HarnessIcon name="chat-send" size={20} />
                      </button>
                    ) : null}
                  </footer>
                </div>
              </div>
            )}
          </div>
        </div>
        {!chat.activeId && !chat.readOnly ? (
          <section className="home-starters" aria-label="Có thể bắt đầu như thế này">
            <div className="home-starter-grid">
              {homeStarters.map((starter, index) => (
                <button
                  type="button"
                  className={`home-starter home-starter-${starter.tone}`}
                  key={starter.id || starter.title}
                  style={{ "--card-index": index }}
                  onClick={() => handleStarter(starter)}
                >
                  <span className="home-starter-top">
                    <span className="home-starter-icon">
                      <HarnessIcon name={starter.icon} size={21} />
                    </span>
                    <HarnessIcon
                      name="chevron-right"
                      className="home-starter-arrow"
                      size={16}
                      aria-hidden="true"
                    />
                  </span>
                  <strong>{starter.title}</strong>
                  <span className="home-starter-description">
                    {starter.description}
                  </span>
                  <span className="home-starter-flow" aria-hidden="true">
                    {starter.steps.map((step) => (
                      <span key={step}>
                        <i />
                        {step}
                      </span>
                    ))}
                  </span>
                </button>
              ))}
            </div>
          </section>
        ) : null}
      </main>
      {artifactPresence.mounted ? (
        <ArtifactPanel
          open={artifactPanelVisible}
          operationContext={operationContext}
          artifacts={artifactList}
          runs={conversationRuns}
          selected={
            artifactList.find((a) => a.id === selectedArtifact?.id) ||
            selectedArtifact
          }
          setSelected={setSelectedArtifact}
          chat={chat}
          view={panelView}
          onViewChange={setPanelView}
          workspaceChanges={workspaceChanges}
          workspaceRunId={workspaceRunId}
          browserRequest={browserRequest}
          onBrowserRequestHandled={browserRequestHandled}
          onAddElement={chat.readOnly ? null : addElementReference}
          onClose={() => setPanel(false)}
        />
      ) : null}
      <IssueContext
        chat={chat}
        open={issueContextOpen}
        onClose={() => {
          setIssueContextOpen(false);
          requestAnimationFrame(() => input.current?.focus());
        }}
        hideTrigger
      />
      {dialog ? (
        <Dialog
          title={
            {
              search: "Tìm phiên",
              rename: "Đổi tên phiên",
              delete: "Xóa phiên",
              edit: dialog.fork ? "Tạo nhánh" : "Sửa tin nhắn",
              settings: "Cài đặt",
              advanced: "Cài đặt nâng cao",
            }[dialog.type]
          }
          onClose={closeDialog}
          footer={
            ["rename", "edit", "delete"].includes(dialog.type) ? (
              <>
                <button className="quiet-button" onClick={closeDialog}>
                  Hủy
                </button>
                <button
                  className={
                    dialog.type === "delete"
                      ? "danger-button"
                      : "primary-button"
                  }
                  onClick={async () => {
                    if (dialog.type === "delete") {
                      await chat.remove(dialog.session.id);
                      closeDialog();
                    } else if (dialog.type === "rename") {
                      if (
                        await chat.rename(
                          dialog.session.id,
                          dialog.value.trim(),
                        )
                      )
                        closeDialog();
                    } else {
                      const stored = await chat.edit(
                        dialog.index,
                        dialog.value,
                        dialog.fork,
                      );
                      if (stored) {
                        closeDialog();
                        const mode = normalizeApprovalMode(readApproval(sessionApprovalScope(stored)).mode);
                        await chat.regenerate(stored, mode);
                      }
                    }
                  }}
                  disabled={dialog.type !== "delete" && !dialog.value?.trim()}
                >
                  {dialog.type === "delete"
                    ? "Xóa"
                    : dialog.type === "rename"
                      ? "Lưu"
                      : dialog.fork
                        ? "Tạo và tiếp tục"
                        : "Lưu và tạo lại"}
                </button>
              </>
            ) : dialog.type === "advanced" ? (
              <button className="primary-button" onClick={closeDialog}>Xong</button>
            ) : null
          }
        >
          {dialog.type === "search" ? (
            <>
              <label className="dialog-search">
                <HarnessIcon name="search" size={17} />
                <input
                  autoFocus
                  aria-label="Tìm tên phiên"
                  placeholder="Tìm tên phiên hoặc dự án"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                />
              </label>
              <div className="search-results">
                {searchGroups.map((group) => (
                  <section
                    key={group.projectId}
                    aria-label={`Kết quả tìm trong ${group.name}`}
                  >
                    <div className="date-label">{group.name}</div>
                    {group.sessions.map((s) => (
                      <button
                        key={s.id}
                        onClick={() => {
                          chat.open(s.id);
                          closeDialog();
                          setQuery("");
                        }}
                      >
                        <HarnessIcon name="new-request" size={16} />
                        <span>
                          {s.displayTitle || s.title}
                          <small>
                            {sessionProjectName(s.projectId, chat.projects)}
                            {s.archived ? " · đã lưu trữ" : ""}
                          </small>
                        </span>
                      </button>
                    ))}
                  </section>
                ))}
                {!searchGroups.length ? (
                  <p className="end-label">Không có phiên khớp</p>
                ) : null}
              </div>
            </>
          ) : dialog.type === "rename" ? (
            <label className="field">
              Tên phiên
              <input
                autoFocus
                value={dialog.value}
                onChange={(e) =>
                  setDialog({ ...dialog, value: e.target.value })
                }
                onKeyDown={async (e) => {
                  if (
                    e.key === "Enter" &&
                    dialog.value.trim() &&
                    (await chat.rename(dialog.session.id, dialog.value.trim()))
                  )
                    closeDialog();
                }}
              />
            </label>
          ) : dialog.type === "delete" ? (
            <p>
              Xóa lịch sử chat “{dialog.session.title}”? Các yêu cầu trong hội thoại này chưa mở MR
               cũng sẽ bị xóa; yêu cầu đã mở MR và SOP đã lưu sẽ được giữ lại.
            </p>
          ) : dialog.type === "edit" ? (
            <>
              <textarea
                className="edit-message"
                autoFocus
                value={dialog.value}
                onChange={(e) =>
                  setDialog({ ...dialog, value: e.target.value })
                }
              />
              <p className="muted">
                {dialog.fork
                  ? "Tạo nhánh độc lập, giữ nguyên phiên gốc."
                  : "Phần hội thoại sau tin này sẽ được tạo lại; thao tác nền tảng đã chạy sẽ không bị hoàn tác."}
              </p>
            </>
          ) : dialog.type === "advanced" ? (
            <>
              <p className="muted">Mặc định dùng môi trường đã cấu hình của dự án, không cần chọn. Các tùy chọn dưới đây chỉ ảnh hưởng giao diện của bạn.</p>
              <label className="settings-row">
                <span>Hiện bộ chọn môi trường chạy</span>
                <HarnessSelect
                  ariaLabel="Tùy chọn môi trường nâng cao"
                  value={showEnvironmentPicker ? "show" : "hide"}
                  onChange={(e) => changeEnvironmentVisibility(e.target.value === "show")}
                  options={[{ value: "hide", label: "Ẩn (mặc định)" }, { value: "show", label: "Hiện" }]}
                />
              </label>
              <p className="muted">Khi bật, bạn có thể chỉnh môi trường chạy của hội thoại ngay cạnh dự án. Ẩn lối vào sẽ không đổi môi trường đã chọn hay workspace hiện có.</p>
            </>
          ) : dialog.type === "settings" ? (
            <>
              <label className="settings-row">
                <span>Giao diện</span>
                <HarnessSelect
                  value={theme}
                  ariaLabel="Giao diện"
                  onChange={(e) => setTheme(e.target.value)}
                  options={THEME_OPTIONS}
                />
              </label>
              <button
                className="settings-row"
                onClick={() => navigate("settings")}
              >
                <span>Cài đặt cá nhân và thông tin xác thực model</span>
                <ChevronRightSmall />
              </button>
              <button
                className="settings-row"
                onClick={() => navigate("runners")}
              >
                <span>Máy thực thi</span>
                <ChevronRightSmall />
              </button>
              <small className="muted">
                {chat.user?.chineseName || chat.user?.loginName}
              </small>
            </>
          ) : null}
        </Dialog>
      ) : null}
      {imagePreview ? (
        <ImageLightbox
          src={imagePreview.src}
          alt={imagePreview.name}
          onClose={() => setImagePreview(null)}
        />
      ) : null}
      <QuickAccessWizard
        open={quickAccessOpen}
        user={chat.user}
        projects={chat.projects}
        projectId={chat.projectId}
        defaultModel={chat.model}
        sponsoredModel={chat.sponsoredModel}
        sponsoredModels={chat.sponsoredModels}
        onClose={() => setQuickAccessOpen(false)}
        onOpenView={(view) => navigate(view)}
        onCredentialsSaved={chat.refreshUser}
        onCompleted={async (nextProjectId) => {
          await chat.refresh();
          if (nextProjectId) await chat.selectProject(nextProjectId);
        }}
      />
    </div>
    </AgentBrowserLinks.Provider>
  );
}
function ChevronRightSmall() {
  return (
    <HarnessIcon name="chevron-right" size={14} className="small-chevron" />
  );
}
function formatFileSize(size) {
  const n = Number(size) || 0;
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.max(1, Math.round(n / 1024))} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}
