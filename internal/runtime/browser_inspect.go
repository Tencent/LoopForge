package runtime

import (
	"context"
	"errors"
	"fmt"

	"agentflow/internal/domain"
)

// Element inspection for the panel's pick mode on the Agent browser
// screencast: the page reports the element under a point, its box for the
// outline and, on a pick, the description the composer sends to the Agent.
// The description follows chatbot/src/element-context.js, which does the same
// for native previews in the user's own browser; keep the two in step.

const inspectScript = `(function (fx, fy, detail) {
  var doc = document, x = fx * innerWidth, y = fy * innerHeight, ox = 0, oy = 0, el = null;
  for (var depth = 0; depth < 8; depth++) {
    var hit = doc.elementFromPoint(x, y);
    while (hit && hit.shadowRoot) {
      var inner = hit.shadowRoot.elementFromPoint(x, y);
      if (!inner || inner === hit) break;
      hit = inner;
    }
    if (!hit) break;
    el = hit;
    if (hit.tagName !== "IFRAME" && hit.tagName !== "FRAME") break;
    var child = null;
    try { child = hit.contentDocument; } catch (e) {}
    if (!child || !child.documentElement) break;
    var box = hit.getBoundingClientRect();
    var dx = box.left + hit.clientLeft, dy = box.top + hit.clientTop;
    x -= dx; y -= dy; ox += dx; oy += dy; doc = child;
  }
  if (!el || el === document.documentElement) return null;
  var clip = function (value, limit) {
    var text = String(value || "").replace(/\s+/g, " ").trim();
    return text.length > limit ? text.slice(0, limit) + "…" : text;
  };
  var esc = function (v) { return window.CSS && CSS.escape ? CSS.escape(v) : String(v).replace(/[^a-zA-Z0-9_-]/g, "\\$&"); };
  var classes = function (node) {
    return Array.prototype.filter.call(node.classList || [], function (c) {
      return !/^(css|sc|jsx|svelte|emotion)-|^[a-z]{1,3}[-_]?(?=[0-9a-z]*[0-9])[0-9a-z]{5,}$/i.test(c) && c.length <= 40;
    }).slice(0, 2);
  };
  var label = function (node) {
    var tag = node.tagName.toLowerCase(), id = node.id ? "#" + node.id : "";
    var cls = !id && classes(node)[0] ? "." + classes(node)[0] : "";
    var text = clip(node.innerText != null ? node.innerText : node.textContent, 16);
    return tag + id + cls + (text ? " 「" + text + "」" : "");
  };
  var rect = el.getBoundingClientRect();
  var out = {
    label: label(el),
    rect: { x: Math.round(rect.left + ox), y: Math.round(rect.top + oy), width: Math.round(rect.width), height: Math.round(rect.height) },
    viewport: { width: innerWidth, height: innerHeight }
  };
  if (!detail) return out;
  var owner = el.ownerDocument;
  var unique = function (selector) {
    try { var found = owner.querySelectorAll(selector); return found.length === 1 && found[0] === el; } catch (e) { return false; }
  };
  var step = function (node) {
    var tag = node.tagName.toLowerCase();
    var testId = node.getAttribute("data-testid") || node.getAttribute("data-test-id");
    if (testId) return tag + '[data-testid="' + testId.replace(/"/g, '\\"') + '"]';
    return tag + classes(node).map(function (c) { return "." + esc(c); }).join("");
  };
  var nth = function (node, base) {
    var parent = node.parentElement;
    if (!parent) return base;
    var same = Array.prototype.filter.call(parent.children, function (c) { return c.tagName === node.tagName; });
    return same.length > 1 ? base + ":nth-of-type(" + (same.indexOf(node) + 1) + ")" : base;
  };
  var selector = function () {
    if (el.id && unique("#" + esc(el.id))) return "#" + esc(el.id);
    var parts = [], node = el;
    while (node && node.nodeType === 1 && node !== owner.documentElement) {
      if (node !== el && node.id && owner.querySelectorAll("#" + esc(node.id)).length === 1) { parts.unshift("#" + esc(node.id)); break; }
      parts.unshift(step(node));
      if (unique(parts.join(" > "))) return parts.join(" > ");
      parts[0] = nth(node, parts[0]);
      if (unique(parts.join(" > "))) return parts.join(" > ");
      node = node.parentElement;
    }
    return parts.join(" > ");
  };
  var source = function (file, line, column) {
    return file ? file + (line ? ":" + line : "") + (line && column ? ":" + column : "") : "";
  };
  var component = function () {
    for (var node = el, i = 0; node && node.nodeType === 1 && i < 25; node = node.parentElement, i++) {
      var key = Object.keys(node).find(function (k) { return k.indexOf("__reactFiber$") === 0 || k.indexOf("__reactInternalInstance$") === 0; });
      if (key) {
        var fiber = node[key], src = fiber && fiber._debugSource;
        for (var j = 0; fiber && j < 40; j++, fiber = fiber.return) {
          src = src || fiber._debugSource;
          var t = fiber.type;
          if (typeof t === "function" || (t && typeof t === "object" && (t.render || t.type))) {
            var name = t.displayName || t.name || (t.render && (t.render.displayName || t.render.name)) || (t.type && t.type.name);
            if (name) return { framework: "React", component: name, source: src ? source(src.fileName, src.lineNumber, src.columnNumber) : "" };
          }
        }
      }
      var vue = node.__vueParentComponent;
      if (vue && vue.type) {
        var file = vue.type.__file || "";
        return { framework: "Vue", component: vue.type.name || vue.type.__name || file.split("/").pop().replace(/\.vue$/, ""), source: file };
      }
      if (node.__vue__ && node.__vue__.$options) {
        var o = node.__vue__.$options;
        return { framework: "Vue", component: o.name || o._componentTag || "", source: o.__file || "" };
      }
      var loc = node.__svelte_meta && node.__svelte_meta.loc;
      if (loc && loc.file) {
        return { framework: "Svelte", component: loc.file.split("/").pop().replace(/\.svelte$/, ""), source: source(loc.file, loc.line != null ? loc.line + 1 : 0, loc.column != null ? loc.column + 1 : 0) };
      }
    }
    return null;
  };
  var computed = owner.defaultView.getComputedStyle(el), styles = {};
  var layout = /flex|grid/.test(computed.getPropertyValue("display"));
  var defaults = { position: "static", opacity: "1", "font-weight": "400" };
  ["display", "position", "width", "height", "margin", "padding", "gap", "flex-direction", "justify-content", "align-items",
   "color", "background-color", "font-size", "font-weight", "line-height", "border", "border-radius", "box-shadow", "opacity"].forEach(function (k) {
    var v = computed.getPropertyValue(k);
    if (!layout && ["gap", "flex-direction", "justify-content", "align-items"].indexOf(k) >= 0) return;
    if (!v || /^(none|normal|auto|0px|0px none.*|rgba\(0, 0, 0, 0\))$/.test(v) || defaults[k] === v) return;
    styles[k] = v;
  });
  // Snapshot refs (data-agentflow-ref) mean nothing outside their snapshot.
  var html = (el.outerHTML || "").replace(/ data-agentflow-ref="[^"]*"/g, ""), found = component();
  out.element = {
    url: location.href,
    title: clip(document.title, 120),
    selector: selector(),
    tag: el.tagName.toLowerCase(),
    text: clip(el.innerText != null ? el.innerText : el.textContent, 200),
    html: html.length > 3000 ? html.slice(0, 3000) + "…" : html,
    rect: out.rect,
    viewport: out.viewport,
    styles: styles
  };
  if (found && found.component) { out.element.component = found.component; out.element.framework = found.framework; }
  if (found && found.source) out.element.source = found.source;
  return out;
})`

// inspect reports the element at point (a fraction of the viewport).
func (v *browserView) inspect(ctx context.Context, point *domain.BrowserPoint, detail bool) (*domain.BrowserInspection, error) {
	if point == nil {
		return nil, errors.New("缺少检查位置")
	}
	var out *domain.BrowserInspection
	expression := fmt.Sprintf("%s(%g, %g, %t)", inspectScript, clampUnit(point.X), clampUnit(point.Y), detail)
	if err := v.evaluate(ctx, expression, &out); err != nil {
		return nil, err
	}
	return out, nil
}
