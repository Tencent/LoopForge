export const THEME_KEY = "agentflow.theme.v3";
export const LEGACY_THEME_KEY = "agentflow.native-chat.theme";
export const THEME_PREFERENCES = ["light", "dark", "system"];

export function isThemePreference(value) {
  return THEME_PREFERENCES.includes(value);
}

export function readThemePreference(storage) {
  try {
    const current = storage.getItem(THEME_KEY);
    const legacy = storage.getItem(LEGACY_THEME_KEY);
    let preference = "light";
    if (isThemePreference(current)) preference = current;
    else if (isThemePreference(legacy)) {
      preference = legacy;
      storage.setItem(THEME_KEY, legacy);
    }
    if (legacy !== null) storage.removeItem(LEGACY_THEME_KEY);
    return preference;
  } catch {
    return "light";
  }
}

export function resolveTheme(preference, prefersDark) {
  if (preference === "system") return prefersDark ? "dark" : "light";
  return preference === "dark" ? "dark" : "light";
}

export function writeThemePreference(storage, preference) {
  if (!isThemePreference(preference)) return;
  try {
    if (storage.getItem(THEME_KEY) !== preference)
      storage.setItem(THEME_KEY, preference);
  } catch {}
}
