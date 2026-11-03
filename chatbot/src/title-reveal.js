export const TITLE_FADE = 260;
const BASE_STEP = 16,
  MIN_STEP = 10,
  MAX_TOTAL = 900;

/** Per-character cadence for the header title; long titles compress the step so the whole reveal stays under ~0.9s. */
export function titleRevealTiming(title) {
  const chars = Array.from(title || "新对话");
  const gaps = Math.max(1, chars.length - 1);
  const step = Math.max(
    MIN_STEP,
    Math.min(BASE_STEP, (MAX_TOTAL - TITLE_FADE) / gaps),
  );
  return {
    chars,
    step,
    fade: TITLE_FADE,
    total: Math.round(step * (chars.length - 1) + TITLE_FADE),
  };
}

/** Only a conversation reached by switching (or a direct URL) reveals its title, once per arrival. */
export function shouldRevealTitle({
  sessionId,
  revealId,
  lastRevealedId,
  reducedMotion = false,
}) {
  return (
    !!sessionId &&
    sessionId === revealId &&
    sessionId !== lastRevealedId &&
    !reducedMotion
  );
}
