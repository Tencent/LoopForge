let counter = 0;

/**
 * Client-side id for placeholders that never leave the browser.
 * crypto.randomUUID only exists in secure contexts, and LoopForge is served
 * over plain HTTP, so fall back to a timestamp, a counter and a random suffix.
 * These ids only key UI state and must not be used as secrets.
 */
export function localId(prefix) {
  const uuid =
    typeof globalThis.crypto?.randomUUID === "function"
      ? globalThis.crypto.randomUUID()
      : `${Date.now().toString(36)}-${(counter++).toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
  return `${prefix}-${uuid}`;
}
