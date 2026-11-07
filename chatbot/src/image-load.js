/** Cache-busting URL for the nth reload; data:/blob: sources are reused as-is. */
export function retryImageURL(url, attempt) {
  if (!url || !attempt || /^(data|blob):/i.test(url)) return url;
  const hashAt = url.indexOf("#");
  const base = hashAt === -1 ? url : url.slice(0, hashAt);
  const hash = hashAt === -1 ? "" : url.slice(hashAt);
  const separator = base.includes("?") ? "&" : "?";
  return `${base}${separator}_retry=${attempt}${hash}`;
}

/** Status for an <img> that may have finished (e.g. from cache) before React attached handlers. */
export function initialImageStatus(img) {
  if (!img?.complete) return "loading";
  return img.naturalWidth > 0 ? "loaded" : "error";
}

/** Silent reloads a message image gets before the failure notice shows up. */
export const imageAutoRetries = 2;

/**
 * A message sent a moment ago is rendered before the server stores its
 * transcript, so the first attachment fetch can still 404. Retry those a couple
 * of times instead of asking the user to refresh the page.
 */
export function shouldAutoRetryImage(status, attempt) {
  return status === "error" && attempt < imageAutoRetries;
}

/** Backoff before the attempt-th silent reload, in ms. */
export function imageRetryDelay(attempt) {
  return 500 * (attempt + 1);
}
