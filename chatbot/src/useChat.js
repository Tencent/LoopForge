import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
import { cliFailureSettles } from "./cli-install.jsx";
import { useExecutionMachines } from "./useExecutionMachines.js";
import { executionLocked } from "./execution-target.js";
import { knownEffort, modelEffort } from "./effort.js";
import { EXECUTORS, concreteModel, conversationEngine, engineModels, engineOfModel, personalEngineConfigured } from "./engines.js";
import { readComposerPreference, useComposerOptions, writeComposerPreference } from "./composer-preferences.js";
import {
  enqueue,
  holdQueue,
  nextQueued,
  queuedApproval,
  queuedMessage,
  readQueues,
  removeQueued,
  restoreQueued,
  writeQueues,
} from "./message-queue.js";
function knownExecutor(id) {
  if (id === "claude") return "claude-code";
  return EXECUTORS.some((item) => item.id === id) ? id : "codebuddy";
}
function storedExecutor() {
  return knownExecutor(readComposerPreference("executor"));
}

// A CLI's fallback list uses bare model IDs, while the personal catalog
// prefixes them with the CLI. Both spellings share the same preference.
function unprefixedModel(executor, model) {
  const prefix = executor + ":";
  return model?.startsWith(prefix) ? model.slice(prefix.length) : model;
}

function findExecutorModel(list, executor, model) {
  const id = unprefixedModel(executor, model);
  if (!id) return undefined;
  return list.find((item) => unprefixedModel(executor, item.id) === id) ??
    list.find((item) => item.efforts?.some((level) => item.effortModelIds?.[level] === id));
}

function effortPreference(executor, model) {
  return `effort.${executor}.${unprefixedModel(executor, model)}`;
}

// Use the first requested preference that is available for this executor.
function preferredModel(list, executor, ...preferences) {
  const available = list.filter((model) => !model.disabled && !model.unavailable);
  for (const id of preferences) {
    if (!concreteModel({ id }, executor)) continue;
    const listed = findExecutorModel(available, executor, id);
    if (listed) return id;
  }
  return available[0]?.id || "";
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

class ComposerModelError extends Error {}

export function useChat() {
  const stopRequestRef = useRef(null);
  const accountDefaultsRef = useRef(null);
  const draftExecutorPicked = useRef(false);
  const userRead = useRef(0);
  const [identity, setIdentity] = useState(null),
    [projects, setProjects] = useState([]),
    [sandboxes, setSandboxes] = useState([]),
    [sandboxProfileId, setSandboxProfileId] = useState(""),
    [sandboxBusy, setSandboxBusy] = useState(false),
    [workspaceSandbox, setWorkspaceSandbox] = useState(null),
    [workspaceExecution, setWorkspaceExecution] = useState(null),
    [workspaceExecutionError, setWorkspaceExecutionError] = useState(""),
    [users, setUsers] = useState([]),
    [draftIssueId, setDraftIssueId] = useState(
      new URLSearchParams(location.search).get("issueId") || "",
    ),
    [models, setModels] = useState([]),
    [catalogEngines, setCatalogEngines] = useState([]),
    [accountDefaults, setAccountDefaults] = useState(null),
    [modelCatalogRefresh, requestModelCatalogRefresh] = useState(0),
    [sessions, setSessions] = useState([]),
    [session, setSession] = useState(null),
    [activeId, setActiveId] = useState(
      new URLSearchParams(location.search).get("session") || null,
    ),
    // The last conversation reached by opening it rather than by creating it;
    // the header reveals that conversation's title once it has loaded.
    [revealId, setRevealId] = useState(activeId),
    [projectId, setProjectId] = useState(initialProject),
    [selection, setSelection] = useState(() => ({ executor: storedExecutor(), model: null, effort: null })),
    [codeBuddyDefault, setCodeBuddyDefault] = useState({ id: "default", name: "默认" }),
    [sponsoredModels, setSponsoredModels] = useState([]),
    [providerCatalogs, setProviderCatalogs] = useState({}),
    [generation, setGeneration] = useState(null),
    [stopRequest, setStopRequest] = useState(null),
    [live, setLive] = useState({ answer: "", thinking: "", tools: [] }),
    [error, setError] = useState(""),
    [ready, setReady] = useState(false),
    [submitting, setSubmitting] = useState(false),
    [steerOnSend, setSteerOnSend] = useState(false),
    // Messages sent while a reply runs, per conversation (see message-queue.js).
    [queues, setQueues] = useState(readQueues),
    // The generation whose final transcript has been loaded. A queued message
    // waits for it, so it is sent after the finished (or unfinished) reply.
    [settledId, setSettledId] = useState(null),
    // The conversation whose reply status has been fetched since it was
    // opened. Until then a reply may still be running there, so queued
    // messages must not go out (they would be rejected and get lost).
    [generationKnown, setGenerationKnownState] = useState(null),
    // A queued message sent with "send now": it interrupts the running reply
    // and stays on screen until the server's follow-up turn has started.
    [steering, setSteering] = useState(null);
  // The users endpoint returns a complete snapshot. Credentials omitted
  // after deletion must not survive in the authenticated user's profile.
  const user = useMemo(() => identity && { ...identity, ...users.find(row => row.id === identity.id) }, [identity, users]);
  const generationKnownRef = useRef(null);
  const generationRevision = useRef(0);
  const setGenerationKnown = useCallback((id) => {
    generationKnownRef.current = id;
    setGenerationKnownState(id);
  }, []);
  const { readOptions, saveOptions } = useComposerOptions(user?.isGuest ? "guest" : user?.id);
  const executionMachines = useExecutionMachines(
    user?.isGuest ? null : user?.id,
    submitting || generation?.status === "running",
    !!session?.readOnly,
    setError,
    bindExecutionMachine,
    session?.executionTarget,
  );
  const currentId = useRef(activeId),
    sessionRef = useRef(session),
    projectRef = useRef(projectId),
    globalProject = useRef(initialProject),
    submittingRef = useRef(false),
    savingDraft = useRef(null),
    catalogRequests = useRef(new Map()),
    sandboxBusyRef = useRef(false),
    applyingRef = useRef(new Set()),
    dispatchingRef = useRef(false),
    steeringRef = useRef(null),
    selectRef = useRef(null);
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
      generationRevision.current++;
      setGeneration(null);
      setGenerationKnown(null);
      setSettledId(null);
      setLive({ answer: "", thinking: "", tools: [] });
      sessionRef.current = null;
      setSession(null);
      executionMachines.resetMachine();
      draftExecutorPicked.current = false;
      setSelection((options) => ({ executor: id ? options.executor : accountDefaultsRef.current?.executor || storedExecutor(), model: null, effort: null }));
      setSandboxProfileId("");
      currentId.current = id;
      setActiveId(id);
      if (id) setRevealId(id);
      changeURL(id);
    },
    [changeURL, executionMachines.resetMachine, setGenerationKnown],
  );
  const newChat = useCallback(() => {
    setDraftIssueId("");
    open(null);
  }, [open]);
  function restoreConversationModel(stored, { running = false, preserveChoice = false } = {}) {
    const picked = readOptions(`model.${stored.id}`);
    const recordedExecutor = knownExecutor(stored.executor || engineOfModel(stored.model, models));
    const locked = conversationEngine(stored.messages, models, recordedExecutor) ||
      (running ? recordedExecutor : "");
    const engine = knownExecutor(locked || picked.executor || recordedExecutor);
    const selection = picked.executor === engine ? picked : stored;
    const choice = { executor: engine, model: selection.model || null, effort: selection.effort ?? "" };
    setSelection((current) => {
      if (!preserveChoice || (locked && current.executor !== engine) ||
          (current.executor === engine && picked.executor !== engine)) return choice;
      return current;
    });
    return choice;
  }
  function receiveSession(stored, running = false) {
    if (!stored || currentId.current !== stored.id) return;
    const opening = sessionRef.current?.id !== stored.id;
    restoreConversationModel(stored, { running, preserveChoice: !opening });
    if (opening) {
      changeURL(stored.id);
      setProjectId(stored.projectId || "");
      syncProject(stored.projectId || "");
    }
    sessionRef.current = stored;
    setSession(stored);
  }
  const applyModelCatalog = useCallback((catalog) => {
    accountDefaultsRef.current = catalog.preferences || null;
    setAccountDefaults(accountDefaultsRef.current);
    setModels(catalog.models || []);
    setCatalogEngines(catalog.engines || []);
    setCodeBuddyDefault({ id: catalog.default || "default", name: "默认", efforts: catalog.defaultEfforts });
    setSponsoredModels(sponsoredModelList(catalog));
  }, []);
  useEffect(() => {
    if (!user?.id || user.isGuest) {
      setSteerOnSend(false);
      return;
    }
    let current = true, revision = 0;
    const refresh = () => {
      const requested = ++revision;
      return api("/api/assistant/preferences")
        .then((prefs) => {
          if (current && requested === revision) setSteerOnSend(!!prefs?.steerOnSend);
        })
        .catch(() => {});
    };
    const onMessage = (event) => {
      if (event.source === window.parent && event.origin === location.origin &&
          event.data?.type === "agentflow-assistant-preferences-changed")
        void refresh();
    };
    const onVisibilityChange = () => {
      if (document.visibilityState === "visible") void refresh();
    };
    void refresh();
    document.addEventListener("visibilitychange", onVisibilityChange);
    window.addEventListener("message", onMessage);
    return () => {
      current = false;
      document.removeEventListener("visibilitychange", onVisibilityChange);
      window.removeEventListener("message", onMessage);
    };
  }, [user?.id, user?.isGuest]);
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
        setIdentity(auth.user);
        setUsers(users || []);
        setProjects(projectRows || []);
        setSandboxes(sandboxRows || []);
        applyModelCatalog(catalog);
        if (catalog.cursorModelsPending) requestModelCatalogRefresh((revision) => revision + 1);
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
  }, [applyModelCatalog, refreshSessions]);
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
        if (!current || currentId.current !== activeId || sessionRef.current?.id === activeId) return;
        receiveSession(stored);
        const recovered = connectionError;
        setError((old) => (old === recovered ? "" : old));
      } catch (e) {
        if (!current || currentId.current !== activeId || sessionRef.current?.id === activeId) return;
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
    const isCurrent = (revision) =>
      !cancelled && currentId.current === activeId && generationRevision.current === revision;
    const schedulePoll = (delay) => {
      if (cancelled || currentId.current !== activeId) return;
      clearTimeout(timer);
      timer = setTimeout(poll, delay);
    };
    const consume = async (g, revision) => {
      if (!isCurrent(revision)) return;
      if (connectionError) {
        const recovered = connectionError;
        connectionError = "";
        setError((current) => (current === recovered ? "" : current));
      }
      const running = g.status === "running";
      const needsTranscript = running ? genId !== g.id : loaded !== g.id;
      if (needsTranscript) {
        // Pause sends until the transcript and CLI for this turn are loaded.
        // A new turn can come from steering or from another member.
        setGenerationKnown(null);
        if (!running) {
          source?.close();
          source = null;
        }
        const stored = await api(sessionPath(activeId));
        if (!isCurrent(revision)) return;
        receiveSession(stored, running);
        if (!running) {
          loaded = g.id;
          setSettledId(g.id);
        }
      }
      if (genId !== g.id) {
        genId = g.id;
        cursor = 0;
        accumulated = { answer: "", thinking: "", tools: [] };
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
      setGenerationKnown(activeId);
      if (!running) setStopRequest((old) => old?.generationId === g.id ? null : old);
      if (running && ["platform", "workspace"].includes(g.runtimeMode)) {
        setSession((stored) => stored?.id === activeId && stored.runtimeMode !== g.runtimeMode ? { ...stored, runtimeMode: g.runtimeMode } : stored);
      }
      if (!running && needsTranscript) {
        await refreshSessions();
        if (isCurrent(revision) && g.error && !["canceled", "cancelled"].includes(g.status))
          setError(g.error);
      }
    };
    const connect = (revision) => {
      if (source || cancelled || typeof EventSource === "undefined")
        return false;
      source = new EventSource(`${sessionPath(activeId)}/generation?stream=1`);
      const stream = source;
      const close = () => {
        if (source !== stream) return;
        stream.close();
        source = null;
      };
      let updates = Promise.resolve();
      stream.addEventListener("generation", (e) => {
        const g = JSON.parse(e.data);
        updates = updates
          .then(async () => {
            if (cancelled || source !== stream) return;
            await consume(g, revision);
            if (!isCurrent(revision)) {
              close();
              schedulePoll(200);
            } else if (g.status !== "running") schedulePoll(2000);
          })
          .catch((error) => {
            if (cancelled || (source && source !== stream)) return;
            close();
            if (isCurrent(revision)) setError(error.message);
            schedulePoll(2000);
          });
      });
      stream.onerror = () => {
        stream.close();
        void updates.then(() => {
          if (cancelled || source !== stream) return;
          close();
          schedulePoll(500);
        });
      };
      return true;
    };
    const poll = async () => {
      let generationFound = false;
      const revision = generationRevision.current;
      try {
        if (submittingRef.current) {
          schedulePoll(500);
          return;
        }
        const g = await api(
          `${sessionPath(activeId)}/generation?after=${cursor}`,
        );
        if (!isCurrent(revision)) return schedulePoll(200);
        generationFound = true;
        await consume(g, revision);
        if (!isCurrent(revision)) return schedulePoll(200);
        if (g.status === "running" && connect(revision)) return;
        schedulePoll(g.status === "running" ? 200 : 2000);
      } catch (e) {
        if (!isCurrent(revision)) return schedulePoll(200);
        // No reply was ever started here: nothing can be running.
        if (e.status === 404 && !generationFound) setGenerationKnown(activeId);
        else {
          connectionError = e.status ? e.message : "连接暂时中断，正在重连…";
          setError(connectionError);
        }
        schedulePoll(2000);
      }
    };
    void poll();
    return () => {
      cancelled = true;
      clearTimeout(timer);
      source?.close();
    };
  }, [activeId, ready, refreshSessions, setGenerationKnown]);
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
  // Starts a new conversation in a project without binding or leaving the
  // conversation that is open now (selectProject would bind a personal one).
  const startProjectDraft = useCallback(
    (id) => {
      newChat();
      if (id === projectRef.current) return;
      setSandboxProfileId("");
      setProjectId(id);
      syncProject(id);
    },
    [newChat, syncProject],
  );
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
  const missingKey = !!user && !user.codeBuddyApiKeyConfigured;
  // Without their own key users chat on the platform's shared key, limited to
  // the models the administrator allowed (the first is the default).
  const sponsoredModel = sponsoredModels[0] || "";
  function composerRunnerId(conversation) {
    if (!executionLocked(conversation)) return executionMachines.defaultRunnerId;
    return conversation.executionTarget?.pool === "runner" ? conversation.executionTarget.runnerId : "";
  }
  const runnerId = composerRunnerId(session);
  const runner = executionMachines.machines.find(machine => machine.id === runnerId && machine.ownerUserId === user?.id);
  const availableExecutors = EXECUTORS.flatMap(item => {
    const local = ["codex", "claude-code", "agy"].includes(item.id) &&
      runner?.runtimes?.some(runtime => runtime.provider === item.id && runtime.authMode !== "personal");
    if (local) return [{ ...item, detail: "使用执行机器上的本机登录态。" }];
    const configured = catalogEngines.find(engine => engine.id === item.id)?.configured ??
      personalEngineConfigured(user, item.id);
    return configured || (item.id === "codebuddy" && sponsoredModel) ? [item] : [];
  });
  // Drafts can use any available executor. A recorded conversation keeps its
  // executor even if its authentication changes.
  const availableExecutor = id => availableExecutors.find(item => item.id === id);
  const preferred = !activeId && !draftExecutorPicked.current && availableExecutor(accountDefaults?.executor);
  const keepSessionExecutor = activeId && (session?.id !== activeId || submitting ||
    generation?.status === "running" || conversationEngine(session?.messages, models, session?.executor) ||
    (session?.executionTarget?.pool === "runner" && !executionMachines.machinesReady));
  const executor = keepSessionExecutor || !user || user.isGuest ? selection.executor :
    (preferred || availableExecutor(selection.executor) || availableExecutors[0])?.id || "";
  const { model, effort } = executor === selection.executor ? selection : { model: null, effort: null };
  function resolveComposerModel(
    { executor, model, effort }, conversation, draft,
    catalogs = providerCatalogs, machines = executionMachines.machines,
  ) {
    const personalMachineId = composerRunnerId(conversation);
    const personalMachine = machines.find(
      (machine) => machine.id === personalMachineId && machine.ownerUserId === user?.id,
    );
    const agyRuntime = personalMachine?.runtimes?.find((runtime) => runtime.provider === "agy");
    const agyAccountKey = executor === "agy" && agyRuntime && agyRuntime.authMode !== "personal"
      ? `agy-account:${user.id}:${personalMachine.id}` : "";
    const providerCatalog = catalogs[agyAccountKey || executor];
    const codeBuddy = executor === "codebuddy";
    const listed = engineModels(models, executor);
    const usesProviderCatalog = !!executor && !codeBuddy && (!!agyAccountKey || !listed.length);
    const catalogToLoad = usesProviderCatalog && !providerCatalog ? agyAccountKey || executor : "";
    const executorModels = usesProviderCatalog
      ? (providerCatalog?.models || []).filter((model) => concreteModel(model, executor))
      : listed;
    const sponsored = codeBuddy && missingKey && !user.isGuest && !!sponsoredModel;
    const listedModel = findExecutorModel(executorModels, executor, model);
    const concrete = concreteModel({ id: model }, executor);
    const hasSelectedModel = concrete && !listedModel?.disabled && !listedModel?.unavailable &&
      (conversation || !executorModels.length || listedModel);
    const inheritsDefault = !!conversation && !concrete;
    const saved = (draft || inheritsDefault) && !sponsored && accountDefaults?.executor === executor ? accountDefaults : null;
    const savedModel = saved ? findExecutorModel(executorModels, executor, saved.model) : null;
    const selectedModel = hasSelectedModel ? model : preferredModel(
      executorModels, executor,
      savedModel?.id,
      inheritsDefault ? (codeBuddy ? codeBuddyDefault.id : providerCatalog?.default) : null,
      draft && !saved ? readComposerPreference(`model.${executor}`) : null,
    );
    const availableModel = sponsored && !sponsoredModels.includes(selectedModel) ? sponsoredModel : selectedModel;
    const catalogEntry = findExecutorModel(executorModels, executor, availableModel);
    const activeModel = catalogEntry?.id ?? availableModel;
    const requestedModelId = unprefixedModel(executor, availableModel);
    const unavailableAgyVariant = agyAccountKey && providerCatalog && !catalogEntry &&
      /-(?:low|medium|high|xhigh|max)$/.test(requestedModelId || "");
    // Older sessions and preferences stored the ID of the selected effort variant.
    const legacyEffort = requestedModelId !== unprefixedModel(executor, activeModel)
      ? catalogEntry?.efforts?.find((level) => catalogEntry.effortModelIds?.[level] === requestedModelId)
      : undefined;
    // null follows the browser preference, while "" is the CLI's default level.
    const preferredEffort = (!inheritsDefault && (model === null || hasSelectedModel) ? effort : null) ??
      (savedModel?.id === availableModel && saved ? saved.effort ?? "" :
        draft ? readComposerPreference(effortPreference(executor, availableModel)) : null);
    // Custom models can remain usable without a picker entry. Keep their
    // saved effort visible and let the server validate it on send.
    const historical = conversation && activeModel && !unavailableAgyVariant && availableModel === selectedModel;
    const activeModelEntry = catalogEntry ?? (historical
      ? { id: activeModel, efforts: [knownEffort(preferredEffort)] }
      : undefined);
    const activeEffort = modelEffort(activeModelEntry, preferredEffort || legacyEffort);
    return {
      personalMachineId, agyAccountKey, catalogToLoad, codeBuddy, executorModels,
      sponsored, activeModelEntry, activeModel, activeEffort, unavailableAgyVariant,
    };
  }
  const composerModel = resolveComposerModel({ executor, model, effort }, session, !activeId);
  const {
    personalMachineId, agyAccountKey, catalogToLoad, codeBuddy, executorModels,
    sponsored, activeModelEntry, activeModel, activeEffort,
  } = composerModel;
  const loadProviderCatalog = useCallback((key, executor, runnerId) => {
    const existing = catalogRequests.current.get(key);
    if (existing) return existing;
    const request = runnerId
      ? write(`/api/runners/${encodeURIComponent(runnerId)}/models/probe`, { provider: "agy", useSaved: true })
      : api(`/api/assistant/models?executor=${encodeURIComponent(executor)}`);
    const loading = request
      .then((catalog) => {
        if (catalogRequests.current.get(key) !== loading)
          throw new ComposerModelError("模型目录已更新，请重新发送。");
        if (catalog.error) throw new Error(catalog.error);
        const result = runnerId ? {
          models: (catalog.models || []).filter((model) => concreteModel(model, "agy")).map((model) => ({
            id: `agy:${model.id}`,
            engine: "agy",
            name: model.name || model.id,
            efforts: model.reasoningEfforts ?? model.discoveredReasoningEfforts,
            effortModelIds: model.efforts,
          })),
        } : catalog;
        setProviderCatalogs((old) => ({ ...old, [key]: result }));
        return result;
      })
      .catch((error) => {
        if (catalogRequests.current.get(key) === loading) catalogRequests.current.delete(key);
        throw error;
      });
    catalogRequests.current.set(key, loading);
    return loading;
  }, []);
  useEffect(() => {
    if (!catalogToLoad) return;
    let current = true;
    void loadProviderCatalog(catalogToLoad, executor, agyAccountKey ? personalMachineId : "")
      .catch((e) => { if (current) setError(e.message); });
    return () => {
      current = false;
    };
  }, [agyAccountKey, catalogToLoad, executor, personalMachineId, providerCatalogs, loadProviderCatalog]);

  function composerModelError(resolved) {
    if (resolved.unavailableAgyVariant) return "当前模型已不在该执行机器的账号目录，请重新选择模型。";
    if (!resolved.activeModel) return "当前 CLI 暂无可用模型，请在个人配置中获取或添加模型。";
    return "";
  }
  async function modelForRequest(selection, conversation, resolved) {
    const visibleModel = resolved.activeModel;
    const visibleEffort = resolved.activeEffort;
    let machines = executionMachines.machines;
    if (selection.executor === "agy" && resolved.personalMachineId && !executionMachines.machinesReady) {
      const rows = await api("/api/runners?mine=1");
      machines = Array.isArray(rows) ? rows : [];
      resolved = resolveComposerModel(selection, conversation, false, providerCatalogs, machines);
    }
    const key = resolved.catalogToLoad;
    if (key) {
      const runnerId = resolved.agyAccountKey ? resolved.personalMachineId : "";
      const catalog = await loadProviderCatalog(key, selection.executor, runnerId);
      resolved = resolveComposerModel(selection, conversation, false, { ...providerCatalogs, [key]: catalog }, machines);
    }
    if (visibleModel) {
      const catalogModel = findExecutorModel(resolved.executorModels, selection.executor, visibleModel);
      if (unprefixedModel(selection.executor, catalogModel?.id ?? visibleModel) !==
          unprefixedModel(selection.executor, resolved.activeModel) ||
          (visibleEffort && visibleEffort !== resolved.activeEffort)) {
        throw new ComposerModelError("当前执行环境不支持所选的模型或思考强度，请重新选择。");
      }
    }
    const error = composerModelError(resolved);
    if (error) throw new ComposerModelError(error);
    return { model: resolved.activeModel, effort: resolved.activeEffort };
  }
  function sessionLoading() {
    return !!currentId.current && sessionRef.current?.id !== currentId.current;
  }
  function replyStatePending() {
    const id = currentId.current;
    if (!id) return false;
    return generationKnown !== id || generationKnownRef.current !== id ||
      (!!generation && generation.status !== "running" && settledId !== generation.id);
  }

  function chooseExecutor(id) {
    if (id === "claude") id = "claude-code";
    if (sessionLoading() || replyStatePending() || submittingRef.current || generation?.status === "running" ||
        !availableExecutor(id) || id === executor) return;
    const current = sessionRef.current;
    const locked = conversationEngine(current?.messages, models, current?.executor);
    writeComposerPreference("executor", id);
    if (locked && locked !== id) open(null);
    else if (current?.id === activeId) saveOptions(`model.${activeId}`, { executor: id });
    draftExecutorPicked.current = true;
    setSelection({ executor: id, model: null, effort: null });
  }

  function isDraftVisible(draft) {
    return draft && !currentId.current && projectRef.current === draft.projectId &&
      (!draft.isActive || draft.isActive());
  }

  function rememberModelChoice(id, level) {
    writeComposerPreference("executor", executor);
    writeComposerPreference(`model.${executor}`, id);
    writeComposerPreference(effortPreference(executor, id), level);
    const choice = { executor, model: id, effort: level };
    const draft = savingDraft.current;
    if (sessionRef.current?.id === activeId) saveOptions(`model.${activeId}`, choice);
    else if (isDraftVisible(draft) && draft.issueId === draftIssueId)
      draft.choice = choice;
  }

  function chooseModel(id) {
    if (sessionLoading() || replyStatePending()) return false;
    if (sponsored && !sponsoredModels.includes(id)) {
      setError(sponsoredModelMessage(sponsoredModels));
      return false;
    }
    const entry = executorModels.find((item) => item.id === id);
    if (!entry || entry.disabled || entry.unavailable) return false;
    const savedLevel = readComposerPreference(effortPreference(executor, id));
    const level = id === activeModel ? activeEffort : modelEffort(entry, savedLevel ?? activeEffort);
    setSelection({ executor, model: id, effort: level });
    rememberModelChoice(id, level);
    return true;
  }

  function chooseEffort(value) {
    if (sessionLoading() || replyStatePending()) return;
    const level = modelEffort(activeModelEntry, value);
    setSelection({ executor, model: activeModelEntry ? activeModel : model, effort: level });
    if (activeModelEntry) {
      rememberModelChoice(activeModel, level);
    }
  }
  const refreshUser = useCallback(async () => {
    const read = ++userRead.current;
    try {
      const rows = await api("/api/users");
      if (read !== userRead.current) return;
      setUsers(rows || []);
      const fresh = rows?.find(row => row.id === identity?.id);
      // Drop a "please configure X" error once X is configured.
      setError(old => {
        const hint = credentialHint(old);
        return hint && fresh?.[`${hint.field}Configured`] ? "" : old;
      });
    } catch {}
  }, [identity?.id]);
  const refreshCredentials = useCallback(() => {
    catalogRequests.current.clear();
    setProviderCatalogs({});
    requestModelCatalogRefresh((revision) => revision + 1);
  }, []);
  useEffect(() => {
    const onMessage = (e) => {
      if (
        e.source === window.parent &&
        e.origin === location.origin &&
        e.data?.type === "agentflow-credentials-changed"
      ) {
        refreshCredentials();
      }
    };
    const onFocus = () => requestModelCatalogRefresh(revision => revision + 1);
    window.addEventListener("message", onMessage);
    if (ready && !user?.isGuest) window.addEventListener("focus", onFocus);
    return () => {
      window.removeEventListener("message", onMessage);
      window.removeEventListener("focus", onFocus);
    };
  }, [ready, user?.isGuest, refreshCredentials]);
  useEffect(() => {
    if (!ready || !modelCatalogRefresh) return;
    void refreshUser();
    const request = new AbortController();
    // Cursor discovery can run for up to 30 seconds in the background.
    const deadline = Date.now() + 40000;
    let timer;
    async function refreshModels() {
      try {
        const catalog = await api("/api/assistant/models", { signal: request.signal });
        if (request.signal.aborted) return;
        applyModelCatalog(catalog);
        if (!catalog.cursorModelsPending) return;
      } catch (error) {
        if (error.status >= 400 && error.status < 500) return;
      }
      if (!request.signal.aborted && Date.now() < deadline) timer = setTimeout(refreshModels, 1500);
    }
    void refreshModels();
    return () => {
      request.abort();
      clearTimeout(timer);
    };
  }, [ready, modelCatalogRefresh, refreshUser, applyModelCatalog]);
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
      // The POST can still reject the message after the reply finishes.
      if (s.pending) return;
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
    if (replyStatePending()) return;
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
    if (!item || replyStatePending() || steeringRef.current || dispatchingRef.current) return false;
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
    return steerMessage(id, item);
  }
  async function steerMessage(id, item) {
    const from = generation.id;
    const conversation = sessionRef.current;
    const request = { sessionId: id, generationId: from, item, pending: true };
    steeringRef.current = request;
    setSteering({ sessionId: id, item });
    setQueues((old) => removeQueued(old, id, item.id));
    setError("");
    try {
      const chosen = await modelForRequest({ executor, model, effort }, conversation, composerModel);
      await write(
        `${sessionPath(id)}/generation/steer?generationId=${encodeURIComponent(from)}`,
        {
          content: item.text || "请阅读本轮附件并回答。",
          attachments: item.attachments || [],
          references: item.references || [],
          requireExactModel: true,
          ...chosen,
          ...queuedApproval(item, user?.id, conversation?.projectId || ""),
        },
      );
      request.pending = false;
      if (steeringRef.current === request) setSteering({ sessionId: id, item });
      return true;
    } catch (e) {
      if (steeringRef.current === request) {
        steeringRef.current = null;
        setSteering(null);
      }
      const turnEnded = e.status === 409 &&
        /^(当前没有进行中的回复|这一轮已经结束，请重新发送|生成已结束或不存在)$/.test(e.message);
      const held = e instanceof ComposerModelError || (!turnEnded && e.status >= 400 && e.status < 500);
      setQueues((old) => restoreQueued(old, id, item, held));
      if (!turnEnded && currentId.current === id) setError(e.message);
      return false;
    }
  }
  async function send({
    text,
    attachments = [],
    references = [],
    approvalMode = "confirm",
    approvalUserId,
    approvalProjectId,
    onSessionCreated,
    isDraftActive,
    messagesOverride = null,
    fromQueue = false,
  }) {
    if (!text.trim() && !attachments.length) return false;
    if (sessionLoading() || replyStatePending()) {
      setError("会话仍在加载，请稍候再发送。");
      return false;
    }
    if (!executor) return false;
    const currentProjectId = sessionRef.current?.projectId || "";
    const approval = fromQueue
      ? queuedApproval({ approvalMode, approvalUserId, approvalProjectId }, user?.id, currentProjectId)
      : { approvalMode, approvalUserId: user?.id };
    if (
      submittingRef.current ||
      generation?.status === "running" ||
      steeringRef.current?.sessionId === currentId.current
    ) {
      const modelError = composerModelError(composerModel);
      if (modelError) {
        setError(modelError);
        return false;
      }
      // Internal follow-ups (confirmed drafts) never queue or interrupt.
      if (messagesOverride || fromQueue || !currentId.current) {
        setError("当前回复仍在生成，请稍后再试。");
        return false;
      }
      const id = currentId.current;
      const item = queuedMessage({ text, attachments, references, ...approval, approvalProjectId: currentProjectId });
      if (steerOnSend && generation?.status === "running" && generation.id &&
          !submittingRef.current && !steeringRef.current && !dispatchingRef.current &&
          !stopRequestRef.current) {
        // Failed steering restores the message to the queue, so the composer
        // can clear without losing it. Further sends wait for this handoff.
        await steerMessage(id, item);
        return true;
      }
      setQueues((old) => enqueue(old, id, item));
      setError("");
      return true;
    }
    if (sandboxBusyRef.current) { setError("运行环境仍在保存，请稍后发送。"); return false; }
    if (executionMachines.machineSavingRef.current) { setError("执行机器仍在保存，请稍后发送。"); return false; }
    if (!executionLocked(sessionRef.current) && !executionMachines.machinesReady) {
      setError(executionMachines.machineError || "执行机器仍在加载，请稍后发送。");
      return false;
    }
    // Claim the send and its target before a credential check can yield.
    let s = sessionRef.current?.id === currentId.current ? sessionRef.current : null;
    const draft = s ? null : {
      projectId: projectRef.current, issueId: draftIssueId, isActive: isDraftActive, choice: null,
    };
    savingDraft.current = draft;
    const viewingTarget = () => s ? currentId.current === s.id : isDraftVisible(draft);
    generationRevision.current++;
    submittingRef.current = true;
    setSubmitting(true);
    setError("");
    const previousGeneration = generation?.id;
    let clientRequestId;
    setGeneration(null);
    try {
      // Recheck credentials before creating the session in case the user
      // configured a key elsewhere after this tab loaded.
      const account = users.find((u) => u.id === user?.id) ?? user;
      if (codeBuddy && user && !user.isGuest && !sponsoredModel && !account?.codeBuddyApiKeyConfigured) {
        const read = userRead.current;
        const rows = await api("/api/users").catch(() => null);
        if (rows && read === userRead.current) setUsers(rows);
        const fresh = rows?.find((u) => u.id === user.id) ?? account;
        if (!fresh?.codeBuddyApiKeyConfigured) {
          if (viewingTarget()) setError("请先在个人配置中配置自己的 CodeBuddy API Key");
          return false;
        }
        requestModelCatalogRefresh((revision) => revision + 1);
        if (!composerModel.activeModel) {
          if (viewingTarget()) setError("CodeBuddy 凭证已更新，正在刷新模型列表，请稍后重试。");
          return false;
        }
      }
      const chosen = await modelForRequest({ executor, model, effort }, s, composerModel);
      if (!s) {
        const id = newId();
        s = await write(
          sessionPath(id),
          {
            id,
            projectId: draft.projectId,
            issueId: draft.issueId,
            sandboxProfileId,
            executionTarget: {pool: executionMachines.defaultRunnerId ? "runner" : "cloud", runnerId: executionMachines.defaultRunnerId || ""},
            ...chosen,
            executor,
            title: text.trim().slice(0, 36) || "图片与文件",
            approvalUserId: approval.approvalUserId,
            messages: [],
          },
          "PUT",
        );
        onSessionCreated?.(s);
        if (draft.choice) saveOptions(`model.${s.id}`, draft.choice);
        if (isDraftVisible(draft)) {
          setSelection(draft.choice ?? { executor, ...chosen });
          currentId.current = s.id;
          setActiveId(s.id);
          changeURL(s.id);
        }
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
      if (viewingTarget()) {
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
      }
      const executionTarget = executionLocked(s) ? s.executionTarget : {
        pool: executionMachines.defaultRunnerId ? "runner" : "cloud",
        runnerId: executionMachines.defaultRunnerId || "",
      };
      clientRequestId = newId();
      if (currentId.current === s.id) setGenerationKnown(null);
      const g = await write("/api/assistant/generations", {
        clientRequestId,
        requireExactModel: true,
        sessionId: s.id,
        projectId: s.projectId || "",
        runId: s.runId || "",
        executor,
        ...chosen,
        ...approval,
        executionTarget,
        messages,
      });
      if (currentId.current === s.id) {
        setSession((old) => old?.id === s.id ? { ...old, executionTargetLocked: true,
          executionTarget: g.executionTarget || executionTarget } : old);
        setGeneration(g);
        setGenerationKnown(s.id);
      }
      await refreshSessions().catch(() => {});
      return true;
    } catch (e) {
      if (clientRequestId) {
        const mayHaveReachedServer = !e.status || e.status >= 500;
        const g = await api(`${sessionPath(s.id)}/generation`).catch(() => null);
        if (g && g.id !== previousGeneration && currentId.current === s.id) {
          setGeneration(g);
          setGenerationKnown(null);
        }
        // The generation itself can acknowledge the send even when loading
        // the transcript fails. The session keeps receipts for earlier turns.
        if (mayHaveReachedServer && g?.clientRequestId === clientRequestId) return true;
        try {
          const stored = await api(sessionPath(s.id));
          receiveSession(stored, g?.status === "running");
          if (g?.status === "running" && currentId.current === s.id) setGenerationKnown(s.id);
          if (mayHaveReachedServer && stored?.recentClientRequestIds?.includes(clientRequestId)) return true;
        } catch {}
      }
      if (viewingTarget()) setError(e.message);
      return false;
    } finally {
      if (savingDraft.current === draft) savingDraft.current = null;
      submittingRef.current = false;
      setSubmitting(false);
    }
  }
  async function stop() {
    if (!activeId || generation?.status !== "running" || generation.cancelRequested ||
      (stopRequestRef.current?.sessionId === activeId && stopRequestRef.current.generationId === generation.id)) return;
    const request = { sessionId: activeId, generationId: generation.id };
    stopRequestRef.current = request;
    setStopRequest(request);
    // Stopping means "wait for me": queued messages stay, but do not go out.
    setQueues((old) => holdQueue(old, activeId, true));
    try {
      await write(
        `${sessionPath(activeId)}/generation/cancel?generationId=${encodeURIComponent(generation?.id || "")}`,
        {},
      );
      if (currentId.current === request.sessionId) setError("");
    } catch (e) {
      if (stopRequestRef.current === request) stopRequestRef.current = null;
      setStopRequest((old) => old === request ? null : old);
      if (currentId.current === request.sessionId) setError(e.message);
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
      saveOptions(`model.${id}`, null);
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
      restoreConversationModel(stored);
      await refreshSessions();
      return stored;
    } catch (e) {
      setError(e.message);
      return null;
    }
  }
  // An edit to a shared branch can return its main conversation. Resolve
  // the generation from that session even before React renders it.
  async function regenerate(stored, approvalMode = "confirm") {
    try {
      generationRevision.current++;
      submittingRef.current = true;
      setSubmitting(true);
      open(stored.id);
      setSession(stored);
      const selection = restoreConversationModel(stored);
      const resolved = resolveComposerModel(selection, stored, false);
      const chosen = await modelForRequest(selection, stored, resolved);
      if (currentId.current === stored.id) setGenerationKnown(null);
      const g = await write("/api/assistant/generations", {
        sessionId: stored.id,
        requireExactModel: true,
        projectId: stored.projectId || "",
        runId: stored.runId || "",
        executor: selection.executor,
        ...chosen,
        messages: stored.messages,
        approvalMode: ["confirm", "assisted", "auto"].includes(approvalMode) ? approvalMode : "confirm",
        approvalUserId: user?.id,
      });
      if (currentId.current === stored.id) {
        setGeneration(g);
        setGenerationKnown(stored.id);
      }
      await refreshSessions();
      return true;
    } catch (e) {
      if (currentId.current === stored.id) setError(e.message);
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
      let stored = await api(sessionPath(id));
      let reply = JSON.parse(stored.messages[messageIndex].content);
      let draft = reply.drafts[draftIndex];
      if (draft.saved || draft.inherited) return null;
      if (generation?.status === "running")
        throw Error("请等待回复结束后再确认操作。");
      if (draft.proposal && !draft.decision) {
        // Accept before running it: once the timeout has handed the work to
        // the assistant, the accept fails and the operation must not run.
        try {
          stored = (
            await write(`${sessionPath(id)}/proposals`, {
              messageIndex,
              draftIndex,
              decision: "accepted",
            })
          ).session;
        } catch (error) {
          void api(sessionPath(id))
            .then((latest) => currentId.current === id && setSession(latest))
            .catch(() => {});
          throw error;
        }
        reply = JSON.parse(stored.messages[messageIndex].content);
        draft = reply.drafts[draftIndex];
      }
      if (draft.proposal && draft.decision !== "accepted") {
        if (currentId.current === id) setSession(stored);
        throw Error("这条建议已经处理过，不能执行。");
      }
      try {
        Object.assign(draft, reviewedOperation(draft, review));
      } catch (error) {
        if (error.draftChanged && currentId.current === id) setSession(stored);
        throw error;
      }
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
  // Answers an assistant proposal card. Declining work the assistant had not
  // started continues with the turn the server returns.
  async function decideProposal(messageIndex, draftIndex, decision, approvalMode = "confirm") {
    const id = activeId;
    if (generation?.status === "running")
      throw Error("请等待回复结束后再处理建议。");
    const key = `${id}:${messageIndex}:${draftIndex}`;
    if (applyingRef.current.has(key)) throw Error("这条建议正在处理，请稍候。");
    applyingRef.current.add(key);
    try {
      const result = await write(`${sessionPath(id)}/proposals`, {
        messageIndex,
        draftIndex,
        decision,
      });
      if (currentId.current !== id) return;
      setSession(result.session);
      if (result.followUp)
        await send({
          text: result.followUp.text,
          messagesOverride: result.session.messages,
          approvalMode,
        });
    } catch (e) {
      setError(e.message);
      throw e;
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
  async function bindExecutionMachine(runnerId) {
    if (submittingRef.current || executionLocked(sessionRef.current)) throw new Error("会话执行机器已固定，请新建对话。");
    if (currentId.current && !sessionRef.current) throw new Error("会话仍在加载，请稍后选择。");
    return {runnerId};
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
    setWorkspaceExecution(null);
    setWorkspaceExecutionError("");
    const id = session?.workspaceRunId || session?.runId;
    if (id) void api(`/api/runs/${encodeURIComponent(id)}`).then((run) => {
      if (current) {
        setWorkspaceSandbox(run.workflowSnapshot?.sandboxSnapshot?.profile || { id: run.workflowSnapshot?.sandboxProfileId });
        setWorkspaceExecution(run.executionContext || {});
      }
    }).catch(() => {
      if (current) setWorkspaceExecutionError("无法加载工作区执行机器");
    });
    return () => { current = false; };
  }, [session?.workspaceRunId, session?.runId]);
  async function refresh() {
    await executionMachines.refreshMachines();
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
  const adoptCLIInstall = useCallback((message) => {
    setError(message);
    setGeneration((current) =>
      (current?.error && cliFailureSettles(current.error)) ||
      (current?.failure?.message && cliFailureSettles(current.failure.message))
        ? { ...current, error: message, failure: null }
        : current,
    );
    setSession((current) => {
      if (
        !current?.messages?.some(
          (item) =>
            cliFailureSettles(item.unfinished) ||
            cliFailureSettles(item.failure?.message),
        )
      )
        return current;
      return {
        ...current,
        messages: current.messages.map((item) =>
          cliFailureSettles(item.unfinished) ||
          cliFailureSettles(item.failure?.message)
            ? { ...item, unfinished: message, failure: null }
            : item,
        ),
      };
    });
  }, []);
  return {
    user,
    defaultApproval: accountDefaults?.approval,
    refreshCredentials,
    users,
    issueId: session?.issueId || (!activeId ? draftIssueId : ""),
    linkIssue,
    projects,
    sandboxes,
    sandboxProfileId: activeId ? session?.sandboxProfileId || "" : sandboxProfileId,
    sandboxLocked: !!session?.runId,
    sandboxBusy,
    workspaceSandbox,
    workspaceExecution,
    workspaceExecutionError,
    ...executionMachines,
    selectSandbox,
    models: executorModels,
    executor,
    executors: user ? availableExecutors : [],
    setExecutor: chooseExecutor,
    sessions,
    session,
    activeId,
    revealId,
    projectId,
    model: activeModel,
    modelDetails: activeModelEntry,
    effort: activeEffort,
    setEffort: chooseEffort,
    sponsored,
    sponsoredModel,
    sponsoredModels,
    generation,
    live,
    error,
    ready,
    replyStatePending: replyStatePending(),
    submitting,
    busy: submitting || generation?.status === "running",
    stopping: generation?.status === "running" && (generation.cancelRequested ||
      (stopRequest?.sessionId === activeId && stopRequest.generationId === generation.id)),
    queue: (activeId && queues[activeId]?.items) || [],
    queueHeld: !!(activeId && queues[activeId]?.held),
    removeQueued: removeQueuedMessage,
    resumeQueue,
    sendNow,
    steering: (steering?.sessionId === activeId && steering.item) || null,
    readOnly: !!session?.readOnly,
    setError,
    adoptCLIInstall,
    setModel: chooseModel,
    setSession,
    open,
    newChat,
    selectProject,
    startProjectDraft,
    send,
    stop,
    rename,
    archive,
    remove,
    edit,
    regenerate,
    applyDraft,
    decideProposal,
    refresh,
    refreshSessions,
  };
}
