import React from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

const { project, user, apiMock, writeMock, mockState } = vi.hoisted(() => {
  const project = {
    id: "p1", key: "P1", name: "项目一", ownerId: 1, members: [1],
    workflowId: "generic-backend-delivery",
  };
  const user = {
    id: 1, loginName: "tester", chineseName: "测试",
    gitTokenConfigured: true, codeBuddyApiKeyConfigured: true,
  };
  const apiMock = vi.fn(async (path) => {
  if (path === "/api/users" && mockState.users) return mockState.users();
  if (path === "/api/users")
    return [mockState.unconfigured ? {
      id: 1, loginName: "tester", chineseName: "测试",
      gitTokenConfigured: false, codeBuddyApiKeyConfigured: false,
    } : user];
  if (path === "/api/projects") return [project];
  if (path === "/api/repositories") return [];
  if (path === "/api/workflows")
    return [
      {
        id: "generic-backend-delivery",
        name: "通用后端交付 SOP",
        generic: true,
        nodes: [{ id: "build", name: "实现", type: "agent" }],
      },
    ];
  if (path === "/api/agents") return [];
  if (path.includes("/agents")) return [];
  if (path.includes("/verify")) return { valid: true };
  return [];
  });
  const mockState = { failClone: false, unconfigured: false, users: null };
  const writeMock = vi.fn(async (path, body) => {
    if (path.includes("/git-token/verify")) return { valid: true };
    if (path === "/api/projects") return { ...project, ...body };
    if (path.includes("/clone")) {
      if (mockState.failClone) throw new Error("clone unavailable");
      return { id: "generic-backend-delivery-p1", name: "后端交付 SOP" };
    }
    if (path.includes("/publish"))
      return { id: "generic-backend-delivery-p1", name: "后端交付 SOP", status: "published" };
    return { id: body?.id || "saved", ...body };
  });
  return { project, user, apiMock, writeMock, mockState };
});

vi.mock("../src/api.js", async (original) => ({
  ...(await original()),
  api: apiMock,
  write: writeMock,
}));

import QuickAccessWizard, {
  HarnessSelect,
  HarnessSwitch,
  parseGongfengRepo,
  sharedModelNames,
} from "../src/QuickAccessWizard.jsx";

describe("QuickAccessWizard", () => {
  test("names the shared models an administrator allowed", () => {
    expect(sharedModelNames(["glm-5.1", "deepseek-v4.1-flash"])).toBe("glm-5.1、Deepseek-V4.1-Flash");
    expect(sharedModelNames([], "deepseek-v4.1-flash")).toBe("Deepseek-V4.1-Flash");
    expect(sharedModelNames(undefined)).toBe("");
  });

  test("parses supported repository addresses", () => {
    expect(parseGongfengRepo("https://github.com/acme/widgets")).toEqual({
      project: "github:acme/widgets",
      provider: "github",
      name: "widgets",
      webUrl: "https://github.com/acme/widgets",
      cloneUrl: "https://github.com/acme/widgets.git",
    });
    expect(parseGongfengRepo("git@github.com:acme/widgets.git")?.project).toBe("github:acme/widgets");
    expect(parseGongfengRepo("github:acme/widgets")?.cloneUrl).toBe("https://github.com/acme/widgets.git");
    expect(parseGongfengRepo("git clone https://github.com/acme/widgets.git")?.name).toBe("widgets");
    expect(parseGongfengRepo("https://token@github.com/acme/widgets")).toBeNull();
    expect(parseGongfengRepo("https://github.com/acme")).toBeNull();
    expect(parseGongfengRepo("git@git.example.com:team/repo.git")?.project).toBe("team/repo");
    expect(parseGongfengRepo("git@git.example.com:team/repo.git")?.provider).toBe("gongfeng");
    expect(parseGongfengRepo("https://git.example.com/team/repo/-/tree/main")?.name).toBe("repo");
    expect(parseGongfengRepo("https://example.com/team/repo")).toBeNull();
    expect(parseGongfengRepo("")).toBeNull();
  });

  test("select and switch controls respond to keyboard and pointer input", async () => {
    const userAction = userEvent.setup();
    const changed = vi.fn();
    render(
      <>
        <HarnessSelect
          value=""
          options={[{ value: "one", label: "选项一" }, { value: "two", label: "选项二" }]}
          onChange={changed}
          ariaLabel="测试选择"
          searchable
          allowCustom
        />
        <HarnessSwitch checked={false} onChange={changed} label="测试开关" />
      </>,
    );
    await userAction.click(screen.getByLabelText("测试选择"));
    await userAction.click(screen.getByText("选项二"));
    await userAction.click(screen.getByRole("switch"));
    expect(changed).toHaveBeenCalled();
  });

  test("walks through existing-project setup to success", async () => {
    const userAction = userEvent.setup();
    const completed = vi.fn();
    render(
      <QuickAccessWizard
        open
        user={user}
        projects={[project]}
        projectId="p1"
        sponsoredModel="m1"
        onClose={vi.fn()}
        onCompleted={completed}
        onOpenView={vi.fn()}
      />,
    );
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/api/projects"));

    await userAction.click(screen.getByRole("button", { name: "下一步" }));
    await screen.findByText("项目与仓库", { selector: "h3" });
    await userAction.click(screen.getByRole("button", { name: "下一步" }));
    await screen.findByText("默认角色与 SOP", { selector: "h3" });
    await userAction.click(screen.getByRole("button", { name: "完成接入" }));

    expect(await screen.findByText("快速接入已完成")).toBeInTheDocument();
    expect(completed).toHaveBeenCalledWith("p1");
  });

  test("validates and saves typed credentials", async () => {
    mockState.unconfigured = true;
    const userAction = userEvent.setup();
    render(
      <QuickAccessWizard
        open
        user={{ id: 1, loginName: "tester", chineseName: "测试" }}
        projects={[project]}
        projectId="p1"
        onClose={vi.fn()}
        onCompleted={vi.fn()}
        onOpenView={vi.fn()}
      />,
    );
    await screen.findByRole("dialog");
    await userAction.click(screen.getByRole("button", { name: "下一步" }));
    expect(screen.getByText(/请至少填写 GitHub Git Token 与 CodeBuddy API Key/)).toBeInTheDocument();
    expect(screen.queryByText(/工蜂/)).toBeNull();
    await userAction.click(screen.getByText("使用访问令牌（高级）"));
    await userAction.type(
      screen.getByPlaceholderText(/GitHub personal access token/),
      "token-value",
    );
    await userAction.click(screen.getByRole("button", { name: "校验" }));
    await waitFor(() => expect(writeMock).toHaveBeenCalledWith(
      "/api/users/1/credentials",
      expect.objectContaining({ gitToken: "token-value" }),
      "PUT",
    ));
    mockState.unconfigured = false;
  });

  test("saves a typed CodeBuddy key when moving past the credential step", async () => {
    mockState.unconfigured = true;
    writeMock.mockClear();
    const userAction = userEvent.setup();
    render(
      <QuickAccessWizard
        open
        user={{ id: 1, loginName: "tester", chineseName: "测试" }}
        projects={[project]}
        projectId="p1"
        onClose={vi.fn()}
        onCompleted={vi.fn()}
        onOpenView={vi.fn()}
      />,
    );
    await screen.findByRole("dialog");
    await userAction.type(screen.getByPlaceholderText("粘贴 CodeBuddy API Key"), "cb-key");
    await userAction.click(screen.getByRole("button", { name: "下一步" }));
    await screen.findByText("项目与仓库", { selector: "h3" });
    expect(writeMock).toHaveBeenCalledWith(
      "/api/users/1/credentials",
      { codeBuddyApiKey: "cb-key", codeBuddyInternetEnvironment: "" },
      "PUT",
    );
    mockState.unconfigured = false;
  });

  test("saves the CodeBuddy key with the chosen network environment and links to its key page", async () => {
    mockState.unconfigured = true;
    writeMock.mockClear();
    const userAction = userEvent.setup();
    render(
      <QuickAccessWizard
        open
        user={{ id: 1, loginName: "tester", chineseName: "测试" }}
        projects={[project]}
        projectId="p1"
        onClose={vi.fn()}
        onCompleted={vi.fn()}
        onOpenView={vi.fn()}
      />,
    );
    await screen.findByRole("dialog");
    const keyLink = screen.getByRole("link", { name: /获取 API Key/ });
    expect(keyLink).toHaveAttribute("href", "https://www.codebuddy.ai/profile/keys");
    await userAction.click(screen.getByLabelText("CodeBuddy 网络环境"));
    await userAction.click(screen.getByRole("option", { name: /国内站/ }));
    expect(keyLink).toHaveAttribute("href", "https://copilot.tencent.com/profile/");
    await userAction.type(screen.getByPlaceholderText("粘贴 CodeBuddy API Key"), "sk-deepseek");
    expect(screen.getByText(/第三方模型的 Key/)).toBeInTheDocument();
    await userAction.click(screen.getByRole("button", { name: "下一步" }));
    await screen.findByText("项目与仓库", { selector: "h3" });
    expect(writeMock).toHaveBeenCalledWith(
      "/api/users/1/credentials",
      { codeBuddyApiKey: "sk-deepseek", codeBuddyInternetEnvironment: "internal" },
      "PUT",
    );
    mockState.unconfigured = false;
  });

  test("rewrites an old china environment to internal when moving past the credential step", async () => {
    mockState.users = () => [{ ...user, codeBuddyInternetEnvironment: "china" }];
    writeMock.mockClear();
    const userAction = userEvent.setup();
    try {
      render(
        <QuickAccessWizard
          open
          user={user}
          projects={[project]}
          projectId="p1"
          onClose={vi.fn()}
          onCompleted={vi.fn()}
          onOpenView={vi.fn()}
        />,
      );
      await screen.findByRole("dialog");
      expect(await screen.findByText(/「china」无法识别/)).toBeInTheDocument();
      expect(screen.getByRole("link", { name: /获取 API Key/ })).toHaveAttribute(
        "href",
        "https://copilot.tencent.com/profile/",
      );
      await userAction.click(screen.getByRole("button", { name: "下一步" }));
      await screen.findByText("项目与仓库", { selector: "h3" });
      expect(writeMock).toHaveBeenCalledWith(
        "/api/users/1/credentials",
        { codeBuddyInternetEnvironment: "internal" },
        "PUT",
      );
    } finally {
      mockState.users = null;
    }
  });

  test("a refresh after switching to 国际站 elsewhere drops the omitted environment", async () => {
    let environment = "internal";
    mockState.users = () => [
      { ...user, ...(environment ? { codeBuddyInternetEnvironment: environment } : {}) },
    ];
    writeMock.mockClear();
    const userAction = userEvent.setup();
    try {
      render(
        <QuickAccessWizard
          open
          user={user}
          projects={[project]}
          projectId="p1"
          onClose={vi.fn()}
          onCompleted={vi.fn()}
          onOpenView={vi.fn()}
        />,
      );
      await screen.findByRole("dialog");
      const keyLink = screen.getByRole("link", { name: /获取 API Key/ });
      await waitFor(() =>
        expect(keyLink).toHaveAttribute("href", "https://copilot.tencent.com/profile/"),
      );
      // 个人配置 switched the environment to 国际站; the users API omits it now.
      environment = "";
      window.dispatchEvent(new Event("focus"));
      await waitFor(() =>
        expect(keyLink).toHaveAttribute("href", "https://www.codebuddy.ai/profile/keys"),
      );
      await userAction.type(screen.getByLabelText("CodeBuddy API Key"), "ck_new");
      await userAction.click(screen.getByRole("button", { name: "下一步" }));
      await screen.findByText("项目与仓库", { selector: "h3" });
      expect(writeMock).toHaveBeenCalledWith(
        "/api/users/1/credentials",
        { codeBuddyApiKey: "ck_new", codeBuddyInternetEnvironment: "" },
        "PUT",
      );
    } finally {
      mockState.users = null;
    }
  });

  test("a sponsored model lets new users skip credentials", async () => {
    mockState.unconfigured = true;
    const userAction = userEvent.setup();
    render(
      <QuickAccessWizard
        open
        user={{ id: 1, loginName: "tester", chineseName: "测试" }}
        projects={[project]}
        projectId="p1"
        sponsoredModel="m1"
        onClose={vi.fn()}
        onCompleted={vi.fn()}
        onOpenView={vi.fn()}
      />,
    );
    await screen.findByRole("dialog");
    await userAction.click(screen.getByRole("button", { name: "下一步" }));
    expect(await screen.findByText("项目与仓库", { selector: "h3" })).toBeInTheDocument();
    mockState.unconfigured = false;
  });

  test("creates a project, binds a parsed repository, and previews roles", async () => {
    mockState.failClone = true;
    const userAction = userEvent.setup();
    render(
      <QuickAccessWizard
        open
        user={user}
        projects={[project]}
        projectId="p1"
        onClose={vi.fn()}
        onCompleted={vi.fn()}
        onOpenView={vi.fn()}
      />,
    );
    await screen.findByRole("dialog");
    await userAction.click(screen.getByRole("button", { name: "下一步" }));
    await screen.findByText("项目与仓库", { selector: "h3" });
    await userAction.click(screen.getByRole("tab", { name: /新建项目/ }));
    const name = screen.getByPlaceholderText("例如：智能网关");
    await userAction.clear(name);
    await userAction.type(name, "新项目");
    await userAction.type(
      screen.getByPlaceholderText("https://github.com/owner/repo"),
      "https://github.com/team/repo",
    );
    expect(screen.getByText(/已识别仓库/)).toBeInTheDocument();
    await userAction.click(screen.getByRole("button", { name: "下一步" }));
    await screen.findByText("默认角色与 SOP", { selector: "h3" });
    await userAction.click(screen.getByRole("tab", { name: /默认角色/ }));
    expect(screen.getByText("需求定型")).toBeInTheDocument();
    await userAction.click(screen.getByRole("button", { name: "完成接入" }));
    expect(await screen.findByText("快速接入已完成")).toBeInTheDocument();
    expect(writeMock).toHaveBeenCalledWith(
      "/api/sandboxes",
      expect.objectContaining({ driver: "local" }),
      "PUT",
    );
    expect(writeMock).toHaveBeenCalledWith(
      "/api/repositories",
      expect.objectContaining({
        provider: "github",
        gongfengProjectId: "github:team/repo",
        cloneUrl: "https://github.com/team/repo.git",
        defaultBranch: "main",
      }),
    );
    mockState.failClone = false;
  });
});
