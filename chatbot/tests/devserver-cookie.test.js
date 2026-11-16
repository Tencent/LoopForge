// The dev server intercept script (internal/preview/devserver_intercept.js)
// runs in the user's browser under /devpreview/{run}/{port}/. Its cookie
// setter must keep a page's cookies inside that preview: same port in another
// task, and the platform itself, never see them.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, expect, test } from "vitest";

const source = readFileSync(resolve(__dirname, "../../internal/preview/devserver_intercept.js"), "utf8");
const raw = Object.getOwnPropertyDescriptor(Document.prototype, "cookie");
const browserCookies = () => raw.get.call(document);

function openPreview(run, port) {
  history.replaceState(null, "", `/devpreview/${run}/${port}/app/`);
  delete window.__AGENTFLOW_DEV_SERVER__;
  delete document.cookie;
  const prefix = `/devpreview/${run}/${port}`;
  // As preview.DevServerCookiePrefix fills it in.
  const cookiePrefix = `af_devpreview_${run}_${port}_`;
  (0, eval)(
    source
      .replace("__AF_PREFIX__", JSON.stringify(prefix))
      .replace("__AF_PORT__", String(port))
      .replace("__AF_COOKIE__", JSON.stringify(cookiePrefix)),
  );
}

afterEach(() => {
  history.replaceState(null, "", "/");
});

test("a page's root-path cookie stays inside its own preview", () => {
  openPreview("run-a", 3000);
  document.cookie = "sid=secret; Path=/; Domain=localhost";
  document.cookie = "theme=dark";
  expect(document.cookie).toContain("sid=secret");
  expect(document.cookie).toContain("theme=dark");
  expect(browserCookies()).toContain("af_devpreview_run-a_3000_sid=secret");

  // The platform root does not carry it…
  history.replaceState(null, "", "/chatbot/");
  expect(browserCookies()).not.toContain("sid=secret");
  // …and neither does another task serving the same port.
  openPreview("run-b", 3000);
  expect(browserCookies()).not.toContain("sid=secret");
  expect(document.cookie).not.toContain("sid");
  document.cookie = "sid=other; Path=/";
  expect(document.cookie).toBe("sid=other");

  // Back in the first preview its own cookie is intact.
  openPreview("run-a", 3000);
  expect(document.cookie).toContain("sid=secret");
  expect(document.cookie).not.toContain("sid=other");
});

test("paths already inside the preview are kept", () => {
  openPreview("run-c", 4000);
  document.cookie = "a=1; Path=/devpreview/run-c/4000/app";
  document.cookie = "b=2; path=/api";
  history.replaceState(null, "", "/devpreview/run-c/4000/api/x");
  expect(document.cookie).toContain("b=2");
  expect(document.cookie).not.toContain("a=1");
  history.replaceState(null, "", "/devpreview/run-c/4000/app/x");
  expect(document.cookie).toContain("a=1");
});
