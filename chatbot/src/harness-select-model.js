// Pure helpers behind HarnessSelect, kept DOM-free for node:test.

/** Options longer than this show a search box (same rule as web/app/hui-select.js). */
export const SEARCH_THRESHOLD = 5;

export function normalizeOptions(options = []) {
  return options.map((option) =>
    typeof option === "object" && option !== null
      ? {
          value: String(option.value ?? ""),
          label: String(option.label ?? option.value ?? ""),
          meta: option.meta == null ? "" : String(option.meta),
          disabled: !!option.disabled,
        }
      : { value: String(option), label: String(option), meta: "", disabled: false },
  );
}

export function selectedOption(options, value) {
  const key = String(value ?? "");
  return options.find((option) => option.value === key) || null;
}

export function filterOptions(options, query) {
  const needle = String(query || "").trim().toLocaleLowerCase();
  if (!needle) return options;
  return options.filter((option) =>
    [option.label, option.value, option.meta].join(" ").toLocaleLowerCase().includes(needle),
  );
}

export function showsSearch(options, searchable) {
  return searchable ?? options.length > SEARCH_THRESHOLD;
}

/** Index of the next enabled option for ArrowUp/ArrowDown/Home/End, wrapping around. */
export function nextOptionIndex(options, current, key) {
  const enabled = options.map((option, i) => (option.disabled ? -1 : i)).filter((i) => i >= 0);
  if (!enabled.length) return -1;
  if (key === "Home") return enabled[0];
  if (key === "End") return enabled[enabled.length - 1];
  const at = enabled.indexOf(current);
  if (at < 0) return key === "ArrowUp" ? enabled[enabled.length - 1] : enabled[0];
  const step = key === "ArrowUp" ? -1 : 1;
  return enabled[(at + step + enabled.length) % enabled.length];
}

/** Existing handlers read e.target.value, as they did with the native select. */
export function selectChangeEvent(value) {
  const target = { value: String(value) };
  return { target, currentTarget: target, type: "change" };
}

/** Below the trigger unless it does not fit and there is more room above; clamped to the viewport. */
export function placeSelectPanel(anchor, viewport, naturalHeight, { minWidth = 240, gap = 6, margin = 8, maxHeight = 360 } = {}) {
  const room = Math.max(0, viewport.width - 2 * margin);
  const width = Math.min(Math.max(anchor.width, minWidth), room);
  const below = viewport.height - anchor.bottom - gap - margin;
  const above = anchor.top - gap - margin;
  const wanted = Math.min(naturalHeight, maxHeight);
  const placement = below >= wanted || below >= above ? "bottom" : "top";
  const available = Math.max(120, placement === "bottom" ? below : above);
  const height = Math.min(wanted, available);
  const left = Math.max(margin, Math.min(anchor.left, viewport.width - width - margin));
  const top = placement === "bottom" ? anchor.bottom + gap : anchor.top - gap - height;
  return { placement, width, left, top: Math.max(margin, top), maxHeight: Math.min(maxHeight, available) };
}
