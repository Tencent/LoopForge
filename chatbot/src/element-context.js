// Element context: what the user points at in a native preview, described so
// the Agent can find it in code (component and source file when the dev build
// exposes them) and in its own browser (a unique selector).

const styleKeys = [
  "display",
  "position",
  "width",
  "height",
  "margin",
  "padding",
  "gap",
  "flex-direction",
  "justify-content",
  "align-items",
  "color",
  "background-color",
  "font-size",
  "font-weight",
  "line-height",
  "border",
  "border-radius",
  "box-shadow",
  "opacity",
];

// Values that only restate the default are left out.
const boringStyle = /^(none|normal|auto|0px|0px none.*|rgba\(0, 0, 0, 0\))$/;
const styleDefaults = { position: "static", opacity: "1", "font-weight": "400" };
const layoutKeys = new Set(["gap", "flex-direction", "justify-content", "align-items"]);

const agentRefAttr = / data-agentflow-ref="[^"]*"/g;
const textLimit = 200;
const htmlLimit = 3000;

const clip = (value, limit) => {
  const text = String(value || "").replace(/\s+/g, " ").trim();
  return text.length > limit ? `${text.slice(0, limit)}…` : text;
};

const escapeIdent = (value) =>
  typeof CSS !== "undefined" && CSS.escape ? CSS.escape(value) : String(value).replace(/[^a-zA-Z0-9_-]/g, "\\$&");

// Hashed or utility classes say little about where the element is; skip
// classes that look generated (css-1x2y3z, sc-abc123) and keep at most two.
const meaningfulClasses = (el) =>
  [...(el.classList || [])]
    .filter((c) => !/^(css|sc|jsx|svelte|emotion)-|^[a-z]{1,3}[-_]?(?=[0-9a-z]*[0-9])[0-9a-z]{5,}$/i.test(c) && c.length <= 40)
    .slice(0, 2);

function unique(doc, selector, el) {
  try {
    const found = doc.querySelectorAll(selector);
    return found.length === 1 && found[0] === el;
  } catch {
    return false;
  }
}

function step(el) {
  const tag = el.tagName.toLowerCase();
  const testId = el.getAttribute("data-testid") || el.getAttribute("data-test-id");
  if (testId) return `${tag}[data-testid="${testId.replace(/"/g, '\\"')}"]`;
  const classes = meaningfulClasses(el).map((c) => `.${escapeIdent(c)}`).join("");
  return `${tag}${classes}`;
}

function nth(el, base) {
  const parent = el.parentElement;
  if (!parent) return base;
  const same = [...parent.children].filter((c) => c.tagName === el.tagName);
  return same.length > 1 ? `${base}:nth-of-type(${same.indexOf(el) + 1})` : base;
}

// elementSelector returns a selector that matches only el in its document.
export function elementSelector(el) {
  const doc = el.ownerDocument;
  if (el.id && unique(doc, `#${escapeIdent(el.id)}`, el)) return `#${escapeIdent(el.id)}`;
  const parts = [];
  let node = el;
  while (node && node.nodeType === 1 && node !== doc.documentElement) {
    if (node !== el && node.id && unique(doc, `#${escapeIdent(node.id)}`, node)) {
      parts.unshift(`#${escapeIdent(node.id)}`);
      break;
    }
    parts.unshift(step(node));
    const selector = parts.join(" > ");
    if (unique(doc, selector, el)) return selector;
    parts[0] = nth(node, parts[0]);
    if (unique(doc, parts.join(" > "), el)) return parts.join(" > ");
    node = node.parentElement;
  }
  return parts.join(" > ");
}

// elementLabel is the short name shown on the composer chip.
export function elementLabel(el) {
  const tag = el.tagName.toLowerCase();
  const id = el.id ? `#${el.id}` : "";
  const cls = !id && meaningfulClasses(el)[0] ? `.${meaningfulClasses(el)[0]}` : "";
  const text = clip(el.innerText ?? el.textContent, 16);
  return `${tag}${id}${cls}${text ? ` 「${text}」` : ""}`;
}

function sourceOf(file, line, column) {
  if (!file) return "";
  return `${file}${line ? `:${line}` : ""}${line && column ? `:${column}` : ""}`;
}

function reactComponent(el) {
  const key = Object.keys(el).find((k) => k.startsWith("__reactFiber$") || k.startsWith("__reactInternalInstance$"));
  let fiber = key ? el[key] : null;
  let source = fiber?._debugSource || null;
  for (let i = 0; fiber && i < 40; i++, fiber = fiber.return) {
    source = source || fiber._debugSource || null;
    const type = fiber.type;
    if (typeof type === "function" || (type && typeof type === "object" && (type.render || type.type))) {
      const name = type.displayName || type.name || type.render?.displayName || type.render?.name || type.type?.name;
      if (name)
        return {
          framework: "React",
          component: name,
          source: sourceOf(source?.fileName, source?.lineNumber, source?.columnNumber),
        };
    }
  }
  return null;
}

function vueComponent(el) {
  const instance = el.__vueParentComponent;
  if (instance?.type) {
    const type = instance.type;
    const file = type.__file || "";
    const name = type.name || type.__name || (file ? file.split("/").pop().replace(/\.vue$/, "") : "");
    if (name || file) return { framework: "Vue", component: name, source: file };
  }
  const vm = el.__vue__;
  if (vm?.$options) {
    const file = vm.$options.__file || "";
    return { framework: "Vue", component: vm.$options.name || vm.$options._componentTag || "", source: file };
  }
  return null;
}

function svelteComponent(el) {
  const loc = el.__svelte_meta?.loc;
  if (!loc?.file) return null;
  return {
    framework: "Svelte",
    component: loc.file.split("/").pop().replace(/\.svelte$/, ""),
    source: sourceOf(loc.file, loc.line != null ? loc.line + 1 : 0, loc.column != null ? loc.column + 1 : 0),
  };
}

// componentOf finds the nearest framework component a dev build exposes on
// the element or its ancestors.
export function componentOf(el) {
  for (let node = el, depth = 0; node && node.nodeType === 1 && depth < 25; node = node.parentElement, depth++) {
    const found = reactComponent(node) || vueComponent(node) || svelteComponent(node);
    if (found) return found;
  }
  return null;
}

// describeElement collects what the Agent needs about el. url is the page
// address as the sandbox sees it (http://localhost:PORT/… or file://…).
export function describeElement(el, url) {
  const win = el.ownerDocument.defaultView;
  const rect = el.getBoundingClientRect();
  const computed = win.getComputedStyle(el);
  const styles = {};
  const layout = /flex|grid/.test(computed.getPropertyValue("display"));
  for (const key of styleKeys) {
    const value = computed.getPropertyValue(key);
    if (!layout && layoutKeys.has(key)) continue;
    if (!value || boringStyle.test(value) || styleDefaults[key] === value) continue;
    styles[key] = value;
  }
  // The Agent's page snapshots tag elements with data-agentflow-ref; they mean
  // nothing outside that snapshot.
  const html = (el.outerHTML || "").replace(agentRefAttr, "");
  const component = componentOf(el);
  return {
    url,
    title: clip(el.ownerDocument.title, 120),
    selector: elementSelector(el),
    tag: el.tagName.toLowerCase(),
    text: clip(el.innerText ?? el.textContent, textLimit),
    html: html.length > htmlLimit ? `${html.slice(0, htmlLimit)}…` : html,
    rect: {
      x: Math.round(rect.left),
      y: Math.round(rect.top),
      width: Math.round(rect.width),
      height: Math.round(rect.height),
    },
    viewport: { width: win.innerWidth, height: win.innerHeight },
    styles,
    ...(component?.component ? { component: component.component, framework: component.framework } : {}),
    ...(component?.source ? { source: component.source } : {}),
  };
}

let sequence = 0;

// elementReference wraps a description as a composer reference.
export function elementReference(el, url) {
  sequence += 1;
  return {
    type: "element",
    id: `el-${Date.now().toString(36)}-${sequence}`,
    name: elementLabel(el),
    element: describeElement(el, url),
  };
}

// inspectionReference wraps what the Agent browser reported for a picked
// element (internal/runtime/browser_inspect.go) as a composer reference.
export function inspectionReference(inspection) {
  sequence += 1;
  return {
    type: "element",
    id: `el-${Date.now().toString(36)}-${sequence}`,
    name: inspection.label || inspection.element?.tag || "元素",
    element: inspection.element,
  };
}

// elementTitle is the hover text of an element chip.
export function elementTitle(ref) {
  const e = ref?.element || {};
  return [
    e.component ? `${e.component}${e.source ? ` · ${e.source}` : ""}` : e.source,
    e.selector,
    e.url,
  ]
    .filter(Boolean)
    .join("\n");
}
