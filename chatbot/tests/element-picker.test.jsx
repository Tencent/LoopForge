import React from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import AgentBrowser from "../src/AgentBrowser.jsx";
import { componentOf, describeElement, elementLabel, elementSelector, elementTitle } from "../src/element-context.js";

function page(html) {
  document.body.innerHTML = html;
  return document;
}

describe("element context", () => {
  test("builds selectors that match only the element", () => {
    const doc = page(`
      <main id="app">
        <ul class="list"><li class="item">a</li><li class="item">b</li></ul>
        <button data-testid="save" class="css-1x2y3z primary">保存</button>
        <section><p>one</p><p>two</p></section>
      </main>`);
    const second = doc.querySelectorAll("li")[1];
    const save = doc.querySelector("button");
    const two = doc.querySelectorAll("p")[1];
    for (const el of [second, save, two, doc.getElementById("app")]) {
      const selector = elementSelector(el);
      expect(doc.querySelectorAll(selector)).toHaveLength(1);
      expect(doc.querySelector(selector)).toBe(el);
    }
    expect(elementSelector(save)).toBe('button[data-testid="save"]');
    expect(elementSelector(save)).not.toContain("css-1x2y3z");
    expect(elementLabel(save)).toBe("button.primary 「保存」");
  });

  test("finds the component a dev build exposes", () => {
    const doc = page(`<div id="root"><span>hi</span></div><b>vue</b><i>svelte</i>`);
    function SaveButton() {}
    doc.querySelector("span")["__reactFiber$abc"] = {
      type: "span",
      _debugSource: { fileName: "src/Save.tsx", lineNumber: 12, columnNumber: 3 },
      return: { type: SaveButton, return: null },
    };
    expect(componentOf(doc.querySelector("span"))).toEqual({
      framework: "React",
      component: "SaveButton",
      source: "src/Save.tsx:12:3",
    });
    doc.querySelector("b").__vueParentComponent = { type: { __name: "Toolbar", __file: "src/Toolbar.vue" } };
    expect(componentOf(doc.querySelector("b"))).toEqual({ framework: "Vue", component: "Toolbar", source: "src/Toolbar.vue" });
    doc.querySelector("i").__svelte_meta = { loc: { file: "src/Card.svelte", line: 4, column: 0 } };
    expect(componentOf(doc.querySelector("i"))).toEqual({ framework: "Svelte", component: "Card", source: "src/Card.svelte:5:1" });
    expect(componentOf(doc.getElementById("root"))).toBeNull();
  });

  test("describes the element for the Agent", () => {
    const doc = page(`<button id="go" style="color: red">${"长".repeat(400)}</button>`);
    const info = describeElement(doc.getElementById("go"), "http://localhost:5173/x");
    expect(info).toMatchObject({ url: "http://localhost:5173/x", selector: "#go", tag: "button" });
    expect(info.text.length).toBeLessThanOrEqual(201);
    expect(info.styles.color).toBe("rgb(255, 0, 0)");
    expect(elementTitle({ element: { ...info, component: "Go", source: "src/Go.tsx:1" } })).toBe(
      "Go · src/Go.tsx:1\n#go\nhttp://localhost:5173/x",
    );
  });
});

describe("element picking in the native preview", () => {
  test("picks an element in the frame and adds it to the composer", async () => {
    const add = vi.fn();
    const { container } = render(
      <AgentBrowser runId="run-7" request={{ id: "p1", url: "http://localhost:5173/" }} onAddElement={add} />,
    );
    const frame = container.querySelector("iframe.agent-browser-frame");
    const doc = frame.contentDocument;
    doc.open();
    doc.write(`<!doctype html><html><head></head><body><main><button class="primary">提交</button><a href="#x">link</a></main></body></html>`);
    doc.close();
    act(() => {
      fireEvent.load(frame);
    });
    // The button sits at the end of the address bar.
    expect(screen.getByRole("button", { name: "标记元素" }).closest(".agent-browser-address")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "标记元素" }));
    expect(screen.getByRole("button", { name: "标记元素" })).toHaveAttribute("aria-pressed", "true");
    expect(doc.querySelector("style[data-af-pick]")).not.toBeNull();
    const button = doc.querySelector("button");
    const pageClick = vi.fn();
    button.addEventListener("click", pageClick);
    act(() => {
      button.dispatchEvent(new MouseEvent("mousemove", { bubbles: true }));
    });
    expect(container.querySelector(".agent-browser-pick-outline")).not.toBeNull();
    act(() => {
      button.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    // The page never sees clicks made while picking.
    expect(pageClick).not.toHaveBeenCalled();
    expect(doc.querySelector("style[data-af-pick]")).toBeNull();
    expect(screen.getByRole("dialog", { name: "选中的元素" })).toHaveTextContent("button.primary 「提交」");
    fireEvent.click(screen.getByText("加入对话框"));
    expect(add).toHaveBeenCalledTimes(1);
    expect(add.mock.calls[0][0]).toMatchObject({
      type: "element",
      name: "button.primary 「提交」",
      element: { tag: "button", selector: "button.primary", text: "提交" },
    });
    expect(screen.queryByRole("dialog", { name: "选中的元素" })).toBeNull();

    // Escape leaves picking mode.
    fireEvent.click(screen.getByRole("button", { name: "标记元素" }));
    act(() => {
      doc.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(screen.getByRole("button", { name: "标记元素" })).toHaveAttribute("aria-pressed", "false");
  });

  test("offers picking only when the composer can take elements", () => {
    render(<AgentBrowser runId="run-8" request={{ id: "p2", url: "http://localhost:5173/" }} />);
    expect(screen.queryByRole("button", { name: "标记元素" })).toBeNull();
  });
});

class PickSocket {
  static OPEN = 1;
  static last = null;
  constructor() {
    this.readyState = 0;
    this.sent = [];
    PickSocket.last = this;
  }
  send(data) {
    this.sent.push(JSON.parse(data));
  }
  close() {
    this.readyState = 3;
  }
  open() {
    this.readyState = PickSocket.OPEN;
    this.onopen?.();
  }
  state(state) {
    this.onmessage?.({ data: JSON.stringify({ type: "state", state }) });
  }
}

const rect = (left, top, width, height) => () => ({ left, top, width, height, right: left + width, bottom: top + height, x: left, y: top });

describe("element picking on the Agent browser screencast", () => {
  afterEach(() => vi.unstubAllGlobals());

  test("inspects the page over CDP and adds the picked element", async () => {
    vi.stubGlobal("WebSocket", PickSocket);
    // jsdom has no PointerEvent; without it pointer coordinates are dropped.
    if (!window.PointerEvent) vi.stubGlobal("PointerEvent", class extends MouseEvent {});
    const inspections = [];
    const save = { label: "button#save 「保存」", rect: { x: 100, y: 50, width: 200, height: 40 }, viewport: { width: 1280, height: 800 } };
    const fetchMock = vi.fn(async (_path, init) => {
      const body = JSON.parse(init.body);
      if (body.operation === "inspect") inspections.push(body);
      const inspect = body.operation === "inspect" ? { ...save, ...(body.detail ? { element: { tag: "button", selector: "#save", url: "http://localhost:3000/" } } : {}) } : undefined;
      return { ok: true, status: 200, json: async () => ({ available: true, inspect }) };
    });
    vi.stubGlobal("fetch", fetchMock);
    const add = vi.fn();
    const { container } = render(<AgentBrowser runId="run-9" onAddElement={add} />);
    const socket = PickSocket.last;
    act(() => {
      socket.open();
      socket.state({ available: true, url: "http://localhost:3000/", width: 1280, height: 800, tabs: [] });
    });
    const stage = container.querySelector(".agent-browser-stage");
    const img = container.querySelector(".agent-browser-screen");
    // The panel shows the 1280px page at half size, 10px below the stage top.
    stage.getBoundingClientRect = rect(0, 0, 640, 500);
    img.getBoundingClientRect = rect(0, 10, 640, 400);

    expect(screen.getByRole("button", { name: "标记元素" }).closest(".agent-browser-address")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "标记元素" }));
    const capture = screen.getByTestId("agent-browser-pick-capture");
    const before = socket.sent.length;
    fireEvent.pointerMove(capture, { clientX: 160, clientY: 110 });
    await waitFor(() => expect(container.querySelector(".agent-browser-pick-outline")).not.toBeNull());
    expect(inspections[0]).toEqual({ operation: "inspect", point: { x: 0.25, y: 0.25 }, detail: false });
    const outline = container.querySelector(".agent-browser-pick-outline");
    expect(outline.style.left).toBe("50px");
    expect(outline.style.top).toBe("35px");
    expect(outline.style.width).toBe("100px");
    expect(outline.querySelector(".agent-browser-pick-label")).toHaveTextContent("button#save 「保存」");
    expect(outline.querySelector(".agent-browser-pick-size")).toHaveTextContent("100×20");

    fireEvent.click(capture, { clientX: 160, clientY: 110 });
    await waitFor(() => expect(screen.getByRole("button", { name: /加入对话框/ })).toBeEnabled());
    expect(inspections.at(-1)).toMatchObject({ detail: true });
    // Hovering and picking never reach the page as input.
    expect(socket.sent.slice(before).filter((m) => m.operation === "input")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: /加入对话框/ }));
    expect(add).toHaveBeenCalledWith(
      expect.objectContaining({ type: "element", name: "button#save 「保存」", element: expect.objectContaining({ selector: "#save" }) }),
    );
    expect(screen.queryByRole("dialog", { name: "选中的元素" })).toBeNull();

    // Escape leaves picking before the screencast forwards it to the page.
    fireEvent.click(screen.getByRole("button", { name: "标记元素" }));
    const sent = socket.sent.length;
    fireEvent.keyDown(container.querySelector(".agent-browser-ime"), { key: "Escape", code: "Escape", keyCode: 27 });
    expect(screen.getByRole("button", { name: "标记元素" })).toHaveAttribute("aria-pressed", "false");
    expect(socket.sent).toHaveLength(sent);
  });

  test("the runtime inspect script describes the element at a point", () => {
    const go = readFileSync(resolve(__dirname, "../../internal/runtime/browser_inspect.go"), "utf8");
    const script = go.match(/const inspectScript = `([\s\S]*?)`/)[1];
    document.body.innerHTML = `<main id="app"><button class="primary" data-testid="save" data-agentflow-ref="e14">保存<i data-agentflow-ref="e15"></i></button></main>`;
    const button = document.querySelector("button");
    button.getBoundingClientRect = rect(10, 20, 80, 30);
    button["__reactFiber$x"] = { type: "button", _debugSource: { fileName: "src/Save.tsx", lineNumber: 4 }, return: { type: function SaveButton() {} } };
    const hits = [];
    document.elementFromPoint = (x, y) => {
      hits.push([x, y]);
      return button;
    };
    try {
      const out = (0, eval)(script)(0.5, 0.25, true);
      expect(hits[0]).toEqual([innerWidth * 0.5, innerHeight * 0.25]);
      expect(out.label).toBe("button.primary 「保存」");
      expect(out.rect).toEqual({ x: 10, y: 20, width: 80, height: 30 });
      // Same description as the native preview builds in the panel.
      const native = describeElement(button, location.href);
      expect(out.element).toMatchObject({
        selector: native.selector,
        tag: "button",
        text: "保存",
        component: "SaveButton",
        framework: "React",
        source: "src/Save.tsx:4",
      });
      expect(out.element.selector).toBe('button[data-testid="save"]');
      // The Agent's snapshot refs are dropped on both sides.
      expect(out.element.html).toBe('<button class="primary" data-testid="save">保存<i></i></button>');
      expect(native.html).toBe(out.element.html);
      expect((0, eval)(script)(0.5, 0.25, false).element).toBeUndefined();
    } finally {
      delete document.elementFromPoint;
    }
  });
});
