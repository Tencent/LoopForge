import React from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { Attachment } from "../src/RichContent.jsx";
import { imageRetryDelay } from "../src/image-load.js";

// jsdom never decodes images, so report what a browser has on first paint: the
// image request is still in flight.
Object.defineProperty(HTMLImageElement.prototype, "complete", {
  configurable: true,
  get: () => false,
});

const image = { id: "a1", name: "shot.png", mimeType: "image/png", size: 10 };

const source = () => screen.getByAltText("shot.png").getAttribute("src");

// Retries wait on real backoff timers; fake timers keep the test independent
// of machine load (a fixed real-time timeout made it fail on busy CI hosts).
const waitRetry = (attempt) =>
  act(() => vi.advanceTimersByTime(imageRetryDelay(attempt)));

describe("message image attachments", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  test("retries a failed first fetch before showing the failure", () => {
    render(<Attachment attachment={image} sessionId="s1" onOpen={vi.fn()} />);
    expect(source()).toBe("/api/assistant/attachments/a1?sessionId=s1");

    fireEvent.error(screen.getByAltText("shot.png"));
    expect(screen.queryByText("图片加载失败")).toBeNull();
    act(() => vi.advanceTimersByTime(imageRetryDelay(0) - 1));
    expect(source()).not.toContain("_retry=");
    waitRetry(0);
    expect(source()).toContain("_retry=1");

    fireEvent.error(screen.getByAltText("shot.png"));
    expect(screen.queryByText("图片加载失败")).toBeNull();
    waitRetry(1);
    expect(source()).toContain("_retry=2");

    fireEvent.error(screen.getByAltText("shot.png"));
    expect(screen.getByText("图片加载失败")).toBeInTheDocument();
  });

  test("a late load clears the failure instead of waiting for a refresh", () => {
    render(<Attachment attachment={image} sessionId="s1" onOpen={vi.fn()} />);
    fireEvent.error(screen.getByAltText("shot.png"));
    waitRetry(0);
    expect(source()).toContain("_retry=1");
    fireEvent.load(screen.getByAltText("shot.png"));
    expect(screen.queryByText("图片加载失败")).toBeNull();
  });
});
