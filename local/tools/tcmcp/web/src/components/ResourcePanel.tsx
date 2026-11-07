import { useMemo, useState } from "react";
import type { BoundResources, ListedLogset, ListedResource, ProductMeta, ResourceCatalog } from "../types";

const TITLES: Record<string, string> = {
  redis: "Redis",
  cdb: "CDB",
  tdsqlc: "TDSQL-C",
  tke: "TKE",
  cls: "CLS",
  cos: "COS",
};

const COLORS: Record<string, string> = {
  redis: "#ff6b57",
  cdb: "#4c8dff",
  tdsqlc: "#3dd68c",
  tke: "#c084fc",
  cls: "#f5b942",
  cos: "#00a4ff",
};

type SimpleKind = "redis" | "cdb" | "tke" | "cos";

function AliasInput({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <input
      className="alias"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      aria-label="资源别名"
    />
  );
}

function regionLabel(regions: { id: string; name: string }[], id: string) {
  return regions.find((r) => r.id === id)?.name || id;
}

function match(q: string, ...parts: string[]) {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  return parts.some((p) => p.toLowerCase().includes(needle));
}

export function ResourcePanel({
  products,
  productMeta,
  regions,
  productRegions,
  pulledRegions,
  catalog,
  bound,
  loadingProduct,
  productErrors,
  onRegion,
  onFetchProduct,
  onAddSimple,
  onRemoveSimple,
  onAddTdsqlc,
  onRemoveTdsqlc,
  onAddTopics,
  onRemoveTopic,
  onAlias,
}: {
  products: string[];
  productMeta: ProductMeta[];
  regions: { id: string; name: string }[];
  productRegions: Record<string, string>;
  pulledRegions: Record<string, string[]>;
  catalog: ResourceCatalog;
  bound: BoundResources;
  loadingProduct: string | null;
  productErrors: Record<string, string>;
  onRegion: (product: string, region: string) => void;
  onFetchProduct: (product: string, region: string) => void;
  onAddSimple: (
    kind: SimpleKind,
    items: { id: string; name: string; region: string; extra?: Record<string, unknown> }[],
  ) => void;
  onRemoveSimple: (kind: SimpleKind, id: string) => void;
  onAddTdsqlc: (items: { clusterId: string; instanceId: string; name: string; region: string }[]) => void;
  onRemoveTdsqlc: (clusterId: string) => void;
  onAddTopics: (
    items: { logsetId: string; logsetName: string; topicId: string; topicName: string; region: string }[],
  ) => void;
  onRemoveTopic: (logsetId: string, topicId: string) => void;
  onAlias: (kind: string, id: string, alias: string, extra?: string) => void;
}) {
  const names = useMemo(() => {
    const m: Record<string, string> = {};
    for (const p of productMeta) m[p.id] = p.name;
    return m;
  }, [productMeta]);

  if (!products.length) {
    return (
      <div className="empty-card">
        <p className="empty">先回到上一步勾选产品。</p>
      </div>
    );
  }

  return (
    <div className="product-fetch-list">
      {products.map((id) => (
        <ProductFetchCard
          key={id}
          product={id}
          title={names[id] || TITLES[id] || id}
          color={productMeta.find((p) => p.id === id)?.color || COLORS[id] || "#888"}
          regions={regions}
          region={productRegions[id] || "ap-guangzhou"}
          pulled={pulledRegions[id] || []}
          catalog={catalog}
          bound={bound}
          loading={loadingProduct === id}
          error={productErrors[id] || ""}
          onRegion={(r) => onRegion(id, r)}
          onFetch={(r) => onFetchProduct(id, r)}
          onAddSimple={onAddSimple}
          onRemoveSimple={onRemoveSimple}
          onAddTdsqlc={onAddTdsqlc}
          onRemoveTdsqlc={onRemoveTdsqlc}
          onAddTopics={onAddTopics}
          onRemoveTopic={onRemoveTopic}
          onAlias={onAlias}
        />
      ))}
    </div>
  );
}

function ProductFetchCard({
  product,
  title,
  color,
  regions,
  region,
  pulled,
  catalog,
  bound,
  loading,
  error,
  onRegion,
  onFetch,
  onAddSimple,
  onRemoveSimple,
  onAddTdsqlc,
  onRemoveTdsqlc,
  onAddTopics,
  onRemoveTopic,
  onAlias,
}: {
  product: string;
  title: string;
  color: string;
  regions: { id: string; name: string }[];
  region: string;
  pulled: string[];
  catalog: ResourceCatalog;
  bound: BoundResources;
  loading: boolean;
  error: string;
  onRegion: (region: string) => void;
  onFetch: (region: string) => void;
  onAddSimple: (
    kind: SimpleKind,
    items: { id: string; name: string; region: string; extra?: Record<string, unknown> }[],
  ) => void;
  onRemoveSimple: (kind: SimpleKind, id: string) => void;
  onAddTdsqlc: (items: { clusterId: string; instanceId: string; name: string; region: string }[]) => void;
  onRemoveTdsqlc: (clusterId: string) => void;
  onAddTopics: (
    items: { logsetId: string; logsetName: string; topicId: string; topicName: string; region: string }[],
  ) => void;
  onRemoveTopic: (logsetId: string, topicId: string) => void;
  onAlias: (kind: string, id: string, alias: string, extra?: string) => void;
}) {
  const [q, setQ] = useState("");
  const boundCount = countBound(product, bound);
  const fetched = pulled.includes(region);
  const regionName = regionLabel(regions, region);
  const known = regions.some((r) => r.id === region);

  function addSimpleResource(item: ListedResource) {
    if (product !== "redis" && product !== "cdb" && product !== "tke" && product !== "cos") return;
    onAddSimple(product, [{ id: item.id, name: item.name, region: item.region || region, extra: item.extra }]);
  }

  function addTdsqlcResource(item: ListedResource) {
    onAddTdsqlc([
      {
        clusterId: item.id,
        instanceId: String(item.extra?.instanceId || ""),
        name: item.name,
        region: item.region || region,
      },
    ]);
  }

  function addClsTopic(ls: ListedLogset, tp: ListedLogset["topics"][number]) {
    if (product === "cls") {
      onAddTopics([
        {
          logsetId: ls.id,
          logsetName: ls.name,
          topicId: tp.id,
          topicName: tp.name,
          region: ls.region || region,
        },
      ]);
    }
  }

  return (
    <section className="fetch-card">
      <header className="fetch-card-head">
        <span className="dot" style={{ background: color }} />
        <h3>{title}</h3>
        <span className="count-pill">{boundCount} 已绑定</span>
      </header>
      <div className="fetch-toolbar">
        <select
          className="region-select"
          value={region}
          onChange={(e) => {
            onRegion(e.target.value);
          }}
        >
          {!known && region ? <option value={region}>{region}</option> : null}
          {regions.map((r) => (
            <option key={r.id} value={r.id}>
              {r.name} · {r.id}
            </option>
          ))}
        </select>
        <button className="btn" type="button" onClick={() => onFetch(region)} disabled={loading || !region}>
          {loading ? "拉取中…" : `拉取 ${regionName}`}
        </button>
        <input className="search-mini" placeholder="筛选名称 / ID" value={q} onChange={(e) => setQ(e.target.value)} />
      </div>
      {pulled.length ? (
        <div className="region-chips">
          <span className="region-chips-empty">已拉取</span>
          {pulled.map((id) => (
            <button
              key={id}
              className={`region-chip-btn${id === region ? " on" : ""}`}
              type="button"
              onClick={() => onRegion(id)}
            >
              {regionLabel(regions, id)}
            </button>
          ))}
        </div>
      ) : null}
      {error ? <div className="err">{error}</div> : null}
      <div className="transfer">
        <div className="transfer-pane">
          <h4>待选 · {regionName}</h4>
          <div className="transfer-list">
            {product === "cls" ? (
              <CLSPool
                items={catalog.cls.filter((ls) => (ls.region || "") === region)}
                bound={bound}
                q={q}
                fetched={fetched}
                onAdd={addClsTopic}
              />
            ) : product === "tdsqlc" ? (
              <TDSQLCPool
                items={catalog.tdsqlc.filter((r) => (r.region || "") === region)}
                bound={bound}
                q={q}
                fetched={fetched}
                onAdd={addTdsqlcResource}
              />
            ) : (
              <SimplePool
                kind={product as SimpleKind}
                items={(catalog[product as SimpleKind] ?? []).filter((r) => (r.region || "") === region)}
                bound={bound}
                q={q}
                fetched={fetched}
                onAdd={addSimpleResource}
              />
            )}
          </div>
        </div>
        <div className="transfer-mid">
          <span className="transfer-arrow" aria-hidden>
            →
          </span>
        </div>
        <div className="transfer-pane">
          <h4>已绑定</h4>
          <div className="transfer-list">
            {product === "cls" ? (
              <CLSBound bound={bound} onAlias={onAlias} onRemove={onRemoveTopic} />
            ) : product === "tdsqlc" ? (
              <TDSQLCBound bound={bound} onAlias={onAlias} onRemove={onRemoveTdsqlc} />
            ) : (
              <SimpleBound kind={product as SimpleKind} bound={bound} onAlias={onAlias} onRemove={onRemoveSimple} />
            )}
          </div>
        </div>
      </div>
    </section>
  );
}

function PickRow({
  onAdd,
  name,
  region,
  meta,
}: {
  onAdd: () => void;
  name: string;
  region?: string;
  meta: string;
}) {
  return (
    <button className="res-item pick" type="button" onClick={onAdd}>
      <span className="add-mark" aria-hidden>
        +
      </span>
      <div>
        <div className="name">
          {name}
          {region ? <span className="region-chip">{region}</span> : null}
        </div>
        <div className="meta">{meta}</div>
      </div>
    </button>
  );
}

function SimplePool({
  kind,
  items,
  bound,
  q,
  fetched,
  onAdd,
}: {
  kind: SimpleKind;
  items: ListedResource[];
  bound: BoundResources;
  q: string;
  fetched: boolean;
  onAdd: (item: ListedResource) => void;
}) {
  const boundIds = new Set(bound[kind].map((x) => x.id));
  const rows = items.filter((r) => !boundIds.has(r.id) && match(q, r.name, r.id, r.region || ""));
  if (!fetched) return <p className="empty">选好地域后点拉取，只拉这一类产品。</p>;
  if (!rows.length) return <p className="empty">{q ? "没有匹配的资源" : "这个地域没有可添加的实例"}</p>;
  return (
    <>
      {rows.map((r) => (
        <PickRow
          key={`${r.region}:${r.id}`}
          onAdd={() => onAdd(r)}
          name={r.name}
          region={r.region}
          meta={r.id}
        />
      ))}
    </>
  );
}

function SimpleBound({
  kind,
  bound,
  onAlias,
  onRemove,
}: {
  kind: SimpleKind;
  bound: BoundResources;
  onAlias: (kind: string, id: string, alias: string) => void;
  onRemove: (kind: SimpleKind, id: string) => void;
}) {
  const rows = bound[kind];
  if (!rows.length) return <p className="empty">点击左侧资源后会出现在这里。</p>;
  return (
    <>
      {rows.map((r) => (
        <div className="res-item bound" key={r.id}>
          <div>
            <div className="name">
              {r.name}
              {r.region ? <span className="region-chip">{r.region}</span> : null}
            </div>
            <div className="meta">{r.id}</div>
          </div>
          <AliasInput value={r.alias} onChange={(v) => onAlias(kind, r.id, v)} />
          <button className="btn ghost" type="button" onClick={() => onRemove(kind, r.id)}>
            移除
          </button>
        </div>
      ))}
    </>
  );
}

function TDSQLCPool({
  items,
  bound,
  q,
  fetched,
  onAdd,
}: {
  items: ListedResource[];
  bound: BoundResources;
  q: string;
  fetched: boolean;
  onAdd: (item: ListedResource) => void;
}) {
  const boundIds = new Set(bound.tdsqlc.map((x) => x.clusterId));
  const rows = items.filter(
    (r) => !boundIds.has(r.id) && match(q, r.name, r.id, String(r.extra?.instanceId || ""), r.region || ""),
  );
  if (!fetched) return <p className="empty">选好地域后点拉取，只拉 TDSQL-C 集群。</p>;
  if (!rows.length) return <p className="empty">{q ? "没有匹配的资源" : "这个地域没有可添加的集群"}</p>;
  return (
    <>
      {rows.map((r) => {
        const instanceId = String(r.extra?.instanceId || "");
        return (
          <PickRow
            key={`${r.region}:${r.id}`}
            onAdd={() => onAdd(r)}
            name={r.name}
            region={r.region}
            meta={instanceId ? `${r.id} · ${instanceId}` : r.id}
          />
        );
      })}
    </>
  );
}

function TDSQLCBound({
  bound,
  onAlias,
  onRemove,
}: {
  bound: BoundResources;
  onAlias: (kind: string, id: string, alias: string) => void;
  onRemove: (clusterId: string) => void;
}) {
  if (!bound.tdsqlc.length) return <p className="empty">点击左侧资源后会出现在这里。</p>;
  return (
    <>
      {bound.tdsqlc.map((r) => (
        <div className="res-item bound" key={r.clusterId}>
          <div>
            <div className="name">
              {r.name}
              {r.region ? <span className="region-chip">{r.region}</span> : null}
            </div>
            <div className="meta">
              {r.clusterId}
              {r.instanceId ? ` · ${r.instanceId}` : ""}
            </div>
          </div>
          <AliasInput value={r.alias} onChange={(v) => onAlias("tdsqlc", r.clusterId, v)} />
          <button className="btn ghost" type="button" onClick={() => onRemove(r.clusterId)}>
            移除
          </button>
        </div>
      ))}
    </>
  );
}

function CLSPool({
  items,
  bound,
  q,
  fetched,
  onAdd,
}: {
  items: ListedLogset[];
  bound: BoundResources;
  q: string;
  fetched: boolean;
  onAdd: (logset: ListedLogset, topic: ListedLogset["topics"][number]) => void;
}) {
  if (!fetched) return <p className="empty">选好地域后点拉取，只拉该地域的日志集和主题。</p>;
  const visible = items
    .map((ls) => ({
      ...ls,
      topics: ls.topics.filter((tp) => {
        const already = bound.cls.find((x) => x.logsetId === ls.id)?.topics.some((t) => t.id === tp.id);
        if (already) return false;
        return match(q, ls.name, ls.id, tp.name, tp.id, ls.region || "");
      }),
    }))
    .filter((ls) => ls.topics.length);
  if (!visible.length) return <p className="empty">{q ? "没有匹配的主题" : "这个地域没有可添加的日志主题"}</p>;
  return (
    <>
      {visible.map((ls) => (
        <div key={`${ls.region}:${ls.id}`} className="logset">
          <div className="logset-head">
            <span>
              {ls.name}
              {ls.region ? <span className="region-chip">{ls.region}</span> : null}
            </span>
            <code>{ls.id}</code>
          </div>
          {ls.topics.map((tp) => (
            <PickRow
              key={tp.id}
              onAdd={() => onAdd(ls, tp)}
              name={tp.name}
              meta={tp.id}
            />
          ))}
        </div>
      ))}
    </>
  );
}

function CLSBound({
  bound,
  onAlias,
  onRemove,
}: {
  bound: BoundResources;
  onAlias: (kind: string, id: string, alias: string, extra?: string) => void;
  onRemove: (logsetId: string, topicId: string) => void;
}) {
  if (!bound.cls.length) return <p className="empty">点击左侧主题后会出现在这里。</p>;
  return (
    <>
      {bound.cls.map((ls) => (
        <div key={ls.logsetId} className="logset">
          <div className="logset-head">
            <span>
              {ls.logsetName}
              {ls.region ? <span className="region-chip">{ls.region}</span> : null}
            </span>
            <code>{ls.logsetId}</code>
          </div>
          {ls.topics.map((tp) => (
            <div className="res-item bound" key={tp.id}>
              <div>
                <div className="name">{tp.name}</div>
                <div className="meta">{tp.id}</div>
              </div>
              <AliasInput value={tp.alias} onChange={(v) => onAlias("cls", tp.id, v, ls.logsetId)} />
              <button className="btn ghost" type="button" onClick={() => onRemove(ls.logsetId, tp.id)}>
                移除
              </button>
            </div>
          ))}
        </div>
      ))}
    </>
  );
}

function countBound(product: string, bound: BoundResources): number {
  if (product === "redis") return bound.redis.length;
  if (product === "cdb") return bound.cdb.length;
  if (product === "tdsqlc") return bound.tdsqlc.length;
  if (product === "tke") return bound.tke.length;
  if (product === "cls") return bound.cls.reduce((n, ls) => n + ls.topics.length, 0);
  if (product === "cos") return bound.cos.length;
  return 0;
}
