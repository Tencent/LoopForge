import { useRef, useState } from "react";

const prefix = "loopforge.assistant.";

export function readComposerPreference(name) {
  try {
    return localStorage.getItem(prefix + name);
  } catch {
    return null;
  }
}

export function writeComposerPreference(name, value) {
  try {
    if (value === null) localStorage.removeItem(prefix + name);
    else localStorage.setItem(prefix + name, value);
    return true;
  } catch {
    return false;
  }
}

// Each conversation or draft has its own key. Keep choices in memory when
// browser storage is unavailable so switching away still preserves them.
export function useComposerOptions(userId) {
  const unsaved = useRef(new Map());
  const [, refresh] = useState(0);
  const keyFor = (scope) => `conversation.${userId}.${scope}`;

  function readOptions(scope) {
    if (userId == null) return {};
    const key = keyFor(scope);
    try {
      return JSON.parse(unsaved.current.get(key) ?? readComposerPreference(key)) || {};
    } catch {
      return {};
    }
  }

  function saveOptions(scope, value) {
    if (userId == null) return;
    const key = keyFor(scope);
    if (writeComposerPreference(key, value === null ? null : JSON.stringify(value)))
      unsaved.current.delete(key);
    else
      unsaved.current.set(key, JSON.stringify(value));
    refresh((version) => version + 1);
  }

  return { readOptions, saveOptions };
}
