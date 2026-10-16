(function () {
  var root = __AF_PREFIX__;
  var routed = __AF_ROUTED__;
  var h = window.history;
  var loc = window.location;
  if (!root || !h || typeof h.pushState !== "function" || typeof h.replaceState !== "function") return;
  var push = h.pushState;
  var replace = h.replaceState;
  var listen = window.addEventListener;
  var family = root.slice(0, root.lastIndexOf("/") + 1);
  var pending = false;
  var booting = false;
  function inside(path) {
    return path === root || path.indexOf(root + "/") === 0;
  }
  function leaks(args) {
    if (args.length < 3 || args[2] == null) return false;
    try {
      var u = new URL(String(args[2]), document.baseURI);
      return u.origin === loc.origin && !inside(u.pathname);
    } catch (e) {
      return false;
    }
  }
  function strip() {
    if (!inside(loc.pathname)) return;
    replace.call(h, h.state, "", (loc.pathname.slice(root.length) || "/") + loc.search + loc.hash);
  }
  function restore() {
    pending = false;
    booting = false;
    if (!inside(loc.pathname)) {
      replace.call(h, h.state, "", root + loc.pathname + loc.search + loc.hash);
    }
  }
  function later() {
    if (pending) return;
    pending = true;
    // A task, not a microtask: browsers run microtasks between event
    // listeners, and every popstate listener must see the unprefixed path.
    setTimeout(restore, 0);
  }
  // Same rule as the gateway's stray-navigation redirect: a page on this
  // origin outside every preview path belongs to this preview.
  function stray(u) {
    return u.protocol === loc.protocol && u.host === loc.host && !inside(u.pathname) && u.pathname.indexOf(family) !== 0;
  }
  // The redirect needs a Referer under the preview path, which the address
  // lacks while unprefixed and noreferrer links never send.
  function leave(u, replacing) {
    restore();
    var to = root + u.pathname + u.search + u.hash;
    if (replacing) loc.replace(to);
    else loc.assign(to);
  }
  h.pushState = function () {
    restore();
    var out = push.apply(this, arguments);
    if (leaks(arguments)) later();
    return out;
  };
  h.replaceState = function () {
    var out = replace.apply(this, arguments);
    if (booting || leaks(arguments)) later();
    return out;
  };
  window.addEventListener("popstate", function () {
    strip();
    later();
  }, true);
  window.addEventListener("click", function (e) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    var a = e.target && e.target.closest ? e.target.closest("a[href]") : null;
    if (!a || a.hasAttribute("download")) return;
    var target = a.getAttribute("target");
    if (target && target !== "_self") return;
    var u;
    try {
      u = new URL(a.getAttribute("href"), document.baseURI);
    } catch (err) {
      return;
    }
    if (!stray(u)) return;
    e.preventDefault();
    leave(u, false);
  });
  var nav = window.navigation;
  if (nav && typeof nav.addEventListener === "function") {
    nav.addEventListener("navigate", function (e) {
      var d = e.destination;
      if (!e.cancelable || d.sameDocument || e.formData || e.downloadRequest != null || e.navigationType === "traverse") return;
      var u;
      try {
        u = new URL(d.url);
      } catch (err) {
        return;
      }
      if (!stray(u)) return;
      e.preventDefault();
      leave(u, e.navigationType !== "push");
    });
  }
  if (routed && inside(loc.pathname)) {
    strip();
    booting = true;
    // Routers may start well after load (dynamic imports, awaited requests).
    // One that resumes its own history state never writes, but every router
    // listens for popstate right after reading the address; either ends the
    // boot. The timer only bounds bundles whose router never starts.
    window.addEventListener = function (type) {
      if (booting && type === "popstate") later();
      return listen.apply(this == null ? window : this, arguments);
    };
    listen.call(window, "load", function () {
      setTimeout(function () {
        if (booting) restore();
      }, 10000);
    });
  }
})();
