import React, { useState, useRef } from "react";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import ReferenceInput from "../src/ReferenceInput";
import { mountPromptEditor } from "../src/prompt-editor-entry";

afterEach(() => vi.unstubAllGlobals());
const skill = {
  type: "skill",
  id: "qa",
  name: "测试流程",
  projectId: "project-a",
};
function mockReferences(items = [skill]) {
  const fetch = vi.fn(async () => ({
    ok: true,
    status: 200,
    json: async () => ({ items }),
  }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
function ConversationInput() {
  const [draft, setDraft] = useState({ text: "", references: [] });
  const [mention, setMention] = useState(null);
  const input = useRef(),
    editor = useRef();
  return (
    <div className="composer">
      <ReferenceInput
        ref={editor}
        editorRef={input}
        projectId="project-a"
        {...draft}
        mention={mention}
        onMentionChange={setMention}
        onChange={setDraft}
        textareaProps={{ "aria-label": "消息" }}
      />
      <button onClick={() => editor.current.openReferences()}>
        引用项目资产
      </button>
    </div>
  );
}
test("conversation input uses @ search, keeps structured references and removes deleted mentions", async () => {
  const fetch = mockReferences();
  render(<ConversationInput />);
  const input = screen.getByRole("textbox", { name: "消息" });
  fireEvent.change(input, {
    target: { value: "请使用 @测试", selectionStart: 7 },
  });
  await waitFor(() =>
    expect(
      screen.getByRole("option", { name: /测试流程/ }),
    ).toBeInTheDocument(),
  );
  fireEvent.click(screen.getByRole("option", { name: /测试流程/ }));
  expect(input.value).toContain("@测试流程");
  expect(
    screen.getByRole("button", { name: "移除引用 测试流程" }),
  ).toBeInTheDocument();
  const request = new URL(fetch.mock.calls[0][0], "http://localhost");
  expect(request.searchParams.get("projectId")).toBe("project-a");
  expect(request.searchParams.has("scope")).toBe(false);
  fireEvent.change(input, { target: { value: "继续测试" } });
  expect(
    screen.queryByRole("button", { name: "移除引用 测试流程" }),
  ).not.toBeInTheDocument();
});

test("manual references omit unsupported types from mixed search results", async () => {
  mockReferences([skill, { type: "unknown", id: "unknown", name: "未知资产" }]);
  render(<ConversationInput />);
  fireEvent.click(screen.getByRole("button", { name: "引用项目资产" }));
  expect(screen.getByRole("option", { name: /技能/ })).toBeInTheDocument();
  const input = screen.getByRole("textbox", { name: "消息" });
  fireEvent.change(input, { target: { value: "@测试", selectionStart: 3 } });
  await waitFor(() =>
    expect(
      screen.getByRole("option", { name: /测试流程/ }),
    ).toBeInTheDocument(),
  );
  expect(
    screen.queryByRole("option", { name: /未知资产/ }),
  ).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("option", { name: /测试流程/ }));
  expect(
    screen.getByRole("button", { name: "移除引用 测试流程" }),
  ).toBeInTheDocument();
});

test("Escape closes the picker without bubbling into the surrounding modal", async () => {
  mockReferences();
  render(<ConversationInput />);
  const input = screen.getByRole("textbox", { name: "消息" });
  let bubbled = 0;
  const onDocumentKeyDown = (event) => {
    if (event.key === "Escape") bubbled++;
  };
  document.addEventListener("keydown", onDocumentKeyDown);
  try {
    fireEvent.change(input, { target: { value: "@测试", selectionStart: 3 } });
    fireEvent.click(input);
    await waitFor(() =>
      expect(
        screen.getByRole("option", { name: /测试流程/ }),
      ).toBeInTheDocument(),
    );
    fireEvent.keyDown(input, { key: "Escape", bubbles: true });
    expect(
      screen.queryByRole("option", { name: /测试流程/ }),
    ).not.toBeInTheDocument();
    expect(bubbled).toBe(0);
    fireEvent.change(input, { target: { value: "@测", selectionStart: 2 } });
    await waitFor(() =>
      expect(
        screen.getByRole("option", { name: /测试流程/ }),
      ).toBeInTheDocument(),
    );
    const option = screen.getByRole("option", { name: /测试流程/ });
    option.focus();
    fireEvent.keyDown(option, { key: "Escape", bubbles: true });
    expect(
      screen.queryByRole("option", { name: /测试流程/ }),
    ).not.toBeInTheDocument();
    expect(bubbled).toBe(0);
  } finally {
    document.removeEventListener("keydown", onDocumentKeyDown);
  }
});

test("Escape stays dismissed through selection events until the caret or draft changes", async () => {
  mockReferences();
  render(<ConversationInput />);
  const input = screen.getByRole("textbox", { name: "消息" });
  act(() => input.focus());
  fireEvent.change(input, {
    target: { value: "@测试", selectionStart: 3 },
  });
  await screen.findByRole("option", { name: /测试流程/ });
  fireEvent.keyDown(input, { key: "Escape" });
  fireEvent.keyUp(input, { key: "Escape" });
  fireEvent.select(input);
  expect(input).toHaveAttribute("aria-expanded", "false");
  expect(input).toHaveFocus();
  expect(input.value).toBe("@测试");

  // A deliberate caret movement or input must still reopen suggestions.
  input.setSelectionRange(2, 2);
  fireEvent.select(input);
  expect(input).toHaveAttribute("aria-expanded", "true");
  await screen.findByRole("option", { name: /测试流程/ });
  const search = screen.getByRole("textbox", { name: "搜索引用对象" });
  act(() => search.focus());
  fireEvent.keyDown(search, { key: "Escape" });
  fireEvent.keyUp(input, { key: "Escape" });
  fireEvent.select(input);
  expect(input).toHaveAttribute("aria-expanded", "false");
  expect(input).toHaveFocus();

  // Explicitly clicking the same position must reopen the picker too.
  fireEvent.click(input);
  expect(input).toHaveAttribute("aria-expanded", "true");
  fireEvent.keyDown(input, { key: "Escape" });
  fireEvent.keyUp(input, { key: "Escape" });
  fireEvent.select(input);
  expect(input).toHaveAttribute("aria-expanded", "false");

  fireEvent.change(input, {
    target: { value: "@测", selectionStart: 2 },
  });
  expect(input).toHaveAttribute("aria-expanded", "true");
});

test("native scheduled editor mounts the shared picker, restores assets and clears stale state on reopening", async () => {
  mockReferences();
  const host = document.createElement("div");
  document.body.append(host);
  const changes = vi.fn();
  let editor;
  act(() => {
    editor = mountPromptEditor(host, {
      value: { projectId: "project-a", text: "检查", references: [] },
      onChange: changes,
    });
  });
  try {
    const ui = within(host.shadowRoot);
    fireEvent.click(ui.getByRole("button", { name: /引用项目资产/ }));
    fireEvent.click(ui.getByRole("option", { name: /技能/ }));
    await waitFor(() =>
      expect(ui.getByRole("option", { name: /测试流程/ })).toBeInTheDocument(),
    );
    fireEvent.click(ui.getByRole("option", { name: /测试流程/ }));
    expect(changes.mock.lastCall[0].references).toEqual([skill]);
    expect(editor.getValue().text).toContain("@测试流程");
    expect(
      ui.queryByRole("button", { name: "移除引用 测试流程" }),
    ).not.toBeInTheDocument();
    fireEvent.change(ui.getByRole("textbox", { name: "提示词" }), {
      target: { value: "检查", selectionStart: 2 },
    });
    expect(editor.getValue().references).toEqual([]);
    act(() =>
      editor.setValue({
        projectId: "project-b",
        text: "另一个任务",
        references: [skill],
      }),
    );
    expect(ui.getByRole("textbox", { name: "提示词" }).value).toBe(
      "另一个任务",
    );
    expect(
      ui.queryByRole("button", { name: "移除引用 测试流程" }),
    ).not.toBeInTheDocument();
    act(() =>
      editor.setValue({
        projectId: "project-b",
        text: "新任务",
        references: [],
      }),
    );
    expect(
      ui.queryByRole("button", { name: "移除引用 测试流程" }),
    ).not.toBeInTheDocument();
  } finally {
    act(() => editor.destroy());
    host.remove();
  }
});

test("shared subscriptions display pinned references without allowing replacement", async () => {
  const fetch = mockReferences();
  const host = document.createElement("div");
  document.body.append(host);
  let editor;
  act(() => {
    editor = mountPromptEditor(host, {
      value: {
        projectId: "project-a",
        text: "固定共享提示词 @测试流程",
        references: [skill],
        readOnly: true,
      },
      onChange: vi.fn(),
    });
  });
  try {
    const ui = within(host.shadowRoot),
      input = ui.getByRole("textbox", { name: "提示词" });
    expect(input.readOnly).toBe(true);
    expect(input.value).toContain("@测试流程");
    expect(
      ui.queryByRole("button", { name: /引用项目资产|移除引用/ }),
    ).not.toBeInTheDocument();
    fireEvent.click(input);
    expect(ui.queryByRole("dialog")).not.toBeInTheDocument();
    expect(fetch).not.toHaveBeenCalled();
    expect(editor.getValue().references).toEqual([skill]);
  } finally {
    act(() => editor.destroy());
    host.remove();
  }
});

test("scheduled picker exposes the same six categories as the homepage", async () => {
  const fetch = mockReferences([
    skill,
    { type: "issue", id: "i1", name: "测试需求" },
    { type: "repository", id: "r1", name: "测试仓库" },
  ]);
  const host = document.createElement("div");
  document.body.append(host);
  let editor;
  act(() => {
    editor = mountPromptEditor(host, {
      value: { projectId: "project-a", text: "", references: [] },
      onChange: vi.fn(),
    });
  });
  try {
    const ui = within(host.shadowRoot);
    fireEvent.click(ui.getByRole("button", { name: /引用项目资产/ }));
    expect(ui.getAllByRole("option")).toHaveLength(6);
    fireEvent.change(ui.getByRole("textbox", { name: "提示词" }), {
      target: { value: "@测试", selectionStart: 3 },
    });
    await waitFor(() =>
      expect(ui.getByRole("option", { name: /测试流程/ })).toBeInTheDocument(),
    );
    expect(ui.getAllByRole("option")).toHaveLength(3);
    const url = new URL(fetch.mock.calls.at(-1)[0], "http://localhost");
    expect(url.searchParams.get("projectId")).toBe("project-a");
    expect(url.searchParams.has("scope")).toBe(false);
    expect(
      ui.queryByRole("button", { name: "返回所有引用类别" }),
    ).not.toBeInTheDocument();
  } finally {
    act(() => editor.destroy());
    host.remove();
  }
});

test("Shadow DOM Escape contains picker events from the toolbar and options, then allows modal dismissal", async () => {
  mockReferences();
  const host = document.createElement("div");
  document.body.append(host);
  let editor;
  let escaped = 0;
  const globalEscape = (event) => {
    if (event.key === "Escape") escaped++;
  };
  document.addEventListener("keydown", globalEscape);
  act(() => {
    editor = mountPromptEditor(host, {
      value: { projectId: "project-a", text: "未保存的草稿", references: [] },
      onChange: vi.fn(),
    });
  });
  try {
    const ui = within(host.shadowRoot),
      button = ui.getByRole("button", { name: /引用项目资产/ });
    fireEvent.click(button);
    button.focus();
    fireEvent.keyDown(button, { key: "Escape", bubbles: true, composed: true });
    expect(escaped).toBe(0);
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(host.shadowRoot.activeElement).toBe(button);
    fireEvent.click(button);
    const option = ui.getByRole("option", { name: /技能/ });
    option.focus();
    fireEvent.keyDown(option, { key: "Escape", bubbles: true, composed: true });
    expect(escaped).toBe(0);
    expect(host.shadowRoot.activeElement).toBe(button);
    expect(editor.getValue().text).toBe("未保存的草稿");
    fireEvent.keyDown(button, { key: "Escape", bubbles: true, composed: true });
    expect(escaped).toBe(1);

    const input = ui.getByRole("textbox", { name: "提示词" });
    act(() => input.focus());
    fireEvent.change(input, {
      target: { value: "未保存的草稿 @测试", selectionStart: 10 },
    });
    await ui.findByRole("option", { name: /测试流程/ });
    fireEvent.keyDown(input, { key: "Escape", bubbles: true, composed: true });
    fireEvent.keyUp(input, { key: "Escape", bubbles: true, composed: true });
    fireEvent.select(input);
    expect(escaped).toBe(1);
    expect(input).toHaveAttribute("aria-expanded", "false");
    expect(host.shadowRoot.activeElement).toBe(input);
    expect(editor.getValue().text).toBe("未保存的草稿 @测试");

    fireEvent.click(button);
    expect(input).toHaveAttribute("aria-expanded", "true");
    fireEvent.keyDown(input, { key: "Escape", bubbles: true, composed: true });
    fireEvent.keyUp(button, { key: "Escape", bubbles: true, composed: true });
    expect(escaped).toBe(1);
    expect(input).toHaveAttribute("aria-expanded", "false");
    expect(host.shadowRoot.activeElement).toBe(button);

    fireEvent.click(input);
    await ui.findByRole("option", { name: /测试流程/ });
    const search = ui.getByRole("textbox", { name: "搜索引用对象" });
    act(() => search.focus());
    fireEvent.keyDown(search, { key: "Escape", bubbles: true, composed: true });
    expect(escaped).toBe(1);
    expect(host.shadowRoot.activeElement).toBe(button);
    fireEvent.keyUp(input, { key: "Escape", bubbles: true, composed: true });
    fireEvent.select(input);
    expect(input).toHaveAttribute("aria-expanded", "false");
    fireEvent.keyDown(input, { key: "Escape", bubbles: true, composed: true });
    expect(escaped).toBe(2);
  } finally {
    document.removeEventListener("keydown", globalEscape);
    act(() => editor.destroy());
    host.remove();
  }
});

test("scheduled editor keeps legacy references without rendering chips", () => {
  const host = document.createElement("div");
  document.body.append(host);
  let editor;
  const legacy = { type: "issue", id: "old-issue", name: "旧需求" };
  act(() => {
    editor = mountPromptEditor(host, {
      value: {
        projectId: "project-a",
        text: "旧提示词",
        references: [legacy, skill],
      },
      onChange: vi.fn(),
    });
  });
  try {
    const ui = within(host.shadowRoot);
    expect(editor.getValue().text).toBe("旧提示词");
    expect(
      ui.queryByRole("button", { name: /移除引用/ }),
    ).not.toBeInTheDocument();
    expect(editor.getValue().references).toEqual([legacy, skill]);
    expect(editor.getValue().text).toBe("旧提示词");
  } finally {
    act(() => editor.destroy());
    host.remove();
  }
});

test("structured references deduplicate and stop at twelve while plain @ stays text", async () => {
  mockReferences();
  const host = document.createElement("div");
  document.body.append(host);
  let editor;
  const refs = Array.from({ length: 12 }, (_, index) => ({
    ...skill,
    id: `s${index}`,
  }));
  const onError = vi.fn();
  act(() => {
    editor = mountPromptEditor(host, {
      value: { projectId: "project-a", text: "草稿", references: refs },
      onChange: vi.fn(),
      onError,
    });
  });
  try {
    const ui = within(host.shadowRoot),
      input = ui.getByRole("textbox", { name: "提示词" });
    fireEvent.change(input, {
      target: { value: "@未选择", selectionStart: 4 },
    });
    expect(editor.getValue().references).toHaveLength(12);
    fireEvent.change(input, { target: { value: "@测试", selectionStart: 3 } });
    await waitFor(() =>
      expect(ui.getByRole("option", { name: /测试流程/ })).toBeInTheDocument(),
    );
    fireEvent.click(ui.getByRole("option", { name: /测试流程/ }));
    expect(onError).toHaveBeenCalledWith(expect.stringContaining("12"));
    expect(editor.getValue().references).toHaveLength(12);
    act(() =>
      editor.setValue({
        projectId: "project-a",
        text: "草稿",
        references: [skill],
        scope: "dedup",
      }),
    );
    fireEvent.click(ui.getByRole("button", { name: /引用项目资产/ }));
    fireEvent.click(ui.getByRole("option", { name: /技能/ }));
    await waitFor(() =>
      expect(ui.getByRole("option", { name: /测试流程/ })).toBeInTheDocument(),
    );
    fireEvent.click(ui.getByRole("option", { name: /测试流程/ }));
    expect(editor.getValue().references).toEqual([skill]);
  } finally {
    act(() => editor.destroy());
    host.remove();
  }
});
