// @vitest-environment node
import { test } from "vitest";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  imageRetryDelay,
  initialImageStatus,
  retryImageURL,
  shouldAutoRetryImage,
} from "../src/image-load.js";

const styles = readFileSync(
  new URL("../src/styles.css", import.meta.url),
  "utf8",
);

test("first attempt keeps the original image URL", () => {
  const url = "/api/assistant/attachments/a1?sessionId=s1";
  assert.equal(retryImageURL(url, 0), url);
});

test("retries append a cache-busting counter", () => {
  assert.equal(
    retryImageURL("/api/assistant/attachments/a1?sessionId=s1", 2),
    "/api/assistant/attachments/a1?sessionId=s1&_retry=2",
  );
  assert.equal(retryImageURL("/img/a.png", 1), "/img/a.png?_retry=1");
  assert.equal(retryImageURL("/img/a.png#top", 1), "/img/a.png?_retry=1#top");
});

test("data and blob URLs are reused as-is", () => {
  const data = "data:image/png;base64,iVBORw0KGgo=";
  const blob = "blob:http://localhost/1234";
  assert.equal(retryImageURL(data, 3), data);
  assert.equal(retryImageURL(blob, 3), blob);
});

test("initial status reflects images already resolved from cache", () => {
  assert.equal(initialImageStatus(null), "loading");
  assert.equal(
    initialImageStatus({ complete: false, naturalWidth: 0 }),
    "loading",
  );
  assert.equal(
    initialImageStatus({ complete: true, naturalWidth: 640 }),
    "loaded",
  );
  assert.equal(
    initialImageStatus({ complete: true, naturalWidth: 0 }),
    "error",
  );
});

test("image placeholder keeps a stable themed footprint", () => {
  const at = styles.indexOf(
    '.message-image-frame:not([data-status="loaded"]) {',
  );
  assert.notEqual(at, -1);
  const rule = styles.slice(at, styles.indexOf("}", at));
  assert.match(rule, /min-height:\s*180px/);
  assert.match(rule, /background:\s*var\(--subtle\)/);
});

test("a failed first fetch is retried before the failure notice", () => {
  assert.equal(shouldAutoRetryImage("error", 0), true);
  assert.equal(shouldAutoRetryImage("error", 1), true);
  assert.equal(shouldAutoRetryImage("error", 2), false);
  assert.equal(shouldAutoRetryImage("loading", 0), false);
  assert.equal(shouldAutoRetryImage("loaded", 0), false);
});

test("silent reloads back off a little at a time", () => {
  assert.equal(imageRetryDelay(0), 500);
  assert.equal(imageRetryDelay(1), 1000);
});
