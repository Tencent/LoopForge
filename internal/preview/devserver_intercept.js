(function () {
  // Dev server pages run under a path prefix on the platform origin. This
  // keeps their requests, sockets and cookies inside that prefix: root paths
  // and http://localhost:PORT addresses are moved under it, and cookies are
  // namespaced so the page neither sees nor sends platform cookies.
  var root = __AF_PREFIX__;
  var port = String(__AF_PORT__);
  var loc = window.location;
  if (window.__AGENTFLOW_DEV_SERVER__) return;
  window.__AGENTFLOW_DEV_SERVER__ = { prefix: root, port: Number(port) };
  var localHosts = { localhost: 1, "127.0.0.1": 1, "0.0.0.0": 1, "[::1]": 1 };
  function inside(path) {
    return path === root || path.indexOf(root + "/") === 0;
  }
  function fix(href) {
    if (href == null || href === "") return href;
    var u;
    try {
      u = new URL(String(href), loc.href);
    } catch (e) {
      return href;
    }
    var socket = u.protocol === "ws:" || u.protocol === "wss:";
    if (!socket && u.protocol !== "http:" && u.protocol !== "https:") return href;
    var changed = false;
    if (localHosts[u.hostname] && (u.port || (socket || u.protocol === "http:" ? "80" : "443")) === port) {
      u = new URL(u.pathname + u.search + u.hash, loc.href);
      if (socket) u.protocol = loc.protocol === "https:" ? "wss:" : "ws:";
      changed = true;
    } else if (socket && u.host === loc.host) {
      changed = false;
    } else if (u.origin !== loc.origin) {
      return href;
    }
    if (!inside(u.pathname)) {
      u.pathname = root + u.pathname;
      changed = true;
    }
    return changed ? u.href : href;
  }
  var ofetch = window.fetch;
  if (ofetch) {
    window.fetch = function (input, init) {
      try {
        if (typeof Request !== "undefined" && input instanceof Request) {
          var next = fix(input.url);
          if (next !== input.url) input = new Request(next, input);
        } else if (input != null) {
          input = fix(input instanceof URL ? input.href : input);
        }
      } catch (e) {}
      return ofetch.call(this, input, init);
    };
  }
  var oopen = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function (m, u) {
    if (arguments.length > 1 && u != null) {
      try {
        arguments[1] = fix(u instanceof URL ? u.href : u);
      } catch (e) {}
    }
    return oopen.apply(this, arguments);
  };
  if (navigator.sendBeacon) {
    var obeacon = navigator.sendBeacon.bind(navigator);
    navigator.sendBeacon = function (u, d) {
      return obeacon(fix(u), d);
    };
  }
  if (window.EventSource) {
    var ES = window.EventSource;
    var PatchedES = function (u, o) {
      return new ES(fix(u), o);
    };
    PatchedES.prototype = ES.prototype;
    PatchedES.CONNECTING = ES.CONNECTING;
    PatchedES.OPEN = ES.OPEN;
    PatchedES.CLOSED = ES.CLOSED;
    window.EventSource = PatchedES;
  }
  // Live reload sockets cannot cross the platform yet. A socket that fails
  // makes Vite poll the (reachable) dev server and reload the page forever,
  // so HMR clients get one that simply never connects; the page works and
  // shows edits on refresh.
  function hmrSocket(u, protocols) {
    var list = protocols == null ? [] : [].concat(protocols);
    for (var i = 0; i < list.length; i++) if (/^vite-(hmr|ping)$/.test(String(list[i]))) return true;
    try {
      return /\/_next\/webpack-hmr$/.test(new URL(u).pathname);
    } catch (e) {
      return false;
    }
  }
  if (window.WebSocket) {
    var WS = window.WebSocket;
    var Idle = function (u) {
      var t = typeof EventTarget === "function" ? new EventTarget() : document.createDocumentFragment();
      t.url = String(u);
      t.readyState = 0;
      t.protocol = "";
      t.extensions = "";
      t.bufferedAmount = 0;
      t.binaryType = "blob";
      t.onopen = t.onmessage = t.onerror = t.onclose = null;
      t.send = function () {};
      t.close = function () {
        t.readyState = 3;
      };
      t.CONNECTING = 0;
      t.OPEN = 1;
      t.CLOSING = 2;
      t.CLOSED = 3;
      return t;
    };
    var PatchedWS = function (u, p) {
      var next = fix(u instanceof URL ? u.href : u);
      if (hmrSocket(next, p)) return Idle(next);
      return p === undefined ? new WS(next) : new WS(next, p);
    };
    PatchedWS.prototype = WS.prototype;
    PatchedWS.CONNECTING = WS.CONNECTING;
    PatchedWS.OPEN = WS.OPEN;
    PatchedWS.CLOSING = WS.CLOSING;
    PatchedWS.CLOSED = WS.CLOSED;
    window.WebSocket = PatchedWS;
  }
  // Cookies are namespaced by run and port and kept under the preview
  // prefix, like the gateway does for Set-Cookie: a page writing
  // "sid=1; Path=/" must not leave it at the platform root, where every
  // other preview (and the platform) would see it.
  var cookiePrefix = __AF_COOKIE__;
  function scopeCookie(v) {
    var parts = String(v).split(";");
    var out = [cookiePrefix + parts[0].replace(/^\s+/, "")];
    for (var i = 1; i < parts.length; i++) {
      var attr = parts[i].replace(/^\s+|\s+$/g, "");
      var eq = attr.indexOf("=");
      var name = (eq < 0 ? attr : attr.slice(0, eq)).toLowerCase();
      if (!name || name === "domain") continue;
      if (name === "path") {
        var path = eq < 0 ? "" : attr.slice(eq + 1).replace(/^\s+/, "");
        if (path.charAt(0) !== "/") path = "/";
        if (!inside(path)) path = root + path;
        attr = "Path=" + path;
      }
      out.push(attr);
    }
    return out.join("; ");
  }
  var cookie = Object.getOwnPropertyDescriptor(Document.prototype, "cookie");
  if (cookie && cookie.configurable !== false && cookie.get && cookie.set) {
    Object.defineProperty(document, "cookie", {
      configurable: true,
      get: function () {
        return String(cookie.get.call(document))
          .split(/;\s*/)
          .filter(function (c) {
            return c.indexOf(cookiePrefix) === 0;
          })
          .map(function (c) {
            return c.slice(cookiePrefix.length);
          })
          .join("; ");
      },
      set: function (v) {
        cookie.set.call(document, scopeCookie(v));
      },
    });
  }
  // While the history shim hides the prefix from a booting router, root
  // paths resolve against the bare origin and requests carry no usable
  // Referer, so resource URLs set from script are moved here. Anchors are
  // left alone: routers read them, and the shim routes their clicks.
  var resourceAttrs = { src: 1, href: 1, poster: 1, data: 1 };
  function resource(el, name) {
    var tag = el && el.tagName;
    if (!tag || !resourceAttrs[name]) return false;
    if (name === "href") return tag === "LINK" || tag === "use" || tag === "image";
    return tag !== "A" && tag !== "AREA" && tag !== "FORM" && tag !== "BASE";
  }
  var osetAttribute = Element.prototype.setAttribute;
  Element.prototype.setAttribute = function (name, value) {
    try {
      var lower = String(name).toLowerCase();
      if (resource(this, lower) && typeof value === "string" && value.charAt(0) === "/") value = fix(value);
    } catch (e) {}
    return osetAttribute.call(this, name, value);
  };
  function patchProperty(Ctor, prop) {
    var proto = Ctor && Ctor.prototype;
    var desc = proto && Object.getOwnPropertyDescriptor(proto, prop);
    if (!desc || !desc.set || desc.configurable === false) return;
    Object.defineProperty(proto, prop, {
      configurable: true,
      enumerable: desc.enumerable,
      get: desc.get,
      set: function (value) {
        try {
          if (typeof value === "string" || value instanceof URL) value = fix(String(value));
        } catch (e) {}
        return desc.set.call(this, value);
      },
    });
  }
  [
    [window.HTMLImageElement, "src"],
    [window.HTMLScriptElement, "src"],
    [window.HTMLLinkElement, "href"],
    [window.HTMLSourceElement, "src"],
    [window.HTMLMediaElement, "src"],
    [window.HTMLVideoElement, "poster"],
    [window.HTMLIFrameElement, "src"],
    [window.HTMLTrackElement, "src"],
    [window.HTMLEmbedElement, "src"],
    [window.HTMLObjectElement, "data"],
    [window.HTMLInputElement, "src"],
  ].forEach(function (pair) {
    patchProperty(pair[0], pair[1]);
  });
  var oopenWindow = window.open;
  window.open = function (u) {
    if (arguments.length > 0 && u != null && u !== "") {
      try {
        arguments[0] = fix(u instanceof URL ? u.href : u);
      } catch (e) {}
    }
    return oopenWindow.apply(this, arguments);
  };
})();
