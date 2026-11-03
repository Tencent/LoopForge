// Placement for fixed popovers anchored to a trigger. Pure functions so the
// direction / height decisions can be unit tested without a DOM.
export const MENU_MARGIN = 8;
export const MENU_GAP = 8;
export const MENU_MAX_HEIGHT = 360;
export const MENU_WIDTH = 300;

export function intersectRects(a, b) {
  return {
    top: Math.max(a.top, b.top),
    left: Math.max(a.left, b.left),
    bottom: Math.min(a.bottom, b.bottom),
    right: Math.min(a.right, b.right),
  };
}

/** Vertical room is the viewport cut by clipping ancestors; horizontal room is the viewport. */
export function availableBounds(viewport, clips = []) {
  const clipped = clips.reduce(intersectRects, viewport);
  return {
    top: clipped.top,
    bottom: Math.max(clipped.top, clipped.bottom),
    left: viewport.left,
    right: viewport.right,
  };
}

/** False once the trigger has scrolled out of the visible area. */
export function anchorVisible(anchorRect, bounds) {
  return (
    anchorRect.width > 0 &&
    anchorRect.height > 0 &&
    anchorRect.bottom > bounds.top &&
    anchorRect.top < bounds.bottom &&
    anchorRect.right > bounds.left &&
    anchorRect.left < bounds.right
  );
}

/**
 * Prefer opening above the trigger; open below only when the menu does not
 * fit above and there is more room below. `keepSide` pins the previous side
 * (used while searching so the menu does not jump).
 */
export function placeMenu(
  anchorRect,
  bounds,
  naturalHeight,
  width = MENU_WIDTH,
  keepSide = null,
  { margin = MENU_MARGIN, gap = MENU_GAP, maxHeight = MENU_MAX_HEIGHT } = {},
) {
  const above = Math.max(0, anchorRect.top - bounds.top - gap - margin);
  const below = Math.max(0, bounds.bottom - anchorRect.bottom - gap - margin);
  const wanted = Math.max(0, Math.min(naturalHeight, maxHeight));
  const side =
    keepSide === "above" || keepSide === "below"
      ? keepSide
      : above >= wanted || above >= below
        ? "above"
        : "below";
  const room = side === "above" ? above : below;
  const height = Math.min(wanted, room);
  const menuWidth = Math.max(
    0,
    Math.min(width, bounds.right - bounds.left - 2 * margin),
  );
  const left = Math.max(
    bounds.left + margin,
    Math.min(anchorRect.left, bounds.right - menuWidth - margin),
  );
  const top =
    side === "above" ? anchorRect.top - gap - height : anchorRect.bottom + gap;
  return {
    side,
    top,
    left,
    width: menuWidth,
    height,
    maxHeight: Math.min(maxHeight, room),
  };
}
