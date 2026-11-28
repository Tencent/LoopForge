import React, { useEffect, useState } from "react";
import { api } from "./api";
import ArtifactPanel from "./ArtifactPanel";
import { SopNodeDetail, SopNodeList } from "./SopRunPanel";
import { defaultReviewNode, runPath } from "./sop-review.js";
import { readThemePreference, resolveTheme, THEME_KEY } from "./theme.js";

const artifactLayout = {
  defaultWidth: 340,
  storageKey: "agentflow.runReviewArtifactPanelWidth",
};

export default function SopRunReview({ search = location.search }) {
  const params = new URLSearchParams(search);
  const runId = params.get("runId");
  const [requestedNode, setRequestedNode] = useState(params.get("nodeId"));
  const [run, setRun] = useState(null);
  const [version, refresh] = useState(0);
  const [error, setError] = useState("");

  useEffect(() => {
    const system = matchMedia("(prefers-color-scheme: dark)");
    const paint = () => {
      document.documentElement.dataset.theme = resolveTheme(
        readThemePreference(localStorage),
        system.matches,
      );
    };
    const onStorage = (event) => {
      if (event.key === THEME_KEY) paint();
    };
    paint();
    window.addEventListener("storage", onStorage);
    system.addEventListener("change", paint);
    return () => {
      window.removeEventListener("storage", onStorage);
      system.removeEventListener("change", paint);
    };
  }, []);

  useEffect(() => {
    const receive = (event) => {
      if (event.origin !== location.origin || event.source !== window.parent)
        return;
      if (
        event.data?.type === "agentflow-run-review-select" &&
        event.data.runId === runId
      )
        setRequestedNode(event.data.nodeId);
      else if (
        event.data?.type === "agentflow-run-review-refresh" &&
        (!event.data.runId || event.data.runId === runId)
      )
        refresh((n) => n + 1);
    };
    window.addEventListener("message", receive);
    return () => window.removeEventListener("message", receive);
  }, [runId]);

  useEffect(() => {
    if (!runId) return;
    let current = true;
    let timer;
    let loading = false;
    async function load() {
      if (loading || !current) return;
      clearTimeout(timer);
      loading = true;
      try {
        const result = await api(runPath(runId));
        if (!current) return;
        setRun(result);
        setRequestedNode((node) => node || defaultReviewNode(result));
        setError("");
        if (["queued", "running", "waiting", "paused"].includes(result.status))
          timer = setTimeout(load, 2500);
      } catch (e) {
        if (current) setError(e.message);
      } finally {
        loading = false;
      }
    }
    void load();
    window.addEventListener("focus", load);
    return () => {
      current = false;
      clearTimeout(timer);
      window.removeEventListener("focus", load);
    };
  }, [runId, version]);

  function post(type, fields = {}) {
    if (window.parent !== window)
      window.parent.postMessage({ type, runId, ...fields }, location.origin);
  }
  function select(id) {
    setRequestedNode(id);
    post("agentflow-run-review-node", { nodeId: id });
  }

  const steps = run?.workflowSnapshot?.nodes || [];
  const nodeId = steps.some(({ id }) => id === requestedNode)
    ? requestedNode
    : defaultReviewNode(run || {});
  return (
    <main className="sop-review-page">
      {error || !runId ? (
        <div className="sop-review-load" role="alert">
          {error || "未指定运行"}
          {runId ? (
            <button
              type="button"
              className="sop-action-button"
              onClick={() => refresh((n) => n + 1)}
            >
              重新加载
            </button>
          ) : null}
        </div>
      ) : !run ? (
        <p className="sop-review-load" role="status">
          正在加载运行…
        </p>
      ) : null}
      {run && steps.length ? (
        <div className="sop-review-workspace">
          <nav className="sop-review-navigation" aria-label="流程节点">
            <h2>流程节点</h2>
            <SopNodeList
              run={run}
              selectedNodeId={nodeId}
              onSelectNode={select}
              showArtifacts
            />
          </nav>
          <SopNodeDetail
            key={`${run.id}:${nodeId}`}
            run={run}
            nodeId={nodeId}
            renderArtifacts={(panel) => (
              <ArtifactPanel
                {...panel}
                filesOnly
                layoutOptions={artifactLayout}
                runs={[]}
                chat={{ projectId: run.executionContext?.projectId }}
              />
            )}
            onChanged={() => {
              refresh((n) => n + 1);
              post("agentflow-run-review-updated");
            }}
          />
        </div>
      ) : run ? (
        <p className="sop-review-load">此运行没有 LOOP 节点。</p>
      ) : null}
    </main>
  );
}
