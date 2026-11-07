import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createInstallation, downloadZip, fetchMetadata, fetchResources, previewSchema, type AgentClient, type Installation } from "./api";
import { ProductCard } from "./components/ProductCard";
import { ResourcePanel } from "./components/ResourcePanel";
import { SchemaInspector } from "./components/SchemaInspector";
import { resolveRegions } from "./regions";
import { uniqueAlias } from "./slug";
import type {
  BoundResources,
  Metadata,
  Preview,
  ResourceCatalog,
  ResourceItem,
  Selection,
} from "./types";

const emptyResources = (): BoundResources => ({
  redis: [],
  cdb: [],
  tdsqlc: [],
  tke: [],
  cls: [],
  cos: [],
});

const emptyCatalog = (): ResourceCatalog => ({
  redis: [],
  cdb: [],
  tdsqlc: [],
  tke: [],
  cls: [],
  cos: [],
  errors: {},
});

const PRODUCT_ORDER = ["redis", "cdb", "tdsqlc", "tke", "cls", "cos"] as const;

const PRODUCT_STEP: Record<string, { title: string; short: string }> = {
  redis: { title: "绑定 Redis", short: "Redis" },
  cdb: { title: "绑定 CDB", short: "CDB" },
  tdsqlc: { title: "绑定 TDSQL-C", short: "TDSQL-C" },
  tke: { title: "绑定 TKE", short: "TKE" },
  cls: { title: "绑定 CLS", short: "CLS" },
  cos: { title: "绑定 COS", short: "COS" },
};

type WizardStep = { id: string; title: string; short: string; product?: string };

function buildSteps(selected: string[]): WizardStep[] {
  const resourceSteps = PRODUCT_ORDER.filter((id) => selected.includes(id)).map((id) => ({
    id: `res-${id}`,
    product: id,
    ...PRODUCT_STEP[id],
  }));
  return [
    { id: "welcome", title: "开始", short: "指引" },
    { id: "project", title: "项目配置", short: "项目" },
    { id: "products", title: "项目与产品", short: "产品" },
    ...resourceSteps,
    { id: "schema", title: "MCP Schema", short: "预览" },
    { id: "install", title: "安装与部署", short: "安装" },
  ];
}

export function App() {
  const [step, setStep] = useState(0);
  const [meta, setMeta] = useState<Metadata | null>(null);
  const [name, setName] = useState("demo");
  const [description, setDescription] = useState("Tencent Cloud ops MCP");
  const [target, setTarget] = useState("prod");
  const [secretId, setSecretId] = useState("");
  const [secretKey, setSecretKey] = useState("");
  const [projectId, setProjectId] = useState("");
  const [products, setProducts] = useState<string[]>([]);
  const [disabled, setDisabled] = useState<string[]>([]);
  const [resources, setResources] = useState<BoundResources>(emptyResources);
  const [catalog, setCatalog] = useState<ResourceCatalog>(emptyCatalog);
  const [productRegions, setProductRegions] = useState<Record<string, string>>({});
  const [pulledRegions, setPulledRegions] = useState<Record<string, string[]>>({});
  const [loadingProduct, setLoadingProduct] = useState<string | null>(null);
  const [productErrors, setProductErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [mode, setMode] = useState<"grouped" | "flat">("grouped");
  const [genError, setGenError] = useState("");
  const [generating, setGenerating] = useState(false);
  const [downloaded, setDownloaded] = useState(false);
  const [installation, setInstallation] = useState<Installation | null>(null);
  const [agentClient, setAgentClient] = useState<AgentClient>("codebuddy");
  const [copiedInstall, setCopiedInstall] = useState(false);
  const [installCopyError, setInstallCopyError] = useState("");
  const [copied, setCopied] = useState(false);
  const [copiedDocker, setCopiedDocker] = useState(false);
  const [copiedHttp, setCopiedHttp] = useState(false);
  const [authToken] = useState(() => newAuthToken());

  const region = useMemo(() => fallbackRegion(resources, productRegions), [resources, productRegions]);

  const selection: Selection = useMemo(
    () => ({
      project: { name, description, target, region },
      products,
      disabled_tools: disabled,
      resources,
    }),
    [name, description, target, region, products, disabled, resources],
  );
  const latestSelection = useRef(selection);
  latestSelection.current = selection;

  useEffect(() => {
    fetchMetadata().then(setMeta).catch((e) => setError(String(e)));
  }, []);

  useEffect(() => {
    setInstallation(null);
    setDownloaded(false);
    setCopiedInstall(false);
  }, [selection]);

  useEffect(() => {
    const handle = setTimeout(() => {
      previewSchema(selection).then(setPreview).catch(() => setPreview(null));
    }, 200);
    return () => clearTimeout(handle);
  }, [selection]);

  function toggleProduct(id: string) {
    setProducts((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  }

  function toggleTool(toolName: string) {
    setDisabled((prev) => (prev.includes(toolName) ? prev.filter((x) => x !== toolName) : [...prev, toolName]));
  }

  function setProductRegion(product: string, rid: string) {
    setProductRegions((prev) => ({ ...prev, [product]: rid }));
  }

  async function onFetchProduct(product: string, fetchRegion: string) {
    if (!fetchRegion) {
      setProductErrors((prev) => ({ ...prev, [product]: "先选一个地域再拉取。" }));
      return;
    }
    setProductErrors((prev) => ({ ...prev, [product]: "" }));
    setLoadingProduct(product);
    try {
      const pid = projectId.trim() ? Number(projectId) : null;
      const data = await fetchResources({
        secretId,
        secretKey,
        region: fetchRegion,
        projectId: Number.isFinite(pid as number) ? pid : null,
        products: [product],
      });
      setCatalog((prev) => mergeCatalog(prev, data, product, fetchRegion));
      setPulledRegions((prev) => {
        const cur = prev[product] || [];
        return { ...prev, [product]: cur.includes(fetchRegion) ? cur : [...cur, fetchRegion] };
      });
      if (data.errors[product]) {
        setProductErrors((prev) => ({ ...prev, [product]: `${fetchRegion}: ${data.errors[product]}` }));
      }
    } catch (e) {
      setProductErrors((prev) => ({ ...prev, [product]: `${fetchRegion}: ${e}` }));
    } finally {
      setLoadingProduct(null);
    }
  }

  function onAddSimple(
    kind: "redis" | "cdb" | "tke" | "cos",
    items: { id: string; name: string; region: string; extra?: Record<string, unknown> }[],
  ) {
    setResources((prev) => {
      const used = aliasesOf(prev);
      const list = [...prev[kind]];
      for (const item of items) {
        if (list.some((x) => x.id === item.id)) continue;
        const next: ResourceItem = {
          id: item.id,
          alias: uniqueAlias(item.name || item.id, used),
          name: item.name,
          region: item.region,
          extra: item.extra,
        };
        list.push(next);
      }
      return { ...prev, [kind]: list };
    });
  }

  function onRemoveSimple(kind: "redis" | "cdb" | "tke" | "cos", id: string) {
    setResources((prev) => ({ ...prev, [kind]: prev[kind].filter((x) => x.id !== id) }));
  }

  function onAddTdsqlc(items: { clusterId: string; instanceId: string; name: string; region: string }[]) {
    setResources((prev) => {
      const used = aliasesOf(prev);
      const list = [...prev.tdsqlc];
      for (const item of items) {
        if (list.some((x) => x.clusterId === item.clusterId)) continue;
        list.push({
          clusterId: item.clusterId,
          instanceId: item.instanceId,
          alias: uniqueAlias(item.name || item.clusterId, used),
          name: item.name,
          region: item.region,
        });
      }
      return { ...prev, tdsqlc: list };
    });
  }

  function onRemoveTdsqlc(clusterId: string) {
    setResources((prev) => ({ ...prev, tdsqlc: prev.tdsqlc.filter((x) => x.clusterId !== clusterId) }));
  }

  function onAddTopics(
    items: { logsetId: string; logsetName: string; topicId: string; topicName: string; region: string }[],
  ) {
    setResources((prev) => {
      const used = aliasesOf(prev);
      const cls = prev.cls.map((ls) => ({ ...ls, topics: [...ls.topics] }));
      for (const item of items) {
        let logset = cls.find((x) => x.logsetId === item.logsetId);
        if (!logset) {
          logset = { logsetId: item.logsetId, logsetName: item.logsetName, region: item.region, topics: [] };
          cls.push(logset);
        }
        if (logset.topics.some((t) => t.id === item.topicId)) continue;
        logset.topics.push({
          id: item.topicId,
          alias: uniqueAlias(item.topicName || item.topicId, used),
          name: item.topicName,
        });
      }
      return { ...prev, cls };
    });
  }

  function onRemoveTopic(logsetId: string, topicId: string) {
    setResources((prev) => ({
      ...prev,
      cls: prev.cls
        .map((ls) =>
          ls.logsetId === logsetId ? { ...ls, topics: ls.topics.filter((t) => t.id !== topicId) } : ls,
        )
        .filter((ls) => ls.topics.length > 0),
    }));
  }

  function onAlias(kind: string, id: string, alias: string, logsetId?: string) {
    setResources((prev) => {
      if (kind === "redis" || kind === "cdb" || kind === "tke" || kind === "cos") {
        return { ...prev, [kind]: prev[kind].map((r) => (r.id === id ? { ...r, alias } : r)) };
      }
      if (kind === "tdsqlc") {
        return { ...prev, tdsqlc: prev.tdsqlc.map((r) => (r.clusterId === id ? { ...r, alias } : r)) };
      }
      return {
        ...prev,
        cls: prev.cls.map((ls) =>
          ls.logsetId === logsetId
            ? { ...ls, topics: ls.topics.map((t) => (t.id === id ? { ...t, alias } : t)) }
            : ls,
        ),
      };
    });
  }

  async function onGenerate(advance: boolean) {
    setGenError("");
    setDownloaded(false);
    setCopied(false);
    setGenerating(true);
    try {
      const result = await createInstallation(selection);
      if (latestSelection.current !== selection) {
        setGenError("选择已变化，请重新生成安装命令。");
        return;
      }
      setInstallation(result);
      setCopiedInstall(false);
      setDownloaded(true);
      if (advance) setStep((currentStep) => currentStep + 1);
    } catch (e) {
      setGenError(String(e));
    } finally {
      setGenerating(false);
    }
  }

  const counts = preview?.counts;
  const installCommand = installation
    ? `curl -fsSL '${new URL(installation.paths[agentClient], window.location.origin).href.replace(/'/g, "%27")}' | bash`
    : "";

  async function copyInstallCommand() {
    setInstallCopyError("");
    try {
      await copyText(installCommand);
      setCopiedInstall(true);
    } catch {
      setInstallCopyError("复制失败，请手动选择下方命令复制。");
    }
  }

  async function downloadProject() {
    setGenError("");
    try {
      await downloadZip(selection);
    } catch (e) {
      setGenError(String(e));
    }
  }
  const slug = projectSlug(name);
  const mcpConfigPreview = mcpSnippet(slug, "填写 SecretId", "填写 SecretKey");
  const mcpConfigCopy = mcpSnippet(slug, secretId.trim() || "填写 SecretId", secretKey.trim() || "填写 SecretKey");
  const dockerCommandsPreview = dockerRunSnippet(slug);
  const dockerCommandsCopy = dockerRunSnippet(slug, {
    secretId: secretId.trim(),
    secretKey: secretKey.trim(),
    authToken,
  });
  const httpMcpPreview = httpMcpSnippet(slug, "填写 TCMCP_AUTH_TOKEN");
  const httpMcpCopy = httpMcpSnippet(slug, authToken);

  async function copyMcpConfig() {
    try {
      await copyText(mcpConfigCopy);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  async function copyDockerCommands() {
    try {
      await copyText(`${dockerCommandsCopy}\n\n${httpMcpCopy}`);
      setCopiedDocker(true);
    } catch {
      setCopiedDocker(false);
    }
  }

  async function copyHttpMcp() {
    try {
      await copyText(httpMcpCopy);
      setCopiedHttp(true);
    } catch {
      setCopiedHttp(false);
    }
  }

  const productToolCount = (id: string) =>
    (preview?.grouped[id] ?? []).filter((t) => t.ready && t.enabled).length;

  const steps = useMemo(() => buildSteps(products), [products]);
  const current = steps[Math.min(step, steps.length - 1)] ?? steps[0];
  const resourceProduct = current.product;
  const schemaIndex = steps.findIndex((s) => s.id === "schema");
  const canNext =
    current.id === "welcome"
      ? true
      : current.id === "project"
        ? Boolean(name.trim() && target.trim())
        : current.id === "products"
          ? products.length > 0
          : true;

  useEffect(() => {
    if (step >= steps.length) {
      setStep(steps.length - 1);
      return;
    }
    if (current.id.startsWith("res-") && current.product && !products.includes(current.product)) {
      const idx = steps.findIndex((s) => s.id === "products");
      setStep(idx >= 0 ? idx : 0);
    }
  }, [current.id, current.product, products, step, steps]);

  function next() {
    if (!canNext) return;
    setStep((s) => Math.min(s + 1, steps.length - 1));
  }
  function back() {
    setStep((s) => Math.max(s - 1, 0));
  }

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <span className="logo" aria-hidden>
            <span />
          </span>
          <div>
            <h1>Tencent Cloud MCP Initializr</h1>
            <span>安装向导 · 按产品与资源生成 MCP 项目</span>
          </div>
        </div>
        <div className="status">
          <span className="stat">
            <em>{counts?.products ?? products.length}</em> products
          </span>
          <span className="stat">
            <em>{counts?.resources ?? 0}</em> resources
          </span>
          <span className={`stat${preview?.warn ? " warn" : ""}`}>
            <em>{counts?.tools ?? 0}</em>
            {preview ? `/${preview.tools.length + preview.skipped.length}` : ""} tools
          </span>
        </div>
      </header>

      <ol className="stepper" aria-label="安装步骤">
        {steps.map((s, i) => (
          <li key={s.id} className={i === step ? "on" : i < step ? "done" : ""}>
            <button
              type="button"
              onClick={() => (i <= step || (downloaded && s.id === "install")) && setStep(i)}
              disabled={i > step && !(downloaded && s.id === "install")}
            >
              <span className="num">{i < step ? "✓" : i + 1}</span>
              <span className="lab">{s.short}</span>
            </button>
          </li>
        ))}
      </ol>

      {preview?.warn && step >= schemaIndex ? <div className="warn-banner">{preview.warnMessage}</div> : null}

      <main className="wizard">
        <div className={`sheet${["schema", "install"].includes(current.id) || current.id.startsWith("res-") ? " wide" : ""}`}>
          {error ? <div className="err">{error}</div> : null}
          {current.id === "welcome" ? (
            <section className="intro">
              <p className="eyebrow">安装向导</p>
              <h2>一步一个产品，生成精简的运维 MCP</h2>
              <p className="lead">
                只把你勾选的产品和云资源打进项目。资源 ID 写在配置里，模型只能看到别名，避免工具太多产生幻觉。
              </p>
              <ul className="guide">
                <li>
                  <strong>1. 项目配置</strong>
                  <span>名称和环境别名</span>
                </li>
                <li>
                  <strong>2. 项目与产品</strong>
                  <span>选择 Redis / CDB / TDSQL-C / TKE / CLS</span>
                </li>
                <li>
                  <strong>3. 云资源绑定</strong>
                  <span>每个产品单独一步：拉取后点击资源即可绑定</span>
                </li>
                <li>
                  <strong>4. MCP Schema</strong>
                  <span>预览工具，生成一键安装命令</span>
                </li>
              </ul>
            </section>
          ) : null}

          {current.id === "project" ? (
            <section>
              <h2>项目配置</h2>
              <p className="hint">这些信息会出现在生成项目的 README 和 mcp.json 里。</p>
              <div className="grid-2">
                <Field label="项目名">
                  <input value={name} onChange={(e) => setName(e.target.value)} />
                </Field>
                <Field label="环境别名 target">
                  <input className="mono" value={target} onChange={(e) => setTarget(e.target.value)} />
                </Field>
              </div>
              <Field label="说明">
                <input value={description} onChange={(e) => setDescription(e.target.value)} />
              </Field>
              <div className="callout">
                下一步会填写云 API 密钥。密钥只用于当次拉取资源，<strong>不会写入 zip、也不会落盘</strong>。
              </div>
              <div className="grid-2">
                <Field label="SecretId">
                  <input value={secretId} onChange={(e) => setSecretId(e.target.value)} autoComplete="off" spellCheck={false} />
                </Field>
                <Field label="SecretKey">
                  <input type="password" value={secretKey} onChange={(e) => setSecretKey(e.target.value)} autoComplete="off" />
                </Field>
              </div>
              <Field label="云项目 ProjectId（可选过滤）">
                <input value={projectId} onChange={(e) => setProjectId(e.target.value)} placeholder="留空则不过滤" />
              </Field>
            </section>
          ) : null}

          {current.id === "products" ? (
            <section>
              <h2>项目与产品</h2>
              <p className="hint">只勾选这个 MCP 真正需要的产品。选得越少，模型越不容易用错工具。工具会在下一步绑定资源后才写入 zip，现在看到「待绑定」是正常的。</p>
              <div className="product-grid">
                {(meta?.products ?? []).map((p) => (
                  <ProductCard
                    key={p.id}
                    product={p}
                    on={products.includes(p.id)}
                    toolCount={productToolCount(p.id)}
                    onToggle={() => toggleProduct(p.id)}
                  />
                ))}
              </div>
              {!products.length ? <p className="field-err">至少选择一个产品才能继续。</p> : null}
            </section>
          ) : null}

          {resourceProduct ? (
            <section>
              <h2>{current.title}</h2>
              <p className="hint">选一个地域拉取，点击左侧资源即可绑定；换一个地域再拉、再点。右侧是已经绑定的全部资源。</p>
              {!secretId || !secretKey ? (
                <div className="callout">还没填 SecretId / SecretKey。请回到「项目配置」补上，否则无法拉取。</div>
              ) : null}
              <ResourcePanel
                products={[resourceProduct]}
                productMeta={meta?.products ?? []}
                regions={resolveRegions(meta?.regions)}
                productRegions={productRegions}
                pulledRegions={pulledRegions}
                catalog={catalog}
                bound={resources}
                loadingProduct={loadingProduct}
                productErrors={productErrors}
                onRegion={setProductRegion}
                onFetchProduct={onFetchProduct}
                onAddSimple={onAddSimple}
                onRemoveSimple={onRemoveSimple}
                onAddTdsqlc={onAddTdsqlc}
                onRemoveTdsqlc={onRemoveTdsqlc}
                onAddTopics={onAddTopics}
                onRemoveTopic={onRemoveTopic}
                onAlias={onAlias}
              />
            </section>
          ) : null}

          {current.id === "schema" ? (
            <section>
              <h2>MCP Schema</h2>
              <p className="hint">这里就是生成后模型看到的 tools/list。关掉不需要的工具，enum 来自你绑定的别名。</p>
              <SchemaInspector preview={preview} mode={mode} onMode={setMode} onToggleTool={toggleTool} />
            </section>
          ) : null}

          {current.id === "install" && downloaded ? (
            <section>
              <p className="eyebrow">生成成功</p>
              <h2>安装与部署</h2>
              <p className="hint">选择你的客户端，复制命令到终端运行，即可自动下载项目、安装依赖并配置 MCP。</p>

              {installation ? (
                <div className="download-help one-click-install">
                  <div className="download-help-head">
                    <div>
                      <strong>一键安装到本机</strong>
                      <p>支持 macOS / Linux，需要 Python 3.11+。保留其他 MCP 服务，生成配置快照和一键回滚脚本。</p>
                    </div>
                    <button className="btn primary" type="button" onClick={copyInstallCommand}>
                      {copiedInstall ? "已复制" : "复制安装命令"}
                    </button>
                  </div>
                  <Field label="Agent 客户端">
                    <select value={agentClient} onChange={(e) => { setAgentClient(e.target.value as AgentClient); setCopiedInstall(false); }}>
                      <option value="cursor">Cursor</option>
                      <option value="codebuddy">CodeBuddy</option>
                      <option value="codex">Codex</option>
                      <option value="workbuddy">WorkBuddy</option>
                    </select>
                  </Field>
                  <pre>{installCommand}</pre>
                  {installCopyError ? <p className="field-err">{installCopyError}</p> : null}
                  <p>运行时在终端输入 SecretId / SecretKey，输入和粘贴内容会正常显示。密钥仅保存在本机客户端配置中。安装后刷新 MCP 或重启客户端。</p>
                  <p>安装 ID：<code>{installation.id}</code> · 有效期至 {new Date(installation.expiresAt * 1000).toLocaleString()}。链接包含所选资源，请仅分享给可信人员。</p>
                  <button className="btn" type="button" onClick={downloadProject}>下载 ZIP（手动安装 / Docker）</button>
                </div>
              ) : null}

              <details>
                <summary>手动安装与 Docker 部署</summary>
              <div className="install-grid">
                <div className="download-help">
                  <div className="download-help-head">
                    <div>
                      <strong>方式一：接入 Cursor</strong>
                      <p>启动器会检查 Python 3.11+，首次连接自动创建环境和安装依赖。点复制会带上刚才填写的密钥。</p>
                    </div>
                    <button className="btn" type="button" onClick={copyMcpConfig}>
                      {copied ? "已复制" : "复制配置"}
                    </button>
                  </div>
                  <ol>
                    <li>解压 zip，并把下方路径改成解压目录的绝对路径。</li>
                    <li>将配置加入 <code>~/.cursor/mcp.json</code> 或项目的 <code>.cursor/mcp.json</code>。</li>
                    <li>粘贴后把路径改成实际目录，刷新 MCP。密钥已写入复制内容，无需再填。</li>
                  </ol>
                  <pre>{mcpConfigPreview}</pre>
                </div>

                <div className="deploy-help">
                  <div className="download-help-head">
                    <div>
                      <strong>方式二：Docker Server</strong>
                      <p>
                        HTTP 强制 Bearer 鉴权。没有 <code>TCMCP_AUTH_TOKEN</code> 容器起不来。
                        下面 token 是本次生成的，复制命令会带上密钥和 token。
                      </p>
                    </div>
                    <button className="btn" type="button" onClick={copyDockerCommands}>
                      {copiedDocker ? "已复制" : "复制启动命令"}
                    </button>
                  </div>
                  <ol>
                    <li>解压后在项目目录构建镜像。</li>
                    <li>用环境变量注入云 API 密钥和 <code>TCMCP_AUTH_TOKEN</code>。</li>
                    <li>Cursor 用下方 HTTP 配置连接 <code>http://127.0.0.1:8099/mcp</code>。</li>
                  </ol>
                  <pre>{dockerCommandsPreview}</pre>
                  <div className="download-help-head" style={{ marginTop: 12 }}>
                    <p>Cursor 连 Docker HTTP（鉴权头）</p>
                    <button className="btn" type="button" onClick={copyHttpMcp}>
                      {copiedHttp ? "已复制" : "复制 HTTP 配置"}
                    </button>
                  </div>
                  <pre>{httpMcpPreview}</pre>
                  <p>
                    <code>/healthz</code> 不鉴权；<code>/mcp</code> 必须
                    <code> Authorization: Bearer &lt;token&gt;</code>。
                    生产环境放在 TLS 网关后面，并轮换 token。
                  </p>
                </div>
              </div>
              <p className="secret-note">密钥只通过环境变量注入，不要写进 config.yaml、Dockerfile 或提交到 Git。</p>
              </details>
            </section>
          ) : null}

          <footer className="wizard-foot">
            <button className="btn" type="button" onClick={back} disabled={step === 0}>
              上一步
            </button>
            <div className="foot-right">
              {current.id === "schema" ? (
                <>
                  <span className="ready-count">{preview?.registered.length ?? 0} 个工具将安装到客户端</span>
                  <button
                    className="btn primary"
                    type="button"
                    disabled={!products.length || generating}
                    onClick={() => onGenerate(true)}
                  >
                    {generating ? "生成中…" : "生成安装命令"}
                  </button>
                </>
              ) : current.id === "install" ? (
                <button className="btn primary" type="button" disabled={generating} onClick={() => onGenerate(false)}>
                  {generating ? "生成中…" : "重新生成安装命令"}
                </button>
              ) : (
                <button className="btn primary" type="button" disabled={!canNext} onClick={next}>
                  {current.id === "welcome" ? "开始配置" : "下一步"}
                </button>
              )}
              {genError ? <div className="err">{genError}</div> : null}
            </div>
          </footer>
        </div>
      </main>
    </div>
  );
}

function mergeCatalog(
  prev: ResourceCatalog,
  incoming: ResourceCatalog,
  product: string,
  region: string,
): ResourceCatalog {
  const next: ResourceCatalog = { ...prev, errors: { ...prev.errors, ...incoming.errors } };
  if (product === "cls") {
    const keep = prev.cls.filter((ls) => (ls.region || "") !== region);
    next.cls = [...keep, ...incoming.cls.map((ls) => ({ ...ls, region: ls.region || region }))];
    return next;
  }
  const key = product as "redis" | "cdb" | "tdsqlc" | "tke" | "cos";
  const keep = (prev[key] ?? []).filter((item) => (item.region || "") !== region);
  next[key] = [...keep, ...(incoming[key] ?? []).map((item) => ({ ...item, region: item.region || region }))];
  return next;
}

function fallbackRegion(res: BoundResources, productRegions: Record<string, string>): string {
  for (const list of [res.redis, res.cdb, res.tke, res.cos]) {
    for (const item of list) if (item.region) return item.region;
  }
  for (const item of res.tdsqlc) if (item.region) return item.region;
  for (const ls of res.cls) if (ls.region) return ls.region;
  for (const id of Object.values(productRegions)) {
    if (id) return id;
  }
  return "ap-guangzhou";
}

function aliasesOf(res: BoundResources): Set<string> {
  const used = new Set<string>();
  for (const r of res.redis) used.add(r.alias);
  for (const r of res.cdb) used.add(r.alias);
  for (const r of res.tdsqlc) used.add(r.alias);
  for (const r of res.tke) used.add(r.alias);
  for (const r of res.cos) used.add(r.alias);
  for (const ls of res.cls) for (const t of ls.topics) used.add(t.alias);
  return used;
}

function projectSlug(value: string): string {
  return value.trim().replace(/[^a-zA-Z0-9._-]+/g, "-").replace(/^[-._]+|[-._]+$/g, "") || "tcmcp-project";
}

function mcpSnippet(slug: string, secretId: string, secretKey: string): string {
  return JSON.stringify(
    {
      mcpServers: {
        [slug]: {
          command: "/bin/bash",
          args: [`/absolute/path/to/${slug}/run.sh`],
          env: {
            TENCENT_SECRET_ID: secretId,
            TENCENT_SECRET_KEY: secretKey,
          },
        },
      },
    },
    null,
    2,
  );
}

function httpMcpSnippet(slug: string, token: string): string {
  return JSON.stringify(
    {
      mcpServers: {
        [`${slug}-http`]: {
          url: "http://127.0.0.1:8099/mcp",
          headers: { Authorization: `Bearer ${token}` },
        },
      },
    },
    null,
    2,
  );
}

function dockerRunSnippet(
  slug: string,
  filled?: { secretId: string; secretKey: string; authToken: string },
): string {
  const sid = filled?.secretId ? `'${filled.secretId}'` : "";
  const skey = filled?.secretKey ? `'${filled.secretKey}'` : "";
  const token = filled?.authToken ? `'${filled.authToken}'` : "";
  const env = filled
    ? `-e TENCENT_SECRET_ID=${sid} \\
  -e TENCENT_SECRET_KEY=${skey} \\
  -e TCMCP_AUTH_TOKEN=${token} \\`
    : `-e TENCENT_SECRET_ID \\
  -e TENCENT_SECRET_KEY \\
  -e TCMCP_AUTH_TOKEN \\`;
  return `cd /absolute/path/to/${slug}
docker build -t ${slug} .
docker run --rm -p 127.0.0.1:8099:8099 \\
  ${env}
  ${slug}`;
}

async function copyText(text: string): Promise<void> {
  if (navigator.clipboard?.writeText && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }

  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.left = "-9999px";
  textarea.style.top = "0";
  document.body.appendChild(textarea);
  try {
    textarea.focus();
    textarea.select();
    textarea.setSelectionRange(0, text.length);
    const copied = document.execCommand("copy");
    if (!copied) throw new Error("copy failed");
  } finally {
    document.body.removeChild(textarea);
  }
}

function newAuthToken(): string {
  const bytes = new Uint8Array(24);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="field-block">
      <span>{label}</span>
      {children}
    </label>
  );
}
