import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  write,
  sessionPath,
  newId,
  mergeEvents,
  projectChange,
  canChatInProject,
  restoreStream,
  confirmationCaption,
} from "./api";
import { reviewedOperation, operationError } from "./operations.js";
import { credentialHint } from "./credentials.js";
import { modelEffort } from "./effort.js";
import { conversationEngine, engineModels } from "./engines.js";
import {
  enqueue,
  holdQueue,
  nextQueued,
  queuedMessage,
  readQueues,
  removeQueued,
  restoreQueued,
  writeQueues,
} from "./message-queue.js";
// Main Bot can run on any configured agent CLI; CodeBuddy stays the default.
export const EXECUTORS = [
  { id: "codebuddy", icon: "sparkles", label: "CodeBuddy", detail: "使用 CodeBuddy 模型与个人 API Key。" },
  { id: "codex", icon: "terminal", label: "Codex", detail: "使用 Codex CLI 与个人 Codex API Key。" },
  { id: "claude-code", icon: "brain", label: "Claude Code", detail: "使用 Claude Code CLI 与个人 Claude Code API Key。" },
  { id: "opencode", icon: "terminal", label: "OpenCode", detail: "使用 OpenCode 与个人 API Key。" },
  { id: "cursor", icon: "terminal", label: "Cursor", detail: "使用 Cursor CLI 与个人 API Key。" },
  { id: "agy", icon: "sparkles", label: "Antigravity", detail: "使用 Antigravity 与个人 API Key。" },
  { id: "kimi", icon: "sparkles", label: "Kimi Code", detail: "使用 Kimi Code 与个人 API Key。" },
  { id: "pi", icon: "terminal", label: "Pi", detail: "使用 Pi 与个人 API Key。" },
  { id: "dsh", icon: "terminal", label: "DeepSeek Harness", detail: "使用 DeepSeek Harness 与个人 API Key。" },
];
const EXECUTOR_KEY = "loopforge.assistant.executor";
function knownExecutor(id) {
  if (id === "claude") return "claude-code";
  return EXECUTORS.some((item) => item.id === id) ? id : "codebuddy";
}
function storedExecutor() {
  try {
    return knownExecutor(localStorage.getItem(EXECUTOR_KEY));
  } catch {
    return "codebuddy";
  }
}

// Mirrors errAssistantSponsoredModel in internal/httpapi/assistant_sponsor.go;
// credentialHint turns it into a "去配置" button.
export function sponsoredModelMessage(models = []) {
  return `未配置个人 CodeBuddy API Key 时只能使用平台管理员允许的模型（${models.join("、")}）。要使用其他模型，请先在个人配置中配置自己的 CodeBuddy API Key`;
}

// The models a user without their own key may chat with, the default first.
export function sponsoredModelList(catalog) {
  const listed = Array.isArray(catalog?.sponsoredModels) ? catalog.sponsoredModels.filter(Boolean) : [];
  if (listed.length) return listed;
  return catalog?.sponsoredModel ? [catalog.sponsoredModel] : [];
}
const PROJECT_LIST_TOOLS = new Set([
  "projects_save",
  "projects_delete",
  "projects_restore",
]);
const initialProject =
  new URLSearchParams(location.search).get("projectId") || "";
export function useChat() {
  const [user, setUser] = useState(null),
    [projects, setProjects] = useState([]),
    [sandboxes, setSandboxes] = useState([]),
    [sandboxProfileId, setSandboxProfileId] = useState(""),
    [sandboxBusy, setSandboxBusy] = useState(false),
    [workspaceSandbox, setWorkspaceSandbox] = useState(null),
    [users, setUsers] = useState([]),
    [draftIssueId, setDraftIssueId] = useState(
      new URLSearchParams(location.search).get("issueId") || "",
    ),
    [models, setModels] = useState([]),
    [sessions, setSessions] = useState([]),
    [session, setSession] = useState(null),
    [activeId, setActiveId] = useState(
      new URLSearchParams(location.search).get("session") || null,
    ),
    // The last conversation reached by opening it rather than by creating it;
    // the header reveals that conversation's title once it has loaded.
    [revealId, setRevealId] = useState(activeId),
    [projectId, setProjectId] = useState(initialProject),
    [model, setModel] = useState(""),
    [effort, setEffort] = useState(""),
    [sponsoredModels, setSponsoredModels] = useState([]),
    [executor, setExecutorState] = useState(storedExecutor),
    [providerCatalogs, setProviderCatalogs] = useState({}),
    [generation, setGeneration] = useState(null),
    [live, setLive] = useState({ answer: "", thinking: "", tools: [] }),
    [error, setError] = useState(""),
    [ready, setReady] = useState(false),
    [submitting, setSubmitting] = useState(false),
    // Messages sent while a reply runs, per conversation (see message-queue.js).
    [queues, setQueues] = useState(readQueues),
    // The generation whose final transcript has been loaded. A queued message
    // waits for it, so it is sent after the finished (or unfinished) reply.
    [settledId, setSettledId] = useState(null),
    // A queued message sent with "send now": it interrupts the running reply
    // and stays on screen until the server's follow-up turn has started.
    [steering, setSteering] = useState(null);
  const currentId = useRef(activeId),
    sessionRef = useRef(session),
    projectRef = useRef(projectId),
    globalProject = useRef(initialProject),
    submittingRef = useRef(false),
    sandboxBusyRef = useRef(false),
    applyingRef = useRef(new Set()),
    dispatchingRef = useRef(false),
    steeringRef = useRef(null),
    selectRef = useRef(null),
    defaultModel = useRef("");
  currentId.current = activeId;
  sessionRef.current = session;
  projectRef.current = projectId;
  const refreshSessions = useCallback(async () => {
    const rows = await api("/api/assistant/sessions");
    setSessions(rows || []);
    const current = sessionRef.current;
    const row = (rows || []).find((item) => item.id === current?.id);
    if (row?.title && current && !current.titleEdited && row.title !== current.title) {
      setSession((prev) =>
        prev && prev.id === row.id && !prev.titleEdited
          ? { ...prev, title: row.title, titleGenerated: !!row.titleGenerated }
          : prev,
      );
    }
  }, []);
  const changeURL = useCallback((id) => {
    const url = new URL(location.href);
    url.searchParams.delete("issueId");
    if (id) url.searchParams.set("session", id);
    else url.searchParams.delete("session");
    history.replaceState({}, "", url);
    window.parent !== window &&
      window.parent.postMessage(
        { type: "agentflow-chat-session", sessionId: id || "" },
        location.origin,
      );
  }, []);
  const syncProject = useCallback((id) => {
    globalProject.current = id;
    window.parent !== window &&
      window.parent.postMessage(
        { type: "agentflow-chat-project", projectId: id },
        location.origin,
      );
  }, []);
  const open = useCallback(
    (id) => {
      if (id === currentId.current && sessionRef.current?.id === id) return;
      setError("");
      setGeneration(null);
      setLive({ answer: "", thinking: "", tools: [] });
      setSession(null);
      // A new conversation starts on the platform default, not on whatever
      // model the previously opened conversation used.
      if (!id) {
        const preferred = storedExecutor();
        setExecutorState(preferred);
        const next = engineModels(models, preferred);
        if (preferred === "codebuddy") setModel(defaultModel.current || next[0]?.id || "");
        else setModel(next[0]?.id || "default");
      }
      setEffort("");
      setSandboxProfileId("");
      currentId.current = id;
      setActiveId(id);
      if (id) setRevealId(id);
      changeURL(id);
    },
    [changeURL, models],
  );
  const newChat = useCallback(() => {
    setDraftIssueId("");
    open(null);
  }, [open]);
  useEffect(() => {
    let current = true,
      timer,
      connectionError = "";
    async function load() {
      try {
        const [auth, projectRows, catalog, users, sandboxRows] = await Promise.all([
          api("/api/auth/current"),
          api("/api/projects"),
          api("/api/assistant/models"),
          api("/api/users"),
          api("/api/sandboxes"),
        ]);
        if (!current) return;
        setUser({ ...auth.user, ...users.find((u) => u.id === auth.user.id) });
        setUsers(users || []);
        setProjects(projectRows || []);
        setSandboxes(sandboxRows || []);
        setModels(catalog.models || []);
        defaultModel.current = catalog.default || "";
        setSponsoredModels(sponsoredModelList(catalog));
        setModel(catalog.default);
        await refreshSessions();
        if (!current) return;
        const recovered = connectionError;
        setError((old) => (old === recovered ? "" : old));
        setReady(true);
      } catch (e) {
        if (!current) return;
        const retry = !e.status || e.status >= 500;
        connectionError = retry ? "连接暂时中断，正在重连…" : e.message;
        setError(connectionError);
        if (retry) timer = setTimeout(load, 2000);
      }
    }
    void load();
    return () => {
      current = false;
      clearTimeout(timer);
    };
  }, [refreshSessions]);
  useEffect(() => {
    if (!ready || !activeId) return;
    // send/edit already supplied this session. Reloading it here can return an
    // older empty snapshot and erase the optimistic message during generation.
    if (sessionRef.current?.id === activeId) return;
    let current = true,
      timer,
      connectionError = "";
    async function load() {
      try {
        const stored = await api(sessionPath(activeId));
        if (!current || currentId.current !== activeId) return;
        setSession(stored);
        changeURL(stored.id);
        // A conversation stays on the CLI that answered it. Replies saved before
        // model prefixes keep that CLI on the session executor.
        const locked = conversationEngine(stored.messages, models, stored.executor);
        setExecutorState(knownExecutor(locked || stored.executor));
        setModel(stored.model || defaultModel.current || models[0]?.id || "");
        setEffort(stored.effort || "");
        setProjectId(stored.projectId || "");
        syncProject(stored.projectId || "");
        const recovered = connectionError;
        setError((old) => (old === recovered ? "" : old));
      } catch (e) {
        if (!current) return;
        const retry = !e.status || e.status >= 500;
        connectionError = retry ? "连接暂时中断，正在重连…" : e.message;
        setError(connectionError);
        if (retry) timer = setTimeout(load, 2000);
      }
    }
    void load();
    return () => {
      current = false;
      clearTimeout(timer);
    };
  }, [activeId, ready, syncProject]);
  useEffect(() => {
    if (!ready) return;
    const timer = setInterval(
      () => void refreshSessions().catch(() => {}),
      10000,
    );
    return () => clearInterval(timer);
  }, [ready, refreshSessions]);
  const titlePrompt = (session?.messages || []).find((message) => message.role === "user")?.content || "";
  useEffect(() => {
    if (!ready || !session?.id || session.titleEdited || session.titleGenerated) return;
    if (!titlePrompt.trim()) return;
    let cancelled = false;
    const timer = setInterval(async () => {
      try {
        const stored = await api(sessionPath(session.id));
        if (cancelled || currentId.current !== session.id || !stored?.title) return;
        if (stored.title === sessionRef.current?.title && !stored.titleGenerated) return;
        setSession((prev) =>
          prev && prev.id === stored.id && !prev.titleEdited
            ? { ...prev, title: stored.title, titleGenerated: !!stored.titleGenerated }
            : prev,
        );
        setSessions((rows) =>
          (rows || []).map((item) =>
            item.id === stored.id
              ? { ...item, title: stored.title, titleGenerated: !!stored.titleGenerated }
              : item,
          ),
        );
      } catch {
        // The next tick retries. The prompt title stays until a summary arrives.
      }
    }, 2000);
    const stop = setTimeout(() => clearInterval(timer), 30000);
    return () => {
      cancelled = true;
      clearInterval(timer);
      clearTimeout(stop);
    };
  }, [ready, session?.id, session?.title, session?.titleEdited, session?.titleGenerated, titlePrompt]);
  useEffect(() => {
    if (!ready || !activeId) return;
    let cancelled = false,
      timer,
      source,
      cursor = 0,
      genId = null,
      loaded = null,
      connectionError = "";
    let accumulated = { answer: "", thinking: "", tools: [] };
    const consume = async (g) => {
      if (cancelled) return;
      if (connectionError) {
        const recovered = connectionError;
        connectionError = "";
        setError((current) => (current === recovered ? "" : current));
      }
      if (genId !== g.id) {
        const previous = genId;
        genId = g.id;
        cursor = 0;
        accumulated = { answer: "", thinking: "", tools: [] };
        // Steering starts a new generation. The interrupted paragraphs are
        // already saved; load them before the new turn replaces the live view.
        if (previous) {
          try {
            const stored = await api(sessionPath(activeId));
            if (cancelled) return;
            setSession(stored);
          } catch {
            // The next poll retries. The live turn can still proceed.
          }
        }
      }
      if (g.stream) {
        accumulated = restoreStream(g.stream);
        cursor = g.stream.sequence;
      }
      const events = (g.events || []).filter((e) => e.sequence > cursor);
      // Old persisted generations can lack a snapshot; use their surviving tail
      // once instead of polling the same missing beginning forever.
      cursor = Math.max(cursor, ...events.map((e) => e.sequence));
      accumulated = mergeEvents(accumulated, events);
      setLive(accumulated);
      setGeneration(g);
      if (g.status !== "running" && loaded !== g.id) {
        source?.close();
        source = null;
        const stored = await api(sessionPath(activeId));
        if (cancelled) return;
        loaded = g.id;
        setSession(stored);
        setSettledId(g.id);
        await refreshSessions();
        if (g.error && !["canceled", "cancelled"].includes(g.status))
          setError(g.error);
      }
    };
    const connect = () => {
      if (source || cancelled || typeof EventSource === "undefined")
        return false;
      source = new EventSource(`${sessionPath(activeId)}/generation?stream=1`);
      source.addEventListener("generation", (e) => {
        const g = JSON.parse(e.data);
        void consume(g)
          .then(() => {
            if (!cancelled && g.status !== "running")
              timer = setTimeout(poll, 2000);
          })
          .catch((error) => {
            if (!cancelled) setError(error.message);
          });
      });
      source.onerror = () => {
        source?.close();
        source = null;
        if (!cancelled) timer = setTimeout(poll, 500);
      };
      return true;
    };
    const poll = async () => {
      try {
        if (submittingRef.current) {
          timer = setTimeout(poll, 500);
          return;
        }
        const g = await api(
          `${sessionPath(activeId)}/generation?after=${cursor}`,
        );
        if (cancelled) return;
        await consume(g);
        if (g.status === "running" && connect()) return;
        timer = setTimeout(poll, g.status === "running" ? 200 : 2000);
      } catch (e) {
        if (cancelled) return;
        if (e.status !== 404) {
          connectionError = e.status ? e.message : "连接暂时中断，正在重连…";
          setError(connectionError);
        }
        timer = setTimeout(poll, 2000);
      }
    };
    void poll();
    return () => {
      cancelled = true;
      clearTimeout(timer);
      source?.close();
    };
  }, [activeId, ready, refreshSessions]);
  const selectProject = useCallback(
    async (id, { external = false } = {}) => {
      const current = sessionRef.current;
      const strategy = projectChange(current, id);
      setError("");
      if (strategy === "bind") {
        if (generation?.status === "running" || submittingRef.current) {
          setError("请先停止当前生成，再绑定项目。");
          return;
        }
        try {
          const saved = await write(
            sessionPath(current.id),
            { ...current, projectId: id },
            "PUT",
          );
          setSession(saved);
          setProjectId(id);
          syncProject(id);
          await refreshSessions();
        } catch (e) {
          setError(e.message);
        }
        return;
      }
      if (strategy === "new") newChat();
      if (id !== projectRef.current) {
        setSandboxProfileId("");
        setDraftIssueId("");
        const url = new URL(location.href);
        url.searchParams.delete("issueId");
        history.replaceState({}, "", url);
      }
      setProjectId(id);
      syncProject(id);
    },
    [generation?.status, newChat, refreshSessions, syncProject],
  );
  selectRef.current = selectProject;
  // The page's project switcher defaults to the first project, which a new
  // user usually has not joined. Start their new chats as personal chats
  // instead of failing on the first message. The parent page keeps its
  // project; only this draft drops it.
  useEffect(() => {
    if (!ready || activeId || !projectId) return;
    const project = projects.find((p) => p.id === projectId);
    if (project && !canChatInProject(project, user)) setProjectId("");
  }, [ready, activeId, projectId, projects, user]);
  useEffect(() => {
    const listener = (e) => {
      if (
        e.source !== window.parent ||
        e.origin !== location.origin
      ) return;
      if (e.data?.type === "agentflow-environments-changed" && ready) {
        void Promise.all([api("/api/sandboxes"), api("/api/projects")])
          .then(([sandboxRows, projectRows]) => {
            if (Array.isArray(sandboxRows)) setSandboxes(sandboxRows);
            if (Array.isArray(projectRows)) setProjects(projectRows);
          }).catch(() => {});
        return;
      }
      if (e.data?.type !== "agentflow-project-context") return;
      const id = e.data.projectId || "";
      if (id === globalProject.current) return;
      globalProject.current = id;
      void selectRef.current?.(id, { external: true });
    };
    window.addEventListener("message", listener);
    if (ready)
      window.parent !== window &&
        window.parent.postMessage(
          { type: "agentflow-chat-ready" },
          location.origin,
        );
    return () => window.removeEventListener("message", listener);
  }, [ready]);
  const codeBuddy = executor === "codebuddy";
  const listedForEngine = engineModels(models, executor);
  const executorModels = listedForEngine.length || codeBuddy
    ? (codeBuddy ? models.filter((m) => !m.engine || m.engine === "codebuddy") : listedForEngine)
    : providerCatalogs[executor]?.models || [];
  const missingKey = !!user && !user.codeBuddyApiKeyConfigured;
  // Without their own key users chat on the platform's shared key, limited to
  // the models the administrator allowed (the first is the default).
  const sponsoredModel = sponsoredModels[0] || "";
  const sponsored = codeBuddy && missingKey && !user.isGuest && !!sponsoredModel;
  const sharedModelFor = (id) => (sponsoredModels.includes(id) ? id : sponsoredModel);
  const activeModel = sponsored ? sharedModelFor(model) : model;
  const activeEffort = modelEffort(executorModels.find((m) => m.id === activeModel), effort);
  useEffect(() => {
    if (codeBuddy || listedForEngine.length || providerCatalogs[executor]) return;
    let current = true;
    api(`/api/assistant/models?executor=${encodeURIComponent(executor)}`)
      .then((catalog) => {
        if (!current) return;
        setProviderCatalogs((old) => ({ ...old, [executor]: catalog }));
        setModel((old) => (catalog.models || []).some((m) => m.id === old) ? old : catalog.default || "default");
      })
      .catch((e) => current && setError(e.message));
    return () => {
      current = false;
    };
  }, [codeBuddy, executor, listedForEngine.length, providerCatalogs]);
  const chooseExecutor = useCallback(
    (id) => {
      if (id === "claude") id = "claude-code";
      if (!EXECUTORS.some((item) => item.id === id) || id === executor) return;
      const locked = conversationEngine(sessionRef.current?.messages, models, sessionRef.current?.executor);
      try {
        localStorage.setItem(EXECUTOR_KEY, id);
      } catch {}
      setEffort("");
      const next = engineModels(models, id);
      const catalog = id === "codebuddy" ? { default: defaultModel.current } : providerCatalogs[id];
      setExecutorState(id);
      setModel(next[0]?.id || catalog?.default || (id === "codebuddy" ? defaultModel.current : "default"));
      if (locked && locked !== id) open(null);
    },
    [executor, models, open, providerCatalogs],
  );
  const chooseModel = useCallback(
    (id) => {
      if (sponsored && !sponsoredModels.includes(id)) {
        setError(sponsoredModelMessage(sponsoredModels));
        return false;
      }
      setModel(id);
      setEffort((value) => modelEffort(executorModels.find((m) => m.id === id), value));
      return true;
    },
    [sponsored, sponsoredModels, executorModels],
  );
  useEffect(() => {
    if (!ready) return;
    // Credential flags are omitted when false, so drop the old ones before
    // merging; otherwise a cleared key would still look configured.
    const refresh = () =>
      api("/api/users")
        .then((rows) => {
          setUsers(rows || []);
          let fresh = null;
          setUser((old) => {
            fresh = old && (rows || []).find((u) => u.id === old.id);
            if (!fresh) return old;
            const base = Object.fromEntries(
              Object.entries(old).filter(([k]) => !k.endsWith("Configured")),
            );
            return { ...base, ...fresh };
          });
          // Drop a "please configure X" error once X is configured.
          setError((old) => {
            const hint = credentialHint(old);
            return hint && fresh?.[`${hint.field}Configured`] ? "" : old;
          });
        })
        .catch(() => {});
    const onMessage = (e) => {
      if (
        e.source === window.parent &&
        e.origin === location.origin &&
        e.data?.type === "agentflow-credentials-changed"
      )
        void refresh();
    };
    window.addEventListener("message", onMessage);
    if (missingKey) window.addEventListener("focus", refresh);
    return () => {
      window.removeEventListener("message", onMessage);
      window.removeEventListener("focus", refresh);
    };
  }, [ready, missingKey]);
  useEffect(() => writeQueues(queues), [queues]);
  useEffect(() => {
    const s = steeringRef.current;
    if (!s) return;
    const done = () => {
      steeringRef.current = null;
      setSteering(null);
    };
    // Another conversation is open (the server still follows up there), or
    // the follow-up turn has started and owns the conversation now.
    if (s.sessionId !== activeId || (generation?.id && generation.id !== s.generationId))
      return done();
    if (generation?.id !== s.generationId || generation.status === "running") return;
    if (settledId !== s.generationId) return;
    if (!generation.steer) {
      // The reply ended before the message was taken: send it next instead.
      done();
      setQueues((old) => restoreQueued(old, s.sessionId, s.item, false));
      return;
    }
    s.endedAt ||= Date.now();
    if (Date.now() - s.endedAt > 30000) {
      done();
      setError("插入的消息已记录，但新一轮回复没有开始，请重新发送。");
    }
  });
  useEffect(() => {
    const id = activeId;
    const next = nextQueued(queues, id);
    if (!next || dispatchingRef.current || submittingRef.current) return;
    if (steeringRef.current) return;
    if (generation?.status === "running" || sessionRef.current?.id !== id) return;
    // Wait until the ended turn's transcript (with its reply) is loaded.
    if (generation && settledId !== generation.id) return;
    dispatchingRef.current = true;
    setQueues((old) => removeQueued(old, id, next.id));
    void send({ ...next, fromQueue: true })
      .then((ok) => {
        if (!ok) setQueues((old) => restoreQueued(old, id, next));
      })
      .catch(() => setQueues((old) => restoreQueued(old, id, next)))
      .finally(() => {
        dispatchingRef.current = false;
      });
  });
  const removeQueuedMessage = useCallback(
    (itemId) => setQueues((old) => removeQueued(old, currentId.current, itemId)),
    [],
  );
  const resumeQueue = useCallback(() => {
    setError("");
    setQueues((old) => holdQueue(old, currentId.current, false));
  }, []);
  // "Send now" on a queued message. Like Codex, it interrupts the running
  // reply: what that reply already wrote stays in the conversation and the
  // assistant continues with this message. The rest of the queue keeps
  // waiting. Without a running reply the message is simply sent first.
  async function sendNow(itemId) {
    const id = currentId.current;
    const item = id ? queues[id]?.items.find((m) => m.id === itemId) : null;
    if (!item || steeringRef.current || dispatchingRef.current) return false;
    if (generation?.status !== "running" || !generation.id) {
      if (submittingRef.current) return false;
      dispatchingRef.current = true;
      setQueues((old) => removeQueued(old, id, item.id));
      try {
        const ok = await send({ ...item, fromQueue: true });
        if (!ok) setQueues((old) => restoreQueued(old, id, item));
        return ok;
      } catch {
        setQueues((old) => restoreQueued(old, id, item));
        return false;
      } finally {
        dispatchingRef.current = false;
      }
    }
    const from = generation.id;
    steeringRef.current = { sessionId: id, generationId: from, item };
    setSteering({ sessionId: id, item });
    setQueues((old) => removeQueued(old, id, item.id));
    setError("");
    try {
      await write(
        `${sessionPath(id)}/generation/steer?generationId=${encodeURIComponent(from)}`,
        {
          content: item.text || "请阅读本轮附件并回答。",
          attachments: item.attachments || [],
          references: item.references || [],
          model: activeModel,
          effort: activeEffort,
          approvalMode: item.approvalMode || "confirm",
        },
      );
      return true;
    } catch (e) {
      steeringRef.current = null;
      setSteering(null);
      // If the reply just ended, the message simply goes out next.
      setQueues((old) => restoreQueued(old, id, item, false));
      if (!/没有进行中|已经结束|已结束/.test(e.message || "")) setError(e.message);
      return false;
    }
  }
  async function send({
    text,
    attachments = [],
    references = [],
    approvalMode = "confirm",
    messagesOverride = null,
    fromQueue = false,
  }) {
    if (!text.trim() && !attachments.length) return false;
    if (currentId.current && sessionRef.current?.id !== currentId.current) {
      setError("会话仍在加载，请稍候再发送。");
      return false;
    }
    if (
      submittingRef.current ||
      generation?.status === "running" ||
      steeringRef.current?.sessionId === currentId.current
    ) {
      // Like Codex, a message sent during a reply waits its turn instead of
      // stopping the reply. Internal follow-ups (confirmed drafts) never queue.
      if (messagesOverride || fromQueue || !currentId.current) {
        setError("当前回复仍在生成，请稍后再试。");
        return false;
      }
      const id = currentId.current;
      setQueues((old) =>
        enqueue(old, id, queuedMessage({ text, attachments, references, approvalMode })),
      );
      setError("");
      return true;
    }
    if (sandboxBusyRef.current) { setError("运行环境仍在保存，请稍后发送。"); return false; }
    // Without a key (and no sponsored model) the server rejects every message.
    // Say so before creating an empty conversation that would swallow the
    // typed question. Re-check first in case the key was saved elsewhere since
    // this page loaded.
    if (
      codeBuddy &&
      !sponsoredModel &&
      users.find((u) => u.id === user?.id)?.codeBuddyApiKeyConfigured ===
        undefined &&
      user &&
      !user.isGuest
    ) {
      const rows = await api("/api/users").catch(() => null);
      if (rows) setUsers(rows);
      const fresh = rows?.find((u) => u.id === user.id);
      if (fresh && !fresh.codeBuddyApiKeyConfigured) {
        setError("请先在个人配置中配置自己的 CodeBuddy API Key");
        return false;
      }
    }
    submittingRef.current = true;
    setSubmitting(true);
    setError("");
    let s = sessionRef.current;
    const previousGeneration = generation?.id;
    setGeneration(null);
    try {
      if (!s) {
        const id = newId();
        s = await write(
          sessionPath(id),
          {
            id,
            projectId: projectRef.current,
            issueId: draftIssueId,
            sandboxProfileId,
            model: activeModel,
            effort: activeEffort,
            executor,
            title: text.trim().slice(0, 36) || "图片与文件",
            messages: [],
          },
          "PUT",
        );
        setSession(s);
        currentId.current = id;
        setActiveId(id);
        changeURL(id);
      }
      const content = text.trim() || "请阅读本轮附件并回答。";
      const pending = {
        role: "user",
        authorId: user?.id,
        authorName: user?.loginName,
        content,
        // Shown until the stored transcript (stamped by the server) arrives.
        createdAt: new Date().toISOString(),
        ...(attachments.length ? { attachments } : {}),
        ...(references.length ? { references } : {}),
      };
      const messages = [...(messagesOverride || s.messages || []), pending];
      setSession({
        ...s,
        messages,
        participants:
          user &&
          !confirmationCaption(pending, messages.at(-2)) &&
          !s.participants?.some((p) => p.id === user.id)
            ? [...(s.participants || []), { id: user.id, name: user.loginName }]
            : s.participants,
      });
      setLive({ answer: "", thinking: "", tools: [] });
      const g = await write("/api/assistant/generations", {
        sessionId: s.id,
        projectId: s.projectId || "",
        runId: s.runId || "",
        executor,
        model: activeModel,
        effort: activeEffort,
        approvalMode,
        messages,
      });
      if (currentId.current === s.id) setGeneration(g);
      await refreshSessions();
      return true;
    } catch (e) {
      if (s) {
        try {
          const [stored, g] = await Promise.all([
            api(sessionPath(s.id)),
            api(`${sessionPath(s.id)}/generation`).catch(() => null),
          ]);
          if (currentId.current === s.id) setSession(stored);
          if (g?.id !== previousGeneration && g?.status === "running") {
            setGeneration(g);
            return true;
          }
        } catch {}
      }
      setError(e.message);
      return false;
    } finally {
      submittingRef.current = false;
      setSubmitting(false);
    }
  }
  async function stop() {
    if (!activeId) return;
    // Stopping means "wait for me": queued messages stay, but do not go out.
    setQueues((old) => holdQueue(old, activeId, true));
    try {
      await write(
        `${sessionPath(activeId)}/generation/cancel?generationId=${encodeURIComponent(generation?.id || "")}`,
        {},
      );
      setError("");
    } catch (e) {
      setError(e.message);
    }
  }
  async function rename(id, title) {
    try {
      const stored = await api(sessionPath(id));
      const saved = await write(sessionPath(id), { ...stored, title }, "PUT");
      if (id === currentId.current) setSession(saved);
      await refreshSessions();
      return true;
    } catch (e) {
      setError(e.message);
      return false;
    }
  }
  async function archive(id, archived) {
    try {
      await write(`${sessionPath(id)}/archive`, { archived });
      if (id === currentId.current) newChat();
      await refreshSessions();
    } catch (e) {
      setError(e.message);
    }
  }
  async function remove(id) {
    try {
      await api(sessionPath(id), { method: "DELETE" });
      if (id === currentId.current) newChat();
      await refreshSessions();
    } catch (e) {
      setError(e.message);
    }
  }
  async function edit(index, content, fork = false) {
    try {
      const stored = await write(
        `${sessionPath(activeId)}/${fork ? "fork" : "edit"}`,
        { messageIndex: index, content, updatedAt: session.updatedAt },
      );
      open(stored.id);
      setSession(stored);
      setModel(stored.model || defaultModel.current);
      setEffort(stored.effort || "");
      await refreshSessions();
      return stored;
    } catch (e) {
      setError(e.message);
      return null;
    }
  }
  async function regenerate(stored) {
    try {
      submittingRef.current = true;
      setSubmitting(true);
      open(stored.id);
      setSession(stored);
      const chosen = sponsored ? sharedModelFor(stored.model || model) : stored.model || model;
      setModel(chosen);
      setEffort(stored.effort || "");
      const g = await write("/api/assistant/generations", {
        sessionId: stored.id,
        projectId: stored.projectId || "",
        runId: stored.runId || "",
        executor,
        model: chosen,
        effort: codeBuddy ? modelEffort(models.find((m) => m.id === chosen), stored.effort || "") : "",
        messages: stored.messages,
        approvalMode: "confirm",
      });
      setGeneration(g);
      await refreshSessions();
      return true;
    } catch (e) {
      setError(e.message);
      return false;
    } finally {
      submittingRef.current = false;
      setSubmitting(false);
    }
  }
  async function applyDraft(messageIndex, draftIndex, review = {}) {
    const id = activeId;
    const key = `${id}:${messageIndex}:${draftIndex}`;
    if (applyingRef.current.has(key)) throw Error("这项操作正在执行，请稍候。");
    applyingRef.current.add(key);
    let executed = false;
    try {
      const stored = await api(sessionPath(id));
      const reply = JSON.parse(stored.messages[messageIndex].content);
      const draft = reply.drafts[draftIndex];
      if (draft.saved || draft.inherited) return null;
      try {
        Object.assign(draft, reviewedOperation(draft, review));
      } catch (error) {
        if (error.draftChanged && currentId.current === id) setSession(stored);
        throw error;
      }
      if (generation?.status === "running")
        throw Error("请等待回复结束后再确认操作。");
      // The session header lets a confirmed SOP continue in this
      // conversation's workspace.
      const result = await api("/mcp", {
        method: "POST",
        headers: { "x-agentflow-chat-session": id },
        body: JSON.stringify({
          jsonrpc: "2.0",
          id: Date.now(),
          method: "tools/call",
          params: {
            name: draft.tool,
            arguments: {
              ...(draft.path ? { path: draft.path } : {}),
              ...(draft.query ? { query: draft.query } : {}),
              ...(draft.body ? { body: draft.body } : {}),
            },
          },
        }),
      });
      if (result.error || result.result?.isError || !result.result)
        throw Error(
          operationError(
            result.error?.message ||
              result.result?.content?.[0]?.text ||
              "操作失败",
          ),
        );
      executed = true;
      draft.saved = true;
      draft.savedResult = result.result.structuredContent?.data || null;
      if (PROJECT_LIST_TOOLS.has(draft.tool)) {
        // A project created or removed here must show up in both project
        // pickers right away, not after a hard refresh of the page.
        void api("/api/projects")
          .then((rows) => Array.isArray(rows) && setProjects(rows))
          .catch(() => {});
        window.parent !== window &&
          window.parent.postMessage(
            { type: "agentflow-projects-changed" },
            location.origin,
          );
      }
      stored.messages[messageIndex].content = JSON.stringify(reply);
      const saved = await write(sessionPath(id), stored, "PUT");
      if (currentId.current === id) {
        setSession(saved);
        // Confirmation resumes the original goal, while subsequent writes still
        // require their own confirmation. A failed receipt must never be replayed.
        await send({
          text: `已确认并执行 ${draft.tool}。请核对这条草稿的保存回执，继续完成原始请求；不要重复刚才的写入，下一项需要确认时提交草稿，全部完成时总结实际结果。`,
          messagesOverride: saved.messages,
          approvalMode: "confirm",
        });
      }
      return draft;
    } catch (e) {
      setError(
        (executed
          ? "操作已执行，但回执保存失败，请先核对结果，不要重复执行。"
          : "") + e.message,
      );
      throw Object.assign(e, { executed });
    } finally {
      applyingRef.current.delete(key);
    }
  }
  async function linkIssue(issueId) {
    if (submittingRef.current || generation?.status === "running") {
      setError("请等待回复结束或停止生成，再关联需求。");
      return false;
    }
    const s = sessionRef.current;
    if (currentId.current && s?.id !== currentId.current) {
      setError("会话仍在加载，请稍后关联需求。");
      return false;
    }
    setError("");
    if (!s) {
      setDraftIssueId(issueId);
      const url = new URL(location.href);
      if (issueId) url.searchParams.set("issueId", issueId);
      else url.searchParams.delete("issueId");
      history.replaceState({}, "", url);
      return true;
    }
    try {
      const saved = await write(
        `${sessionPath(s.id)}/issue`,
        { issueId, updatedAt: s.updatedAt },
        "PUT",
      );
      if (currentId.current === s.id) {
        sessionRef.current = saved;
        setSession(saved);
      }
      await refreshSessions();
      return true;
    } catch (e) {
      setError(e.message);
      return false;
    }
  }
  async function selectSandbox(id) {
    const current = sessionRef.current;
    if (submittingRef.current || sandboxBusyRef.current || generation?.status === "running") return false;
    if (current?.runId) {
      setError("任务会话使用任务的运行环境，不能切换。"); return false;
    }
    if (!currentId.current) { setSandboxProfileId(id); return true; }
    if (!current || current.id !== currentId.current) return false;
    sandboxBusyRef.current = true;
    setSandboxBusy(true);
    setError("");
    try {
      const saved = await write(`${sessionPath(current.id)}/sandbox`, { sandboxProfileId: id }, "PUT");
      if (currentId.current === current.id) { sessionRef.current = saved; setSession(saved); }
      return true;
    } catch (e) { setError(e.message); return false; }
    finally { sandboxBusyRef.current = false; setSandboxBusy(false); }
  }
  useEffect(() => {
    let current = true;
    setWorkspaceSandbox(null);
    const id = session?.workspaceRunId || session?.runId;
    if (id) void api(`/api/runs/${encodeURIComponent(id)}`).then((run) => {
      if (current) setWorkspaceSandbox(run.workflowSnapshot?.sandboxSnapshot?.profile || { id: run.workflowSnapshot?.sandboxProfileId });
    }).catch(() => {});
    return () => { current = false; };
  }, [session?.workspaceRunId, session?.runId]);
  async function refresh() {
    const sandboxRows = await api("/api/sandboxes").catch(() => null);
    if (Array.isArray(sandboxRows)) setSandboxes(sandboxRows);
    const projectRows = await api("/api/projects").catch(() => null);
    if (Array.isArray(projectRows)) setProjects(projectRows);
    if (activeId) {
      const stored = await api(sessionPath(activeId));
      if (currentId.current === activeId) setSession(stored);
    }
    await refreshSessions();
  }
  return {
    user,
    users,
    issueId: session?.issueId || (!activeId ? draftIssueId : ""),
    linkIssue,
    projects,
    sandboxes,
    sandboxProfileId: activeId ? session?.sandboxProfileId || "" : sandboxProfileId,
    sandboxLocked: !!session?.runId,
    sandboxBusy,
    workspaceSandbox,
    selectSandbox,
    models: executorModels,
    executor,
    executors: EXECUTORS,
    setExecutor: chooseExecutor,
    sessions,
    session,
    activeId,
    revealId,
    projectId,
    model: activeModel,
    effort: activeEffort,
    setEffort,
    sponsored,
    sponsoredModel,
    sponsoredModels,
    generation,
    live,
    error,
    ready,
    submitting,
    busy: submitting || generation?.status === "running",
    queue: (activeId && queues[activeId]?.items) || [],
    queueHeld: !!(activeId && queues[activeId]?.held),
    removeQueued: removeQueuedMessage,
    resumeQueue,
    sendNow,
    steering: (steering?.sessionId === activeId && steering.item) || null,
    readOnly: !!session?.readOnly,
    setError,
    setModel: chooseModel,
    setSession,
    open,
    newChat,
    selectProject,
    send,
    stop,
    rename,
    archive,
    remove,
    edit,
    regenerate,
    applyDraft,
    refresh,
    refreshSessions,
  };
}
