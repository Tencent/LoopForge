import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, write, sessionPath, newId, mergeEvents, restoreStream, parseReply } from "./api.js";
import { RichContent } from "./RichContent";
import { ReplyTimeline } from "./ActivityTrace";
import ModelPicker from "./ModelPicker";
import PermissionPicker from "./PermissionPicker";
import ModelProviderIcon, { hasProviderLogo } from "./ModelProviderIcon";
import HarnessIcon from "./HarnessIcon";
import { readThemePreference, resolveTheme } from "./theme.js";
import { conversationEngine, defaultEngine, engineBrand, engineModels, engineOfModel } from "./engines.js";
import {
  CONFIGURE_ROLE, CONTEXT, DRAFT, END, MODEL_KEY, NEW_CONVERSATION, PLACE, READY, REPLY, RESET, SURFACE,
  beginOnCanvas, designerParams, draftSummary, latestSession, postToCanvas, replyDraft, scenarioPage,
} from "./sop-designer.js";
import "./sop-designer.css";

const empty = { answer: "", thinking: "", tools: [], activity: [] };

function readModel() {
  try {
    return localStorage.getItem(MODEL_KEY) || localStorage.getItem("agentflow.assistantModel") || "";
  } catch {
    return "";
  }
}

function rememberModel(id, engine) {
  try {
    localStorage.setItem(MODEL_KEY, id);
    localStorage.setItem(`${MODEL_KEY}.${engine}`, id);
  } catch {}
}

function rememberedModel(engine) {
  try {
    return localStorage.getItem(`${MODEL_KEY}.${engine}`) || "";
  } catch {
    return "";
  }
}

function modelsFor(catalog, engine) {
  const models = catalog.models || [];
  return engine === defaultEngine ? models.filter((m) => !m.engine || m.engine === defaultEngine) : engineModels(models, engine);
}

function DraftNote({ draft, onPlace, onConfigureRole }) {
  return (
    <div className="sop-designer-draft">
      <div className="sop-designer-draft-title">
        <HarnessIcon name="workflow" size={14} aria-hidden="true" />
        <span>{draftSummary(draft)}</span>
      </div>
      {draft.warnings.length ? (
        <ul className="sop-designer-warnings">
          {draft.warnings.map((warning) => <li key={warning}>{warning}</li>)}
        </ul>
      ) : null}
      {onConfigureRole ? (
        <button type="button" className="sop-designer-place" onClick={() => onConfigureRole(draft)}>
          配置并创建角色
        </button>
      ) : null}
      {onPlace ? (
        <button type="button" className="sop-designer-place" onClick={() => onPlace(draft)}>
          重新放到画布
        </button>
      ) : null}
    </div>
  );
}

export default function SopDesigner({ search = location.search, canvas = window.parent }) {
  const initial = useMemo(() => designerParams(search), [search]);
  const [context, setContext] = useState({ projectId: initial.projectId, workflowId: initial.workflowId });
  const currentContext = useRef(context);
  const [catalog, setCatalog] = useState({ models: [], engines: [] });
  const [model, setModel] = useState(readModel);
  const [menu, setMenu] = useState(null);
  const [sessionId, setSessionId] = useState("");
  const [session, setSession] = useState(null);
  const [generation, setGeneration] = useState(null);
  const [live, setLive] = useState(empty);
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [starting, setStarting] = useState(false);
  const [page, setPage] = useState(0);
  const owned = useRef(new Set());
  const scroll = useRef(null);
  const input = useRef(null);
  const busy = starting || generation?.status === "running";

  // Same appearance as the studio around the panel.
  useEffect(() => {
    const apply = () => {
      document.documentElement.dataset.theme = resolveTheme(readThemePreference(localStorage), window.matchMedia?.("(prefers-color-scheme: dark)")?.matches);
    };
    apply();
    window.addEventListener("storage", apply);
    return () => window.removeEventListener("storage", apply);
  }, []);

  // The canvas tells us which SOP it shows; a different SOP has its own
  // conversation.
  useEffect(() => {
    const onMessage = (event) => {
      if (event.source !== canvas || event.origin !== location.origin) return;
      if (event.data?.type === CONTEXT) {
        const next = { projectId: event.data.projectId || "", workflowId: event.data.workflowId || "" };
        const old = currentContext.current;
        if (old.projectId !== next.projectId || old.workflowId !== next.workflowId) {
          currentContext.current = next;
          setContext(next);
        }
      }
      if (event.data?.type === NEW_CONVERSATION) startOver();
    };
    window.addEventListener("message", onMessage);
    postToCanvas(READY, {}, canvas);
    return () => window.removeEventListener("message", onMessage);
  }, [canvas]);

  useEffect(() => {
    let current = true;
    let revision = 0;
    const load = () => {
      const requested = ++revision;
      api("/api/assistant/models")
        .then((data) => current && requested === revision && setCatalog(data || {}))
        .catch((e) => current && requested === revision && setError(e.message));
    };
    const onCredentialsChanged = (event) => {
      if (event.source !== canvas || event.origin !== location.origin ||
          event.data?.type !== "agentflow-credentials-changed") return;
      setCatalog({});
      setMenu(null);
      load();
    };
    load();
    window.addEventListener("message", onCredentialsChanged);
    return () => {
      current = false;
      window.removeEventListener("message", onCredentialsChanged);
    };
  }, [canvas]);

  const engine = engineOfModel(model, catalog.models);
  const engineList = useMemo(() => (catalog.engines || []).filter((item) =>
    item.configured && modelsFor(catalog, item.id).length), [catalog]);
  const models = engineList.some((item) => item.id === engine) ? modelsFor(catalog, engine) : [];
  const modelAvailable = models.some((item) => item.id === model);
  // Fall back to an available model when the saved model is no longer selectable.
  useEffect(() => {
    if (modelAvailable || !engineList.length) return;
    const preferredEngine = catalog.preferences?.executor;
    const preferredModel = catalog.preferences?.model;
    const preferred = engineList.some((item) => item.id === preferredEngine) &&
      modelsFor(catalog, preferredEngine).find((m) => m.id === preferredModel || m.id === preferredEngine + ":" + preferredModel);
    setModel(preferred?.id || modelsFor(catalog, engineList[0].id)[0].id);
  }, [catalog, engineList, modelAvailable]);

  useEffect(() => {
    let current = true;
    setSession(null);
    setSessionId("");
    setGeneration(null);
    setLive(empty);
    setStarting(false);
    setError("");
    owned.current.clear();
    if (!context.projectId || !context.workflowId) return;
    const query = `?surface=${SURFACE}&workflowId=${encodeURIComponent(context.workflowId)}&projectId=${encodeURIComponent(context.projectId)}`;
    api(`/api/assistant/sessions${query}`)
      .then((rows) => {
        const latest = current && latestSession(rows, context.projectId, context.workflowId);
        if (latest) setSessionId(latest.id);
      })
      .catch((e) => current && setError(e.message));
    return () => {
      current = false;
    };
  }, [context.projectId, context.workflowId]);

  useEffect(() => {
    if (!sessionId) return;
    let current = true;
    api(sessionPath(sessionId))
      .then((stored) => current && setSession((old) => old?.id === stored.id && (old.messages?.length || 0) > (stored.messages?.length || 0) ? old : stored))
      .catch((e) => current && e.status !== 404 && setError(e.message));
    return () => {
      current = false;
    };
  }, [sessionId]);

  // Follow the conversation's generation: the stream while it runs, then the
  // saved transcript. Only a turn started here builds on the canvas, so
  // reopening the panel never replays an old draft.
  useEffect(() => {
    if (!sessionId) return;
    let cancelled = false, timer, source, cursor = 0, genId = null, settled = null, lastBuffer = "";
    let accumulated = empty;
    const consume = async (g) => {
      if (cancelled || !g) return;
      if (genId !== g.id) {
        genId = g.id;
        cursor = 0;
        accumulated = empty;
      }
      if (g.stream) {
        accumulated = restoreStream(g.stream);
        cursor = g.stream.sequence;
      }
      const events = (g.events || []).filter((e) => e.sequence > cursor);
      cursor = Math.max(cursor, ...events.map((e) => e.sequence));
      accumulated = mergeEvents(accumulated, events);
      setLive(accumulated);
      setGeneration(g);
      const mine = owned.current.has(g.id);
      if (mine && g.status === "running" && accumulated.draftBuffer && accumulated.draftBuffer !== lastBuffer) {
        lastBuffer = accumulated.draftBuffer;
        postToCanvas(DRAFT, { buffer: lastBuffer }, canvas);
      }
      if (g.status === "running" || settled === g.id) return;
      settled = g.id;
      source?.close();
      source = null;
      const stored = await api(sessionPath(sessionId)).catch((e) => {
        if (e.status !== 404) throw e;
        return null;
      });
      if (cancelled) return;
      if (!stored) {
        startOver();
        return;
      }
      setSession(stored);
      setLive(empty);
      if (!mine) return;
      owned.current.delete(g.id);
      const last = stored.messages?.at(-1);
      if (g.status === "completed" && last?.role === "assistant") {
        postToCanvas(REPLY, { generationId: g.id, message: parseReply(last).message, draft: replyDraft(last) }, canvas);
      } else {
        const failed = !["canceled", "cancelled"].includes(g.status);
        if (failed && g.error) setError(g.error);
        postToCanvas(END, { error: failed ? g.error || "生成失败" : "" }, canvas);
      }
    };
    const connect = () => {
      if (source || cancelled || typeof EventSource === "undefined") return false;
      source = new EventSource(`${sessionPath(sessionId)}/generation?stream=1`);
      source.addEventListener("generation", (e) => {
        void consume(JSON.parse(e.data)).catch((err) => !cancelled && setError(err.message));
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
        const g = await api(`${sessionPath(sessionId)}/generation?after=${cursor}`);
        if (cancelled) return;
        await consume(g);
        if (g.status === "running" && !connect()) timer = setTimeout(poll, 300);
      } catch (e) {
        if (cancelled || e.status === 404) return;
        setError(e.message);
        timer = setTimeout(poll, 2000);
      }
    };
    void poll();
    return () => {
      cancelled = true;
      clearTimeout(timer);
      source?.close();
    };
  }, [sessionId, generation?.id, canvas]);

  useEffect(() => {
    scroll.current?.scrollTo?.({ top: scroll.current.scrollHeight });
  }, [session?.messages?.length, live.answer, live.activity?.length]);

  function startOver() {
    owned.current.clear();
    setSession(null);
    setSessionId("");
    setGeneration(null);
    setLive(empty);
    setError("");
    postToCanvas(RESET, {}, canvas);
  }

  const selectEngine = useCallback((id) => {
    const list = modelsFor(catalog, id);
    const next = list.find((m) => m.id === rememberedModel(id)) || list.find((m) => m.id === catalog.default) || list[0];
    if (!next) return;
    rememberModel(next.id, id);
    setModel(next.id);
  }, [catalog]);

  async function send(value = text) {
    const content = value.trim();
    if (!content || busy) return;
    if (!context.projectId) {
      setError("先在顶栏选择项目");
      return;
    }
    if (!modelAvailable) {
      setError("请先在个人配置中添加可用的执行器和模型");
      return;
    }
    setError("");
    setStarting(true);
    let canvasRequested = false;
    try {
      let stored = null;
      if (sessionId) {
        stored = await api(sessionPath(sessionId)).catch((e) => {
          if (e.status !== 404) throw e;
          return null;
        });
        if (currentContext.current !== context) return;
        if (!stored) startOver();
      }
      canvasRequested = true;
      const begun = await beginOnCanvas(canvas);
      if (currentContext.current !== context) return;
      if (!begun.ok) throw new Error(begun.error || "画布暂时不能编排");
      // A conversation stays on one CLI; another CLI starts a new one and
      // keeps the canvas.
      const locked = stored ? conversationEngine(stored.messages, catalog.models, stored.executor) : "";
      const base = stored && (!locked || locked === engine) ? stored : null;
      const id = base?.id || newId();
      const message = { role: "user", content, createdAt: new Date().toISOString() };
      const g = await write("/api/assistant/generations", {
        surface: SURFACE,
        projectId: begun.projectId || context.projectId,
        sessionId: id,
        model,
        approvalMode: "confirm",
        requireExactModel: !!model,
        workflowId: begun.workflowId ?? context.workflowId,
        canvas: begun.canvas ?? null,
        messages: [message],
      });
      if (currentContext.current !== context) return;
      owned.current.add(g.id);
      setText("");
      setSession({ ...(base || { id, messages: [] }), messages: [...(base?.messages || []), message] });
      setLive(empty);
      setGeneration(g);
      if (id !== sessionId) setSessionId(id);
    } catch (e) {
      if (currentContext.current !== context) return;
      setError(e.message);
      if (canvasRequested) postToCanvas(END, { error: e.message }, canvas);
    } finally {
      if (currentContext.current === context) setStarting(false);
    }
  }

  async function stop() {
    if (generation?.status !== "running") return;
    try {
      await write(`${sessionPath(sessionId)}/generation/cancel?generationId=${encodeURIComponent(generation.id)}`, {});
    } catch (e) {
      setError(e.message);
    }
  }

  const messages = session?.messages || [];
  const running = generation?.status === "running";
  return (
    <div className="sop-designer">
      <div className="sop-designer-scroll" ref={scroll}>
        {!messages.length && !running ? (
          <div className="sop-designer-intro">
            <h3>您好，我是 <em>AI 编排助手</em></h3>
            <p>描述要创建的流程，或指出现有流程中需要调整的步骤。</p>
            <div className="sop-designer-suggest-head">
              <span>可以试试这些场景：</span>
              <button type="button" onClick={() => setPage((n) => n + 1)}>换一批</button>
            </div>
            <div className="sop-designer-suggest">
              {scenarioPage(page).map(([label, prompt]) => (
                <button key={label} type="button" onClick={() => { setText(prompt); input.current?.focus(); }}>{label}</button>
              ))}
            </div>
          </div>
        ) : null}
        {messages.map((message, index) => {
          if (message.role === "user") {
            return <div key={index} className="sop-designer-message user"><div className="user-bubble">{message.content}</div></div>;
          }
          const draft = replyDraft(message);
          return (
            <div key={index} className="sop-designer-message assistant">
              <ReplyTimeline steps={message.activity || []} startedAt={message.startedAt} finishedAt={message.createdAt} />
              <RichContent message={message} projectId={context.projectId} />
              {draft ? <DraftNote
                draft={draft}
                onPlace={!busy && draft.tool === "workflows_save" ? (d) => postToCanvas(PLACE, { draft: d.body }, canvas) : null}
                onConfigureRole={!busy && draft.tool === "agents_save" ? (d) => postToCanvas(CONFIGURE_ROLE, { ...context, role: d.body }, canvas) : null}
              /> : null}
            </div>
          );
        })}
        {running ? (
          <div className="sop-designer-message assistant live">
            <ReplyTimeline steps={live.activity || []} startedAt={generation.startedAt} live />
            {live.answer ? <RichContent message={live.answer} projectId={context.projectId} /> : null}
          </div>
        ) : null}
        {error ? <p role="alert" className="sop-designer-error">{error}</p> : null}
      </div>
      <form className="sop-designer-composer" onSubmit={(e) => { e.preventDefault(); void send(); }}>
        <div className="composer">
          <textarea
            ref={input}
            rows={3}
            maxLength={2000}
            aria-label="描述要编排的流程"
            placeholder={messages.length ? "直接说要改哪里，例如：在实现后面加一个人工评审" : "告诉我你的目标，我来搞定…"}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                void send();
              }
            }}
          />
          <footer className="composer-toolbar">
            {engineList.length ? <PermissionPicker
              label="CLI"
              options={engineList.map((item) => {
                const brand = engineBrand(item.id);
                return {
                  ...item,
                  icon: "terminal",
                  label: item.name,
                  detail: `使用 ${item.name} 模型`,
                  glyph: hasProviderLogo(brand) ? <ModelProviderIcon brand={brand} size={16} /> : null,
                };
              })}
              value={engine}
              onSelect={(id) => { if (!busy) selectEngine(id); }}
              open={menu === "engine"}
              onOpenChange={(open) => setMenu(open && !busy ? "engine" : null)}
            /> : null}
            <ModelPicker
              models={models}
              value={model}
              label={models.find((m) => m.id === model)?.name || "模型"}
              open={menu === "model"}
              onOpenChange={(open) => setMenu(open && !busy ? "model" : null)}
              onSelect={(id) => {
                rememberModel(id, engine);
                setModel(id);
                input.current?.focus();
              }}
            />
            <span className="toolbar-spacer" />
            {running ? (
              <button type="button" className="send-button stop" aria-label="停止生成" disabled={generation.cancelRequested} onClick={stop}>
                <HarnessIcon name="chat-stop" size={14} />
              </button>
            ) : (
              <button type="submit" className="send-button" aria-label="发送编排要求" disabled={busy || !text.trim() || !modelAvailable}>
                <HarnessIcon name="chat-send" size={20} />
              </button>
            )}
          </footer>
        </div>
        <small>{catalog.engines?.length && !engineList.length ? "请先在个人配置中添加可用的执行器和模型" : "仅生成草稿，确认后在画布上保存 · 切换 CLI 会开启新对话，画布保留"}</small>
      </form>
    </div>
  );
}
