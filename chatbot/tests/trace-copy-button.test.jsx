import React from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";

const { apiMock } = vi.hoisted(() => ({
  apiMock: vi.fn(async () => ({ status: "ok", commit: "60fc455" })),
}));
vi.mock("../src/api.js", async (importOriginal) => ({
  ...(await importOriginal()),
  api: apiMock,
}));

import TraceCopyButton from "../src/TraceCopyButton";
import { resetPlatformCommit } from "../src/trace-diagnostics.js";

const original = navigator.clipboard;
afterEach(() => {
  resetPlatformCommit();
  apiMock.mockClear();
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: original });
});

test("copying a just-sent question includes its trace ID and the platform build", async () => {
  const writeText = vi.fn(async () => {});
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  const messages = [{ role: "user", content: "刚发出" }];
  render(
    <TraceCopyButton
      messages={messages}
      index={0}
      generation={{ id: "af-live", messageCount: 1, status: "running" }}
      session={{ id: "s1", executor: "codebuddy", model: "glm-5" }}
    />,
  );
  const button = screen.getByRole("button", { name: "复制 Trace ID 与排障信息" });
  // Nothing is fetched until someone reaches for the button.
  expect(apiMock).not.toHaveBeenCalled();
  fireEvent.pointerEnter(button.parentElement);
  await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/api/health"));
  await Promise.resolve();
  fireEvent.click(button);
  await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
  const text = writeText.mock.calls[0][0];
  expect(text).toContain("Trace ID: af-live");
  expect(text).toContain("会话 ID: s1");
  expect(text).toContain("模型: glm-5");
  expect(text).toContain("平台版本: 60fc455");
  await waitFor(() => expect(screen.getByRole("button", { name: "已复制" })).toBeInTheDocument());
});
