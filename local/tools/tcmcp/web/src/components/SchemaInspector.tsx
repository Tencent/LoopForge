import { useMemo, useState } from "react";
import type { Preview, SchemaTool } from "../types";

const LABELS: Record<string, string> = {
  common: "Common",
  redis: "Redis",
  cdb: "CDB",
  tdsqlc: "TDSQL-C",
  tke: "TKE",
  cls: "CLS",
};

const COLORS: Record<string, string> = {
  common: "#8b98a5",
  redis: "#ff6b57",
  cdb: "#4c8dff",
  tdsqlc: "#3dd68c",
  tke: "#c084fc",
  cls: "#f5b942",
};

export function SchemaInspector({
  preview,
  mode,
  onMode,
  onToggleTool,
}: {
  preview: Preview | null;
  mode: "grouped" | "flat";
  onMode: (m: "grouped" | "flat") => void;
  onToggleTool: (name: string) => void;
}) {
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<Record<string, boolean>>({});

  const items = useMemo(() => {
    if (!preview) return [];
    const all = [...preview.tools, ...preview.skipped];
    const needle = q.trim().toLowerCase();
    if (!needle) return all;
    return all.filter(
      (t) => t.name.toLowerCase().includes(needle) || t.description.toLowerCase().includes(needle),
    );
  }, [preview, q]);

  if (!preview || (!preview.tools.length && !preview.skipped.length)) {
    return (
      <div className="empty-card schema-empty">
        <div className="empty-icon">{"{}"}</div>
        <p className="empty">勾选左侧产品后，这里显示模型将看到的 tools/list。</p>
      </div>
    );
  }

  const groups = new Map<string, SchemaTool[]>();
  for (const t of items) {
    const list = groups.get(t.product) ?? [];
    list.push(t);
    groups.set(t.product, list);
  }

  return (
    <>
      <div className="inspector-toolbar">
        <div className="tabs" role="tablist">
          <button className={`tab${mode === "grouped" ? " on" : ""}`} onClick={() => onMode("grouped")}>
            按产品
          </button>
          <button className={`tab${mode === "flat" ? " on" : ""}`} onClick={() => onMode("flat")}>
            扁平列表
          </button>
        </div>
        <input
          className="search"
          placeholder="搜索工具名或描述"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
      </div>
      {mode === "flat"
        ? items.map((t) => (
            <ToolCard
              key={t.name}
              tool={t}
              expanded={!!open[t.name]}
              onExpand={() => setOpen((s) => ({ ...s, [t.name]: !s[t.name] }))}
              onToggle={() => onToggleTool(t.name)}
            />
          ))
        : [...groups.entries()].map(([product, tools]) => {
            const ready = tools.filter((t) => t.ready && t.enabled).length;
            return (
              <div className="group" key={product}>
                <div className="group-head">
                  <span className="dot" style={{ background: COLORS[product] || "#888" }} />
                  <strong>{LABELS[product] ?? product}</strong>
                  <span className="count-pill">{ready} ready</span>
                </div>
                {tools.map((t) => (
                  <ToolCard
                    key={t.name}
                    tool={t}
                    expanded={!!open[t.name]}
                    onExpand={() => setOpen((s) => ({ ...s, [t.name]: !s[t.name] }))}
                    onToggle={() => onToggleTool(t.name)}
                  />
                ))}
              </div>
            );
          })}
    </>
  );
}

function ToolCard({
  tool,
  expanded,
  onExpand,
  onToggle,
}: {
  tool: SchemaTool;
  expanded: boolean;
  onExpand: () => void;
  onToggle: () => void;
}) {
  const dim = !tool.ready || !tool.enabled;
  const required = new Set(tool.inputSchema.required ?? []);
  const props = tool.inputSchema.properties ?? {};
  return (
    <div className={`tool-card${dim ? " dim" : ""}${expanded ? " open" : ""}`}>
      <div className="tool-top">
        <label className="switch" title={tool.enabled ? "从生成结果中移除" : "重新加入"}>
          <input type="checkbox" checked={tool.enabled} onChange={onToggle} />
          <span />
        </label>
        <button type="button" className="tool-name" onClick={onExpand}>
          <code>{tool.name}</code>
          <span className="chevron">{expanded ? "−" : "+"}</span>
        </button>
        {tool.annotations.readOnlyHint ? <span className="badge ok">read-only</span> : null}
        {!tool.ready ? <span className="badge">skipped</span> : null}
      </div>
      <p className="tool-desc">{tool.description}</p>
      {!tool.ready && tool.skipReason ? <div className="skip">{tool.skipReason}</div> : null}
      {expanded ? (
        <div className="fields">
          {Object.entries(props).map(([name, prop]) => (
            <div className="field" key={name}>
              <div className="field-k">
                <span className="k">{name}</span>
                <span className="type">{prop.type}</span>
                {required.has(name) ? <span className="req">required</span> : null}
              </div>
              {prop.description ? <div className="d">{prop.description}</div> : null}
              {prop.enum?.length ? (
                <div className="chips">
                  {prop.enum.map((v) => (
                    <span className="chip" key={v}>
                      {v}
                    </span>
                  ))}
                </div>
              ) : null}
            </div>
          ))}
          <details>
            <summary>查看原始 JSON</summary>
            <pre className="raw">{JSON.stringify(tool.inputSchema, null, 2)}</pre>
          </details>
        </div>
      ) : null}
    </div>
  );
}
