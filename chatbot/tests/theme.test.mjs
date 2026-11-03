import { test } from "vitest";
import assert from "node:assert/strict";
import {
  THEME_KEY,
  LEGACY_THEME_KEY,
  readThemePreference,
  resolveTheme,
  writeThemePreference,
} from "../src/theme.js";

function memoryStorage(initial = {}) {
  const items = new Map(Object.entries(initial));
  return {
    items,
    writes: 0,
    getItem(key) {
      return items.has(key) ? items.get(key) : null;
    },
    setItem(key, value) {
      this.writes += 1;
      items.set(key, String(value));
    },
    removeItem(key) {
      items.delete(key);
    },
  };
}

test("the shell and the chat share the agentflow.theme.v3 key", () => {
  assert.equal(THEME_KEY, "agentflow.theme.v3");
  assert.equal(LEGACY_THEME_KEY, "agentflow.native-chat.theme");
});

test("a valid shared preference wins over the legacy chat key, which is removed", () => {
  const storage = memoryStorage({ [THEME_KEY]: "dark", [LEGACY_THEME_KEY]: "light" });
  assert.equal(readThemePreference(storage), "dark");
  assert.equal(storage.getItem(THEME_KEY), "dark");
  assert.equal(storage.getItem(LEGACY_THEME_KEY), null);
});

test("a legacy chat preference migrates into the shared key", () => {
  const storage = memoryStorage({ [LEGACY_THEME_KEY]: "system" });
  assert.equal(readThemePreference(storage), "system");
  assert.equal(storage.getItem(THEME_KEY), "system");
  assert.equal(storage.getItem(LEGACY_THEME_KEY), null);
});

test("an invalid shared value falls back to a valid legacy value", () => {
  const storage = memoryStorage({ [THEME_KEY]: "blue", [LEGACY_THEME_KEY]: "dark" });
  assert.equal(readThemePreference(storage), "dark");
  assert.equal(storage.getItem(THEME_KEY), "dark");
});

test("missing or invalid values default to light", () => {
  assert.equal(readThemePreference(memoryStorage()), "light");
  const storage = memoryStorage({ [THEME_KEY]: "blue", [LEGACY_THEME_KEY]: "neon" });
  assert.equal(readThemePreference(storage), "light");
  assert.equal(storage.getItem(LEGACY_THEME_KEY), null);
});

test("storage failures default to light", () => {
  const broken = {
    getItem() {
      throw new Error("denied");
    },
  };
  assert.equal(readThemePreference(broken), "light");
  assert.doesNotThrow(() => writeThemePreference(broken, "dark"));
});

test("resolveTheme only produces light or dark", () => {
  assert.equal(resolveTheme("light", true), "light");
  assert.equal(resolveTheme("dark", false), "dark");
  assert.equal(resolveTheme("system", true), "dark");
  assert.equal(resolveTheme("system", false), "light");
  assert.equal(resolveTheme("blue", true), "light");
});

test("writeThemePreference skips unchanged and invalid values", () => {
  const storage = memoryStorage({ [THEME_KEY]: "dark" });
  writeThemePreference(storage, "dark");
  writeThemePreference(storage, "blue");
  assert.equal(storage.writes, 0);
  writeThemePreference(storage, "system");
  assert.equal(storage.getItem(THEME_KEY), "system");
  assert.equal(storage.writes, 1);
});
