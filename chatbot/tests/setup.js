import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

if (typeof window !== "undefined") {
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: (query) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent() {
        return true;
      },
    }),
  });
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  globalThis.IntersectionObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  globalThis.requestAnimationFrame ??= (callback) =>
    setTimeout(() => callback(performance.now()), 0);
  globalThis.cancelAnimationFrame ??= clearTimeout;
  URL.createObjectURL ??= () => "blob:test";
  URL.revokeObjectURL ??= () => {};
  Element.prototype.animate ??= () => ({
    cancel() {},
    finish() {},
    addEventListener() {},
  });
}

const activeTimers = new Set();
const nativeSetTimeout = globalThis.setTimeout.bind(globalThis);
const nativeClearTimeout = globalThis.clearTimeout.bind(globalThis);
if (typeof window !== "undefined") {
  globalThis.setTimeout = window.setTimeout = (fn, ms, ...args) => {
    const id = nativeSetTimeout((...a) => {
      activeTimers.delete(id);
      if (typeof document !== "undefined" && typeof Element !== "undefined") {
        fn(...a);
      }
    }, ms, ...args);
    activeTimers.add(id);
    return id;
  };
  globalThis.clearTimeout = window.clearTimeout = (id) => {
    activeTimers.delete(id);
    return nativeClearTimeout(id);
  };
  const nativeAnchorClick = HTMLAnchorElement.prototype.click;
  HTMLAnchorElement.prototype.click = function (...args) {
    if (!this.isConnected || !this.getAttribute("href")?.startsWith("#")) {
      return;
    }
    return nativeAnchorClick.apply(this, args);
  };
  document.addEventListener(
    "click",
    (event) => {
      const anchor = event.target?.closest?.("a[href]");
      if (anchor && !anchor.getAttribute("href")?.startsWith("#")) {
        event.preventDefault();
      }
    },
    true,
  );
}

afterEach(() => {
  for (const id of activeTimers) nativeClearTimeout(id);
  activeTimers.clear();
  cleanup();
  if (typeof localStorage !== "undefined") localStorage.clear();
  if (typeof sessionStorage !== "undefined") sessionStorage.clear();
});
