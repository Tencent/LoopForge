import { chromium } from "/home/toan/Documents/LoopForge/chatbot/node_modules/playwright/index.mjs";
import fs from "fs";
import path from "path";

const OUT_DIR = "/home/toan/Pictures/loopforge-vi";
const REPORT = path.join(OUT_DIR, "sweep-report.json");
const REPO = "/home/toan/Documents/LoopForge";
const BASE = "http://127.0.0.1:8090";
const PROJECT = "welcome";
const cookie = fs.readFileSync("/tmp/lf-agentflow-user.cookie", "utf8").trim();
const passName = process.argv[2] || "sweep";

const ATTRS = [
  "placeholder", "title", "alt", "aria-label", "aria-placeholder",
  "aria-description", "aria-roledescription", "data-tip", "data-tooltip",
  "data-title", "value",
];

const GLOSSARY = [
  "Vibe Coding", "LoopForge Cloud", "API Key", "LoopForge", "GitHub", "CodeBuddy",
  "Gongfeng", "TAPD", "Dockerfile", "Docker", "CodeBuddy", "Codex",
  "Tokens", "Token", "SOP", "MCP", "Agent", "Agents", "PR", "MR", "CLI", "KPI",
  "P95", "P99", "CNY", "Hy", "ID", "URL", "HTTP", "HTTPS", "SSH", "OpenSSH", "PEM", "JSON", "CSV",
  "PDF", "SQL", "AI", "IDE", "UI", "UX", "CPU", "GPU", "RAM", "OS", "Bug",
  "Trace", "Run", "USD", "Owner", "Cloud", "Model", "Models", "Issue", "Issues",
  "YAML", "XML", "SVG", "PNG", "JWT", "OAuth", "SSL", "TLS", "LLM", "GPT",
  "Vite", "ECharts", "MySQL", "Redis", "Node", "npm", "Git", "CI", "CD", "QA",
  "PM", "SDK", "SHA", "HMAC", "KB", "MB", "GB", "TB", "OK", "TODO", "Workspace",
  "workspace", "Workflow", "workflow", "Sandbox", "sandbox", "Repository",
  "repository", "repo", "Repo", "cache", "Cache", "Bugfix", "HTML", "CSS",
  "JS", "TS", "Go", "Owner", "Docs", "docs",
];

function deriveHashes() {
  const html = fs.readFileSync(path.join(REPO, "web/index.html"), "utf8");
  const app = fs.readFileSync(path.join(REPO, "web/app.js"), "utf8");
  const views = new Set();
  for (const m of html.matchAll(/data-view="([^"]+)"/g)) views.add(m[1]);
  for (const m of html.matchAll(/data-nav-match="([^"]+)"/g)) {
    for (const part of m[1].split(/\s+/)) if (part) views.add(part);
  }
  for (const m of app.matchAll(/if\(name==='([^']+)'\)name='([^']+)'/g)) {
    views.add(m[1]);
    views.add(m[2]);
  }
  for (const v of ["rules", "skills", "dockerfiles", "environments", "sandboxes", "flow", "access", "assets", "playbooks", "management", "dashboard"]) {
    views.add(v);
  }
  // Prefer rail order, then extras
  const preferred = [];
  for (const m of html.matchAll(/class="[^"]*nav[^"]*"[^>]*data-view="([^"]+)"|data-view="([^"]+)"[^>]*class="[^"]*nav/g)) {
    preferred.push(m[1] || m[2]);
  }
  const rest = [...views].filter(v => !preferred.includes(v)).sort();
  const hashes = [...new Set([...preferred, ...rest])];
  return hashes;
}

function cookieHeader() {
  return { Cookie: `agentflow_user=${cookie}`, "Content-Type": "application/json" };
}

async function api(method, url, body) {
  const res = await fetch(url, {
    method,
    headers: cookieHeader(),
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json = null;
  try { json = JSON.parse(text); } catch {}
  return { status: res.status, json, text: text.slice(0, 500) };
}

async function scanPage(page, opts = {}) {
  return await page.evaluate(({ ATTRS, GLOSSARY, includeTextareas }) => {
    const HAN = /[\u3400-\u9fff]/g;
    const VI = /[àáảãạăằắẳẵặâầấẩẫậèéẻẽẹêềếểễệìíỉĩịòóỏõọôồốổỗộơờớởỡợùúủũụưừứửữựỳýỷỹỵđÀÁẢÃẠĂẰẮẲẴẶÂẦẤẨẪẬÈÉẺẼẸÊỀẾỂỄỆÌÍỈĨỊÒÓỎÕỌÔỒỐỔỖỘƠỜỚỞỠỢÙÚỦŨỤƯỪỨỬỮỰỲÝỶỸỴĐ]/;
    const SKIP = new Set(["SCRIPT", "STYLE", "NOSCRIPT", "CODE", "PRE", ...(includeTextareas ? [] : ["TEXTAREA"])]);
    const SKIP_SEL = ".monaco-editor, .xterm, .log-line, .run-log, [contenteditable='true']";
    const glossary = [...GLOSSARY].sort((a, b) => b.length - a.length);

    function visible(el) {
      if (!el) return false;
      const node = el.nodeType === 1 ? el : el.parentElement;
      if (!node) return false;
      const style = getComputedStyle(node);
      if (!style) return true;
      if (style.display === "none" || style.visibility === "hidden" || Number(style.opacity) === 0) return false;
      const r = node.getBoundingClientRect();
      if (r.width === 0 && r.height === 0 && style.position !== "fixed") return false;
      return true;
    }
    function skipped(el) {
      if (!el) return true;
      if (SKIP.has(el.tagName)) return true;
      if (el.closest && el.closest(SKIP_SEL)) return true;
      return false;
    }
    const EN_LEFTOVER = new Set([
      "including","enough","compare","excludes","current","source","here","average","avg",
      "interventions","pause","resume","failed","retry","configured","created","please",
      "invalid","error","success","default","load","loading","click","view","show","hide",
      "open","close","save","delete","edit","using","used","already","still","with","from",
      "this","that","they","have","been","were","said","which","their","will","other","about",
      "many","then","them","these","into","than","only","come","over","such","also","after",
      "first","well","year","work","time","very","when","just","like","long","make","most",
      "take","could","should","would","the","and","for","you","are","not","but","all",
      "had","her","was","our","out","get","has","him","his","how","new","now","old","see",
      "who","did","its","let","put","say","she","too","use","according","instead","directly",
      "matching","changing","rework","steps","human","approval","appear","awaiting","handle",
      "escalate","follow","same","yet","etc","land","hand","it"
    ]);
    function soupIn(raw) {
      const t = String(raw || "").replace(/\s+/g, " ").trim();
      if (!t || !VI.test(t)) return null;
      if (HAN.test(t)) return null; // counted as CJK
      // ECharts built-in English aria, mixed with our Vietnamese series names.
      if (/^This is a chart\b/i.test(t)) return null;
      let masked = t;
      masked = masked.replace(/https?:\/\/\S+/g, " ");
      masked = masked.replace(/\b[0-9a-f]{7,}\b/gi, " ");
      masked = masked.replace(/\b[\w./-]+\.(md|js|jsx|ts|go|sql|json|ya?ml|html|css)\b/gi, " ");
      masked = masked.replace(/\b[a-z]+[A-Z][A-Za-z0-9]*\b/g, " ");
      masked = masked.replace(/\b[A-Za-z]+(?:_[A-Za-z0-9]+)+\b/g, " ");
      for (const g of glossary) {
        const re = new RegExp(g.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "gi");
        masked = masked.replace(re, " ");
      }
      const words = (masked.match(/[A-Za-z]{2,}/g) || []).filter((w) => EN_LEFTOVER.has(w.toLowerCase()));
      if (words.length) return { reason: "vi+en", words, sample: t.slice(0, 200) };
      const jamSrc = t.replace(/https?:\/\/\S+/g, " ").replace(/\b[0-9a-f]{7,}\b/gi, " ");
      let jamProbe = jamSrc;
      jamProbe = jamProbe.replace(/\b[A-Za-z]+[A-Z][A-Za-z0-9]*\b/g, " ");
      for (const g of glossary) {
        const re = new RegExp(g.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "gi");
        jamProbe = jamProbe.replace(re, " ");
      }
      if (/[a-zàáảãạăằắẳẵặâầấẩẫậèéẻẽẹêềếểễệìíỉĩịòóỏõọôồốổỗộơờớởỡợùúủũụưừứửữựỳýỷỹỵđ][A-Z]/.test(jamProbe)) {
        return { reason: "jammed-boundary", words: [], sample: t.slice(0, 200) };
      }
      return null;
    }

    function scan(doc, frameLabel) {
      const hits = [];
      const soups = [];
      let textCount = 0, attrCount = 0;
      const root = doc.body || doc.documentElement;
      if (!root) return { textCount, attrCount, hits, soups };
      const tw = doc.createTreeWalker(root, NodeFilter.SHOW_TEXT);
      let n;
      while ((n = tw.nextNode())) {
        const parent = n.parentElement;
        if (skipped(parent) || !visible(parent)) continue;
        const t = n.nodeValue || "";
        const m = t.match(HAN);
        if (m) {
          textCount += m.length;
          hits.push({ type: "text", frame: frameLabel, sample: t.trim().slice(0, 160), n: m.length });
        }
        const soup = soupIn(t);
        if (soup) soups.push({ type: "text", frame: frameLabel, ...soup });
      }
      for (const el of root.querySelectorAll("*")) {
        if (skipped(el) || !visible(el)) continue;
        for (const a of ATTRS) {
          let v = el.getAttribute && el.getAttribute(a);
          if (a === "value" && el.value && (el.tagName === "INPUT" || el.tagName === "BUTTON" || el.tagName === "TEXTAREA")) v = el.value;
          if (!v) continue;
          const m = String(v).match(HAN);
          if (m) {
            attrCount += m.length;
            hits.push({ type: "attr:" + a, frame: frameLabel, sample: String(v).slice(0, 160), n: m.length });
          }
          const soup = soupIn(v);
          if (soup) soups.push({ type: "attr:" + a, frame: frameLabel, ...soup });
        }
      }
      if (doc.title && HAN.test(doc.title)) {
        const m = doc.title.match(HAN);
        textCount += m.length;
        hits.push({ type: "title", frame: frameLabel, sample: doc.title, n: m.length });
      }
      if (doc.title) {
        const soup = soupIn(doc.title);
        if (soup) soups.push({ type: "title", frame: frameLabel, ...soup });
      }
      return { textCount, attrCount, hits, soups };
    }
    const root = scan(document, "top");
    const frames = [];
    for (const iframe of document.querySelectorAll("iframe")) {
      try {
        const doc = iframe.contentDocument;
        if (!doc) continue;
        const r = scan(doc, iframe.src || "iframe");
        root.textCount += r.textCount;
        root.attrCount += r.attrCount;
        root.hits.push(...r.hits);
        root.soups.push(...r.soups);
        frames.push({ src: iframe.src, textCount: r.textCount, attrCount: r.attrCount, soup: r.soups.length });
      } catch {
        frames.push({ src: iframe.src, error: "cross-origin" });
      }
    }
    return {
      htmlLang: document.documentElement.lang,
      dataLang: document.documentElement.dataset.lang,
      title: document.title,
      textCount: root.textCount,
      attrCount: root.attrCount,
      total: root.textCount + root.attrCount,
      soupCount: root.soups.length,
      frames,
      hits: root.hits,
      soups: root.soups,
    };
  }, { ATTRS, GLOSSARY, includeTextareas: !!opts.includeTextareas });
}

function log(msg) {
  const line = new Date().toISOString() + " " + msg;
  fs.appendFileSync("/tmp/cjk-sweep.log", line + "\n");
}

async function settle(page, ms = 400) {
  try { await page.waitForLoadState("domcontentloaded", { timeout: 4000 }); } catch {}
  await page.waitForTimeout(ms);
}

async function record(page, states, name, url, opts = {}) {
  const counts = await scanPage(page, opts);
  log("record " + name + " cjk=" + counts.total + " soup=" + counts.soupCount);
  states.push({
    name,
    url: url || page.url(),
    title: counts.title,
    lang: counts.htmlLang,
    textCount: counts.textCount,
    attrCount: counts.attrCount,
    total: counts.total,
    soupCount: counts.soupCount,
    frames: counts.frames,
    hits: (counts.hits || []).slice(0, 50),
    soups: (counts.soups || []).slice(0, 50),
  });
  return counts;
}

async function hoverTitled(page) {
  const sels = "[title], [data-tip], [data-tooltip], [aria-label], [data-title]";
  const locators = page.locator(sels);
  const n = Math.min(await locators.count(), 8);
  for (let i = 0; i < n; i++) {
    try { await locators.nth(i).hover({ timeout: 200 }); } catch {}
  }
}

async function clickAll(page, selector, limit = 12) {
  const loc = page.locator(selector);
  const n = Math.min(await loc.count(), limit);
  for (let i = 0; i < n; i++) {
    const el = loc.nth(i);
    try {
      if (!(await el.isVisible({ timeout: 200 }))) continue;
      const label = ((await el.innerText().catch(() => "")) || "").trim();
      if (/xóa|xóa hết|thu hồi|revoke|delete|卸载|删除|撤销|登出|đăng xuất/i.test(label)) continue;
      await el.click({ timeout: 700 });
      await page.waitForTimeout(150);
    } catch {}
  }
}

async function dismissOverlays(page) {
  try { await page.keyboard.press("Escape"); } catch {}
  await page.waitForTimeout(150);
  for (const sel of [".modal-close", "button.modal-close", "[data-close]", "button:has-text('Đóng')", "button:has-text('Hủy')", "button:has-text('关闭')", "button:has-text('取消')"]) {
    const loc = page.locator(sel).last();
    try {
      if (await loc.isVisible({ timeout: 200 })) await loc.click({ timeout: 400 });
    } catch {}
  }
}

async function clickSel(page, sel) {
  const loc = page.locator(sel).first();
  try {
    if (await loc.count() && await loc.isVisible({ timeout: 500 })) {
      await loc.click({ timeout: 1500 });
      await settle(page, 500);
      return true;
    }
  } catch {}
  return false;
}

async function waitObs(page) {
  try {
    await page.waitForSelector("#observabilityView .obs-fbar, #observabilityView .obs-kpi, [data-obs-tab], .obs-card, .obs-kpi-group", { timeout: 10000 });
  } catch {}
  await settle(page, 800);
}

async function exploreView(page) {
  await clickAll(page, "[role=tab]", 16);
  await clickAll(page, "[data-asset-type-tab], [data-obs-tab], [data-ai-schedule-tab], .obs-tab, .issue-tab, .sliding-tab", 16);
  await hoverTitled(page);
  await clickAll(page, "details > summary, [aria-haspopup='menu'], [aria-haspopup='true'], .fd-icon-btn, .hui-menu-trigger, .heading-menu, button.select, .select-trigger", 12);
  await page.waitForTimeout(200);
  await dismissOverlays(page);
}


async function exploreSettings(page, states) {
  // Personal settings: filters, credential rows, add-credential wizard, Git dialogs, user menu.
  await page.evaluate(() => { location.hash = "#/settings"; });
  await settle(page, 900);
  try {
    await page.waitForSelector("#personalSettingsContent, #settingsView", { timeout: 8000 });
  } catch {}
  await record(page, states, "settings-personal", page.url());

  for (const id of ["all", "configured", "missing"]) {
    if (await clickSel(page, `[data-credential-filter="${id}"]`)) {
      await settle(page, 400);
      await record(page, states, `settings-filter-${id}`, page.url());
    }
  }
  await clickSel(page, `[data-credential-filter="all"]`);

  // Expand a few credential rows (skip destructive actions via clickAll).
  const rows = page.locator("#personalSettingsContent [data-credential-key], #credentialPanel [data-credential-key], .credential-row, .credential-card");
  const rowN = Math.min(await rows.count().catch(() => 0), 8);
  for (let i = 0; i < rowN; i++) {
    try {
      const row = rows.nth(i);
      if (!(await row.isVisible({ timeout: 300 }))) continue;
      await row.click({ timeout: 800 });
      await settle(page, 350);
      await record(page, states, `settings-credential-row-${i}`, page.url());
      // Open environment / protocol dropdowns inside the expanded row.
      const selects = row.locator("select, [aria-haspopup='listbox']");
      const sN = Math.min(await selects.count().catch(() => 0), 3);
      for (let s = 0; s < sN; s++) {
        try {
          await selects.nth(s).click({ timeout: 500 });
          await settle(page, 250);
          await record(page, states, `settings-credential-row-${i}-dropdown-${s}`, page.url());
          await page.keyboard.press("Escape");
        } catch {}
      }
    } catch {}
  }

  // Add-credential wizard: kind list + a few kinds + next-step validation.
  const addBtn = page.locator("[data-credential-add], button:has-text('Thêm thông tin xác thực'), button:has-text('添加凭证')").first();
  try {
    if (await addBtn.count() && await addBtn.isVisible({ timeout: 800 })) {
      await addBtn.click({ timeout: 2000 });
      await settle(page, 600);
      await record(page, states, "settings-wizard-kinds", page.url());
      const kinds = page.locator("[data-wizard-kind], .credential-opt[role='radio']");
      const kN = Math.min(await kinds.count().catch(() => 0), 10);
      for (let i = 0; i < kN; i++) {
        try {
          const kind = kinds.nth(i);
          if (!(await kind.isVisible({ timeout: 200 }))) continue;
          const key = (await kind.getAttribute("data-wizard-kind").catch(() => "")) || `i${i}`;
          await kind.click({ timeout: 800 });
          await settle(page, 300);
          await record(page, states, `settings-wizard-kind-${key}`, page.url());
        } catch {}
      }
      // Advance if Next is enabled (may show validation without secrets).
      const next = page.locator("[data-wizard-next], button:has-text('Bước tiếp'), button:has-text('Tiếp')").first();
      try {
        if (await next.count() && await next.isVisible({ timeout: 400 }) && await next.isEnabled({ timeout: 200 })) {
          await next.click({ timeout: 1000 });
          await settle(page, 500);
          await record(page, states, "settings-wizard-step", page.url());
          // Open dropdowns inside the wizard (environment, protocol).
          const wsel = page.locator(".credential-dialog select, [role='dialog'] select");
          const wN = Math.min(await wsel.count().catch(() => 0), 3);
          for (let s = 0; s < wN; s++) {
            try {
              await wsel.nth(s).click({ timeout: 500 });
              await settle(page, 250);
              await record(page, states, `settings-wizard-dropdown-${s}`, page.url());
              await page.keyboard.press("Escape");
            } catch {}
          }
        }
      } catch {}
    }
  } catch {}
  await dismissOverlays(page);
  await page.keyboard.press("Escape").catch(() => {});

  // Git SSH / HTTPS management dialogs from credential actions.
  for (const [name, sel] of [
    ["settings-git-ssh", "button:has-text('Quản lý khóa'), [data-credential-key='gitSshKey'] button, button:has-text('Git SSH')"],
    ["settings-git-http", "button:has-text('Quản lý token'), [data-credential-key='gitHttpCredentials'] button, button:has-text('Git access token'), button:has-text('HTTPS')"],
  ]) {
    await page.evaluate(() => { location.hash = "#/settings"; });
    await settle(page, 500);
    const loc = page.locator(sel).first();
    try {
      if (await loc.count() && await loc.isVisible({ timeout: 800 })) {
        await loc.click({ timeout: 1500 });
        await settle(page, 700);
        await record(page, states, name, page.url());
        const add = page.locator("[data-ssh-key-add], [data-http-cred-add], button:has-text('Thêm khóa'), button:has-text('Thêm token')").first();
        if (await add.count() && await add.isVisible({ timeout: 400 }) && await add.isEnabled({ timeout: 200 }).catch(() => false)) {
          await add.click({ timeout: 1000 });
          await settle(page, 400);
          await record(page, states, name + "-form", page.url());
        }
      }
    } catch {}
    await dismissOverlays(page);
    await page.keyboard.press("Escape").catch(() => {});
  }

  // User profile popover: theme segment, account labels.
  try {
    const badge = page.locator("#userBadge, #userProfileWrap .user-badge").first();
    if (await badge.count()) {
      await badge.click({ timeout: 1500 });
      await settle(page, 500);
      await record(page, states, "settings-user-popover", page.url());
    }
  } catch {}
  await dismissOverlays(page);
  await page.keyboard.press("Escape").catch(() => {});

  // Access tokens page (settings nav).
  await page.evaluate(() => { location.hash = "#/guide"; });
  await settle(page, 800);
  try { await page.waitForSelector("#accessBody, #guideView", { timeout: 6000 }); } catch {}
  await record(page, states, "settings-access", page.url());
  await dismissOverlays(page);

  // Platform admin tabs, filters, add-host form (no destructive submit).
  await page.evaluate(() => { location.hash = "#/platform"; });
  await settle(page, 900);
  try { await page.waitForSelector("#platformAdminPanel, #platformView", { timeout: 6000 }); } catch {}
  await record(page, states, "settings-platform", page.url());
  const tabs = page.locator("[data-platform-tab]");
  const tN = Math.min(await tabs.count().catch(() => 0), 8);
  for (let i = 0; i < tN; i++) {
    try {
      const tab = tabs.nth(i);
      const id = (await tab.getAttribute("data-platform-tab").catch(() => "")) || `t${i}`;
      await tab.click({ timeout: 1000 });
      await settle(page, 600);
      await record(page, states, `settings-platform-tab-${id}`, page.url());
      // Filters on admins / accounts.
      for (const fid of ["all", "builtin", "granted"]) {
        await clickSel(page, `[data-source-filter="${fid}"]`);
      }
      await clickAll(page, "[data-platform-panel]:not([hidden]) details > summary, [data-platform-panel]:not([hidden]) select", 8);
      if (id === "sshTrust") {
        if (await clickSel(page, "[data-ssh-host-add], button:has-text('Thêm máy chủ')")) {
          await settle(page, 400);
          await record(page, states, "settings-platform-ssh-add", page.url());
          const manual = page.locator("[data-platform-panel]:not([hidden]) details > summary, .platform-ssh-host-manual > summary").first();
          try {
            if (await manual.isVisible({ timeout: 300 })) {
              await manual.click({ timeout: 500 });
              await settle(page, 250);
              await record(page, states, "settings-platform-ssh-manual", page.url());
            }
          } catch {}
        }
      }
      if (id === "sharedKey") {
        const sels = page.locator("[data-platform-panel]:not([hidden]) select, [data-platform-panel]:not([hidden]) input[type='checkbox']");
        const sN = Math.min(await sels.count().catch(() => 0), 6);
        // Just record the tab content; avoid toggling checkboxes that would dirty config.
        if (sN) await record(page, states, "settings-platform-shared-fields", page.url());
      }
    } catch {}
  }
  await dismissOverlays(page);
  await page.keyboard.press("Escape").catch(() => {});
}

async function exploreObservability(page, states) {
  await page.evaluate(() => { location.hash = "#/observability"; });
  await waitObs(page);
  await record(page, states, "obs-landed", page.url());

  const tabs = ["overview", "delivery"];
  const scopes = ["project", "user"];
  const grains = ["all", "day", "week", "month"];
  const metrics = ["tokens", "comp", "cost", "runs", "issues", "rate"];

  for (const tab of tabs) {
    await clickSel(page, `[data-obs-tab="${tab}"]`);
    await waitObs(page);
    await record(page, states, `obs-${tab}`, page.url());
    for (const scope of scopes) {
      await clickSel(page, `[data-obs-scope="${scope}"]`);
      await settle(page, 600);
      await waitObs(page);
      await record(page, states, `obs-${tab}-${scope}`, page.url());
      for (const grain of grains) {
        await clickSel(page, `[data-obs-grain="${grain}"]`);
        await settle(page, 550);
        await record(page, states, `obs-${tab}-${scope}-${grain}`, page.url());
      }
    }
    if (tab === "overview") {
      await clickSel(page, `[data-obs-scope="project"]`);
      await clickSel(page, `[data-obs-grain="week"]`);
      await settle(page, 400);
      for (const metric of metrics) {
        await clickSel(page, `[data-obs-metric="${metric}"]`);
        await settle(page, 450);
        await record(page, states, `obs-trend-${metric}`, page.url());
      }
    }
  }

  await clickSel(page, `[data-obs-tab="overview"]`);
  await clickSel(page, `[data-obs-scope="project"]`);
  await settle(page, 400);
  if (await clickSel(page, "#obsManageSources, button:has-text('Nguồn dữ liệu ngoài')")) {
    await settle(page, 800);
    await record(page, states, "obs-external-data-modal", page.url());
    await dismissOverlays(page);
  }

  if (await clickSel(page, "#exportObsReport, button:has-text('Xuất báo cáo')")) {
    await settle(page, 800);
    await record(page, states, "obs-export", page.url());
    await dismissOverlays(page);
  }

  if (await clickSel(page, "#obsPeriodInput, .hui-date-trigger, button.hui-date-trigger, .obs-period-input")) {
    await settle(page, 500);
    await record(page, states, "obs-period-picker", page.url());
    await dismissOverlays(page);
  }
}

async function shot(page, name) {
  const dest = path.join(OUT_DIR, name);
  await page.screenshot({ path: dest, fullPage: true });
  log("shot " + dest);
}


async function pageOrFrame(page) {
  // /chatbot/ is the app itself; the workbench hosts it in an iframe.
  const frames = page.frames();
  for (const f of frames) {
    const url = f.url() || "";
    if (url.includes("/chatbot") || url.includes("/native-chat")) return f;
  }
  return page;
}

async function exploreComposer(page, states) {
  const target = page;
  await target.goto(`${BASE}/chatbot/?projectId=${PROJECT}`, { waitUntil: "domcontentloaded", timeout: 25000 });
  await settle(target, 1200);
  // dismiss quick-access wizard if it appears
  for (const sel of [
    "button:has-text('Để sau')", "button:has-text('Bỏ qua')", "button:has-text('Đóng')",
    "button:has-text('Sau')", ".quick-access-wizard [aria-label='Đóng']",
  ]) {
    try {
      const loc = target.locator(sel).first();
      if (await loc.count() && await loc.isVisible({ timeout: 400 })) {
        await loc.click({ timeout: 1000 });
        await settle(target, 400);
      }
    } catch {}
  }
  try {
    await target.waitForSelector("textarea, .home-starter, .composer-toolbar, .permission-selector", { timeout: 10000 });
  } catch {}
  await settle(target, 600);
  await record(target, states, "chat-home", target.url());

  const dropdowns = [
    { name: "chat-picker-executor", sel: ".composer-toolbar button.permission-selector[aria-label='CLI'], .composer-toolbar button.permission-selector[aria-label='CLI']", shot: "final3-picker.png", wait: ".permission-menu, .permission-menu-option" },
    { name: "chat-picker-model", sel: "button.grasp-model-picker, button[aria-label='Chọn model']", shot: null, wait: ".grasp-model-menu" },
    { name: "chat-picker-effort", sel: ".composer-toolbar button.permission-selector[aria-label='Cường độ suy luận']", shot: null, wait: ".permission-menu" },
    { name: "chat-picker-approval", sel: ".composer-toolbar button.permission-selector[aria-label='Cách xác nhận thao tác']", shot: null, wait: ".permission-menu" },
    { name: "chat-picker-attach", sel: ".composer-toolbar button[aria-haspopup='menu'][aria-label='Thêm'], .composer-toolbar button[aria-label='Thêm']", shot: null, wait: "[role='menu'], .menu" },
  ];

  // Fallback: click permission-selectors in order if aria-labels differ
  for (const d of dropdowns) {
    await dismissOverlays(target);
    await settle(target, 200);
    let clicked = await clickSel(target, d.sel);
    if (!clicked && d.name === "chat-picker-executor") {
      const loc = target.locator(".composer-toolbar button.permission-selector");
      const n = await loc.count();
      if (n > 0) {
        try { await loc.nth(0).click({ timeout: 1500 }); clicked = true; await settle(target, 400); } catch {}
      }
    }
    if (clicked && d.wait) {
      try { await target.waitForSelector(d.wait, { timeout: 3000 }); } catch {}
    }
    await settle(target, 400);
    if (d.shot) {
      await shot(target, d.shot);
    }
    await record(target, states, d.name, target.url());
    await dismissOverlays(target);
  }

  // Placeholder of the composer
  await record(target, states, "chat-composer-placeholder", target.url());

  const cards = target.locator(".home-starter");
  const n = await cards.count();
  log("home starters " + n);
  for (let i = 0; i < n; i++) {
    const card = cards.nth(i);
    let title = "";
    try { title = ((await card.locator("strong").innerText()) || "").trim(); } catch {}
    const slug = (title || "card" + i).normalize("NFD").replace(/[\u0300-\u036f]/g, "").toLowerCase()
      .replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || ("card" + i);
    try {
      await card.click({ timeout: 2000 });
      await settle(target, 700);
    } catch {}
    const shotName = `final3-template-${slug}.png`;
    await shot(target, shotName);
    await record(target, states, `chat-template-${slug}`, target.url(), { includeTextareas: true });
    // clear textarea so the next card starts clean
    try {
      const ta = target.locator("textarea[aria-label='Tin nhắn'], textarea[aria-label='消息'], .composer-column textarea").first();
      if (await ta.count()) await ta.fill("");
    } catch {}
    await dismissOverlays(target);
    await settle(target, 300);
  }
}

(async () => {
  log("sweep start " + passName);
  fs.mkdirSync(OUT_DIR, { recursive: true });
  const hashes = deriveHashes();
  log("derived hashes " + hashes.join(","));
  const states = [];
  let testIssueId = null;

  const created = await api("POST", `${BASE}/api/issues`, {
    id: "vi-sweep-" + Date.now(),
    projectId: PROJECT,
    title: "VI sweep test issue",
    description: "Temporary issue for the i18n sweep. Safe to delete.",
    status: "todo",
    priority: "medium",
    kind: "requirement",
  });
  testIssueId = created.json?.id || created.json?.issue?.id || null;
  if (!testIssueId && created.status >= 400) {
    const retry = await api("POST", `${BASE}/api/issues`, {
      projectId: PROJECT,
      title: "VI sweep test issue",
      description: "Temporary issue for the i18n sweep. Safe to delete.",
      status: "todo",
      priority: "medium",
    });
    testIssueId = retry.json?.id || retry.json?.issue?.id || null;
    created.status = retry.status;
    created.json = retry.json;
  }

  const browser = await chromium.launch({
    channel: "chrome",
    headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage", "--window-size=1440,960"],
  });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 960 },
    locale: "vi-VN",
  });
  await context.addCookies([{
    name: "agentflow_user",
    value: cookie,
    url: BASE + "/",
    httpOnly: true,
    sameSite: "Lax",
  }]);
  await context.addInitScript(() => {
    try { localStorage.setItem("loopforge.lang", "vi"); } catch {}
  });
  context.on("page", p => p.on("dialog", d => d.dismiss().catch(() => {})));

  const page = await context.newPage();
  page.on("dialog", d => d.dismiss().catch(() => {}));

  await page.goto(`${BASE}/login.html`, { waitUntil: "domcontentloaded", timeout: 30000 });
  await settle(page, 600);
  await record(page, states, "login", `${BASE}/login.html`);

  await page.goto(`${BASE}/?projectId=${PROJECT}&quickAccess=1`, { waitUntil: "domcontentloaded", timeout: 25000 });
  await settle(page, 900);
  await record(page, states, "onboarding-quickAccess", page.url());

  await page.goto(`${BASE}/chatbot/?projectId=${PROJECT}&quickAccess=1`, { waitUntil: "domcontentloaded", timeout: 25000 });
  await settle(page, 900);
  await hoverTitled(page);
  await record(page, states, "chatbot-quickAccess", page.url());

  await page.goto(`${BASE}/chatbot/preview.html`, { waitUntil: "domcontentloaded", timeout: 15000 });
  await settle(page, 400);
  await record(page, states, "chatbot-preview-shell", page.url());

  await exploreComposer(page, states);

  await page.goto(`${BASE}/?projectId=${PROJECT}`, { waitUntil: "domcontentloaded", timeout: 25000 });
  await settle(page, 700);

  for (const hash of hashes) {
    log("hash " + hash);
    await page.evaluate((h) => { location.hash = "#/" + h; }, hash);
    await settle(page, hash === "observability" ? 200 : 800);
    if (hash === "observability") {
      await exploreObservability(page, states);
    } else {
      await exploreView(page);
      await record(page, states, `view-${hash}`, `${BASE}/?projectId=${PROJECT}#/${hash}`);
    }
    await dismissOverlays(page);
  }

  // SOP / flow inspector (catalog prompts if visible)
  await page.evaluate(() => { location.hash = "#/sops"; });
  await settle(page, 800);
  for (const sel of [
    ".sop-card", ".playbook-card", "[data-workflow-id]", ".flow-card",
    "button:has-text('Mở')", "button:has-text('Sửa')", "button:has-text('编辑')",
    "button:has-text('打开')", "a:has-text('SOP')",
  ]) {
    const loc = page.locator(sel).first();
    try {
      if (await loc.isVisible({ timeout: 400 })) {
        await loc.click({ timeout: 1500 });
        await settle(page, 600);
        break;
      }
    } catch {}
  }
  await exploreView(page);
  await record(page, states, "sop-or-flow-opened", page.url());
  await dismissOverlays(page);

  const modalButtons = [
    { name: "modal-new-sop", sel: "#sopCreateOpen, button:has-text('Tạo SOP'), button:has-text('新建 SOP'), #newSOP, button:has-text('Tạo mẫu')" },
    { name: "modal-new-project", sel: "button:has-text('Dự án mới'), button:has-text('新建项目'), #projectCreateOpen, button:has-text('Tạo dự án')" },
    { name: "modal-add-mcp", sel: "#newCustomMcp, button:has-text('Thêm MCP'), button:has-text('添加 MCP')" },
    { name: "modal-new-agent", sel: "button:has-text('Tạo vai trò'), button:has-text('新建角色'), #agentCreateOpen" },
  ];
  for (const m of modalButtons) {
    await dismissOverlays(page);
    const loc = page.locator(m.sel).first();
    try {
      if (m.name.includes("sop")) await page.evaluate(() => { location.hash = "#/sops"; });
      if (m.name.includes("project")) await page.evaluate(() => { location.hash = "#/projects"; });
      if (m.name.includes("mcp")) await page.evaluate(() => { location.hash = "#/mcp"; });
      if (m.name.includes("agent")) await page.evaluate(() => { location.hash = "#/agents"; });
      await settle(page, 600);
      if (await loc.count()) {
        await loc.click({ timeout: 2000 });
        await settle(page, 800);
        await record(page, states, m.name, page.url());
      }
    } catch {}
    await dismissOverlays(page);
  }

  if (testIssueId) {
    await page.evaluate(() => { location.hash = "#/issues"; });
    await settle(page, 800);
    const row = page.locator(`text=VI sweep test issue`).first();
    try {
      if (await row.count()) {
        await row.click({ timeout: 2000 });
        await settle(page, 600);
        await clickAll(page, "#issueDetailTabs [role=tab], .issue-detail [role=tab], [data-issue-tab]", 10);
        await record(page, states, "issue-drawer-test", page.url());
      }
    } catch {}
    await dismissOverlays(page);
  }

  await exploreSettings(page, states);

  await page.evaluate(() => { location.hash = "#/projects"; });
  await settle(page, 800);
  const proj = page.locator("text=Dự án demo LoopForge, text=welcome, text=LoopForge").first();
  try {
    if (await proj.count()) {
      await proj.click({ timeout: 2000 });
      await settle(page, 600);
      await clickAll(page, "#projectAdminTabs button, [aria-label='项目配置分区'] button, [aria-label='Phân vùng cấu hình dự án'] button", 12);
      await record(page, states, "project-admin-welcome", page.url());
    }
  } catch {}
  await dismissOverlays(page);

  await page.evaluate(() => { location.hash = "#/agents"; });
  await settle(page, 800);
  try {
    const row = page.locator(".agent-card, tr, [data-agent-id]").first();
    if (await row.count()) {
      await row.click({ timeout: 1500 });
      await settle(page, 800);
      await record(page, states, "agent-detail", page.url());
    }
  } catch {}
  await dismissOverlays(page);

  // Final screenshots of usage + settings
  await page.evaluate(() => { location.hash = "#/observability"; });
  await waitObs(page);
  await clickSel(page, `[data-obs-tab="overview"]`);
  await clickSel(page, `[data-obs-scope="project"]`);
  await settle(page, 700);
  await shot(page, "final2-usage-mucdung-duan.png");
  await clickSel(page, `[data-obs-scope="user"]`);
  await settle(page, 600);
  await shot(page, "final2-usage-mucdung-canhan.png");
  await clickSel(page, `[data-obs-tab="delivery"]`);
  await clickSel(page, `[data-obs-scope="project"]`);
  await settle(page, 700);
  await shot(page, "final2-usage-bangiao-duan.png");
  await clickSel(page, `[data-obs-scope="user"]`);
  await settle(page, 600);
  await shot(page, "final2-usage-bangiao-canhan.png");
  await page.evaluate(() => { location.hash = "#/settings"; });
  await settle(page, 800);
  await shot(page, "final2-settings.png");
  await shot(page, "lf-settings-personal.png");
  await clickSel(page, "[data-credential-add], button:has-text('Thêm thông tin xác thực')");
  await settle(page, 600);
  await shot(page, "lf-settings-wizard.png");
  await page.keyboard.press("Escape").catch(() => {});
  await dismissOverlays(page);
  await page.evaluate(() => { location.hash = "#/platform"; });
  await settle(page, 800);
  await shot(page, "lf-settings-platform.png");
  await page.evaluate(() => { location.hash = "#/guide"; });
  await settle(page, 700);
  await shot(page, "lf-settings-access.png");

  await browser.close();

  let cleanup = { attempted: !!testIssueId, status: null };
  if (testIssueId) {
    const del = await api("DELETE", `${BASE}/api/issues/${encodeURIComponent(testIssueId)}`);
    cleanup.status = del.status;
    if (del.status >= 400) {
      const close = await api("POST", `${BASE}/api/issues/${encodeURIComponent(testIssueId)}/close`);
      cleanup.closeStatus = close.status;
      const del2 = await api("DELETE", `${BASE}/api/issues/${encodeURIComponent(testIssueId)}`);
      cleanup.status = del2.status;
    }
  }

  const totalCJK = states.reduce((s, st) => s + (st.total || 0), 0);
  const totalSoup = states.reduce((s, st) => s + (st.soupCount || 0), 0);
  const dirtyCJK = states.filter(st => (st.total || 0) > 0);
  const dirtySoup = states.filter(st => (st.soupCount || 0) > 0);
  const uniqueSamples = [];
  const seen = new Set();
  for (const st of states) {
    for (const h of st.hits || []) {
      const key = "cjk|" + (h.type || "") + "|" + (h.sample || "");
      if (seen.has(key)) continue;
      seen.add(key);
      uniqueSamples.push({ kind: "cjk", ...h, state: st.name });
    }
    for (const h of st.soups || []) {
      const key = "soup|" + (h.type || "") + "|" + (h.sample || "");
      if (seen.has(key)) continue;
      seen.add(key);
      uniqueSamples.push({ kind: "soup", ...h, state: st.name });
    }
  }

  const report = {
    generatedAt: new Date().toISOString(),
    timezone: "Asia/Saigon",
    pass: passName,
    derivedHashes: hashes,
    predicate: "Every crawled state has CJK = 0 and soup = 0 in visible text and listed attributes, excluding SCRIPT/STYLE/CODE/PRE, monaco, xterm, and log lines.",
    testIssue: { id: testIssueId, createStatus: created.status, cleanup },
    stateCount: states.length,
    statesWithCJK: dirtyCJK.length,
    statesWithSoup: dirtySoup.length,
    totalCJKAcrossStates: totalCJK,
    totalSoupAcrossStates: totalSoup,
    uniqueHitCount: uniqueSamples.length,
    uniqueHits: uniqueSamples,
    states,
  };
  fs.writeFileSync(REPORT, JSON.stringify(report, null, 2));
  fs.writeFileSync(path.join(OUT_DIR, "sweep-report-final5.json"), JSON.stringify(report, null, 2));
  const summary = {
    pass: passName,
    states: states.length,
    dirtyCJK: dirtyCJK.length,
    dirtySoup: dirtySoup.length,
    totalCJK,
    totalSoup,
    uniqueHits: uniqueSamples.length,
    hashes: hashes,
    report: REPORT,
    dirtyCJKNames: dirtyCJK.map(d => `${d.name}:${d.total}`),
    dirtySoupNames: dirtySoup.map(d => `${d.name}:${d.soupCount}`),
    samples: uniqueSamples.slice(0, 60).map(h => `[${h.state} ${h.kind}/${h.type}/${h.reason || ""}] ${h.sample}`),
  };
  console.log(JSON.stringify(summary, null, 2));
  if (dirtyCJK.length || dirtySoup.length) process.exitCode = 2;
})().catch((err) => {
  console.error("SWEEP_FAILED", err && err.stack || err);
  process.exit(1);
});
