(function () {
  var base = window.__AGENTFLOW_PREVIEW_BACKEND__;
  if (!base) return;
  var dest;
  try {
    dest = new URL(base);
  } catch (e) {
    return;
  }
  if (dest.protocol !== "http:" && dest.protocol !== "https:") return;
  var prefix = __AF_PREFIX__;
  function isPreviewAsset(u) {
    if (u.origin !== location.origin) return false;
    if (!prefix) return true;
    return u.pathname === prefix || u.pathname.indexOf(prefix + "/") === 0;
  }
  function retarget(href) {
    if (href == null || href === "") return href;
    var u;
    try {
      u = new URL(String(href), location.href);
    } catch (e) {
      return href;
    }
    if (u.protocol !== "http:" && u.protocol !== "https:" && u.protocol !== "ws:" && u.protocol !== "wss:") {
      return href;
    }
    if (isPreviewAsset(u)) return href;
    var out = new URL(u.href);
    if (u.protocol === "ws:" || u.protocol === "wss:") {
      out.protocol = dest.protocol === "https:" ? "wss:" : "ws:";
    } else {
      out.protocol = dest.protocol;
    }
    out.host = dest.host;
    return out.href;
  }
  var ofetch = window.fetch;
  if (ofetch) {
    window.fetch = function (input, init) {
      try {
        if (typeof Request !== "undefined" && Request.prototype && input instanceof Request) {
          var next = retarget(input.url);
          if (next !== input.url) input = new Request(next, input);
        } else if (input != null) {
          input = retarget(input);
        }
      } catch (e) {}
      return ofetch.call(this, input, init);
    };
  }
  var oxhr = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function (m, u) {
    if (arguments.length > 1 && u != null) {
      try {
        arguments[1] = retarget(u);
      } catch (e) {}
    }
    return oxhr.apply(this, arguments);
  };
  if (navigator.sendBeacon) {
    var obeacon = navigator.sendBeacon.bind(navigator);
    navigator.sendBeacon = function (u, d) {
      return obeacon(retarget(u), d);
    };
  }
  if (window.EventSource) {
    var ES = window.EventSource;
    window.EventSource = function (u, o) {
      return new ES(retarget(u), o);
    };
    window.EventSource.prototype = ES.prototype;
    window.EventSource.CONNECTING = ES.CONNECTING;
    window.EventSource.OPEN = ES.OPEN;
    window.EventSource.CLOSED = ES.CLOSED;
  }
  if (window.WebSocket) {
    var WS = window.WebSocket;
    window.WebSocket = function (u, p) {
      return p === undefined ? new WS(retarget(u)) : new WS(retarget(u), p);
    };
    window.WebSocket.prototype = WS.prototype;
    window.WebSocket.CONNECTING = WS.CONNECTING;
    window.WebSocket.OPEN = WS.OPEN;
    window.WebSocket.CLOSING = WS.CLOSING;
    window.WebSocket.CLOSED = WS.CLOSED;
  }
})();
