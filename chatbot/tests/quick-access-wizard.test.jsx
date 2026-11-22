import React from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

const { project, user, apiMock, writeMock, mockState } = vi.hoisted(() => {
  const project = {
    id: "p1", key: "P1", name: "项目一", ownerId: 1, members: [1],
    workflowId: "generic-backend-delivery",
  };
  const user = {
    id: 1, loginName: "tester", chineseName: "测试",
    gitHubCredentialConfigured: true, codeBuddyApiKeyConfigured: true,
  };
  const apiMock = vi.fn(async (path) => {
  if (path === "/api/users")
    return [mockState.unconfigured ? {
      id: 1, loginName: "tester", chineseName: "测试",
      gitHubCredentialConfigured: false, codeBuddyApiKeyConfigured: false,
      ...mockState.savedCredentials,
    } : user];
  if (path === "/api/projects") return [project];
  if (path === "/api/repositories") return [];
  if (path === "/api/users/1/git-ssh-keys") return mockState.sshKeys || [];
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
  return [];
  });
  const mockState = { failClone: false, unconfigured: false, sshKeys: null, savedCredentials: {}, gitVerification: null };
  const writeMock = vi.fn(async (path, body) => {
    if (path === "/api/users/1/git-token/verify") return mockState.gitVerification;
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
    expect(parseGongfengRepo("acme/widgets")).toBeNull();
    expect(parseGongfengRepo("https://git.review.test:8443/acme/widgets", "git.review.test:8443")).toMatchObject({
      provider: "gongfeng", project: "acme/widgets", cloneUrl: "https://git.review.test:8443/acme/widgets.git",
    });
    expect(parseGongfengRepo("https://github.com/acme/widgets")).toEqual({
      project: "github:acme/widgets",
      provider: "github",
      name: "widgets",
      webUrl: "https://github.com/acme/widgets",
      cloneUrl: "https://github.com/acme/widgets.git",
    });
    // Preserve explicit SSH transport for GitHub as for other hosts.
    for (const address of ["git@github.com:acme/widgets.git", "ssh://git@github.com/acme/widgets.git"]) {
      expect(parseGongfengRepo(address)).toMatchObject({ provider: "ssh", host: "github.com:22" });
    }
    expect(parseGongfengRepo("ssh://git@github.com:2222/acme/widgets.git")?.provider).toBe("ssh");
    expect(parseGongfengRepo("git clone ssh://git@gitlab.example.org:2222/team/sub/app.git")).toMatchObject({
      provider: "ssh", name: "app", cloneUrl: "ssh://git@gitlab.example.org:2222/team/sub/app.git",
    });
    expect(parseGongfengRepo("github:acme/widgets")?.cloneUrl).toBe("https://github.com/acme/widgets.git");
    expect(parseGongfengRepo("git clone https://github.com/acme/widgets.git")?.name).toBe("widgets");
    expect(parseGongfengRepo("https://token@github.com/acme/widgets")).toBeNull();
    expect(parseGongfengRepo("https://github.com/acme")).toBeNull();
    expect(parseGongfengRepo("git@git.example.com:team/repo.git")?.provider).toBe("ssh");
    expect(parseGongfengRepo("https://git.example.com/team/repo/-/tree/main")?.name).toBe("repo");
    expect(parseGongfengRepo("https://example.com/team/repo")).toMatchObject({provider:"https",cloneUrl:"https://example.com/team/repo"});
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

  test("warns only when the selected SSH host has no personal key and refreshes after settings changes", async () => {
    mockState.sshKeys = [{ id: "gitlab", name: "GitLab", host: "gitlab.example.org:22" }];
    const userAction = userEvent.setup();
    try {
      render(<QuickAccessWizard open user={{ ...user, gitSshKeyConfigured: true }} projects={[project]} projectId="p1" sponsoredModel="m1" onClose={vi.fn()} onCompleted={vi.fn()} onOpenView={vi.fn()} />);
      await screen.findByRole("dialog");
      await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/api/users/1/git-ssh-keys"));
      await userAction.click(screen.getByRole("button", { name: "下一步" }));
      await screen.findByText("项目与仓库", { selector: "h3" });
      const address = screen.getByPlaceholderText(/https:\/\/github.com\/owner\/repo/);
      await userAction.type(address, "git@gitlab.example.org:team/repo.git");
      expect(screen.queryByText(/尚未配置 .* 的 SSH 密钥/)).toBeNull();
      expect(screen.queryByRole("button", { name: "配置此仓库的 SSH 密钥" })).toBeNull();
      await userAction.clear(address);
      await userAction.type(address, "ssh://git@gitlab.example.org:2222/team/repo.git");
      expect(screen.getByRole("alert")).toHaveTextContent("尚未配置 gitlab.example.org:2222 的 SSH 密钥");
      expect(screen.getByRole("alert").querySelector("button")).toBeNull();
      mockState.sshKeys = [...mockState.sshKeys, { id: "port", name: "Other SSH port", host: "gitlab.example.org:2222" }];
      window.dispatchEvent(new MessageEvent("message", { origin: location.origin, source: window.parent, data: { type: "agentflow-credentials-changed" } }));
      await waitFor(() => expect(screen.queryByText(/尚未配置 .* 的 SSH 密钥/)).toBeNull());
      expect(address).toHaveValue("ssh://git@gitlab.example.org:2222/team/repo.git");
      await userAction.clear(address);
      await userAction.type(address, "git@gitea.example.org:team/repo.git");
      expect(screen.getByRole("alert")).toHaveTextContent("尚未配置 gitea.example.org:22 的 SSH 密钥");
      await userAction.clear(address);
      await userAction.type(address, "https://github.com/team/repo");
      expect(screen.queryByText(/尚未配置 .* 的 SSH 密钥/)).toBeNull();
      await userAction.clear(address);
      await userAction.type(address, "git@gitea.example.org:team/repo.git");
      expect(screen.getByRole("alert")).toHaveTextContent("可稍后");
      await userAction.click(screen.getByRole("button", { name: "下一步" }));
      await screen.findByText("默认角色与 SOP", { selector: "h3" });
      await userAction.click(screen.getByRole("button", { name: "完成接入" }));
      expect(await screen.findByText("快速接入已完成")).toBeInTheDocument();
      expect(writeMock).toHaveBeenCalledWith("/api/repositories", expect.objectContaining({ provider: "ssh", cloneUrl: "git@gitea.example.org:team/repo.git" }));
    } finally {
      mockState.sshKeys = null;
    }
  });

  test("keeps the wizard open when configuring an executor and refreshes it on return", async () => {
    mockState.unconfigured = true;
    const userAction = userEvent.setup();
    const onClose = vi.fn();
    const parentFrame = document.createElement("iframe");
    document.body.append(parentFrame);
    const parent = parentFrame.contentWindow;
    const post = vi.spyOn(parent, "postMessage").mockImplementation(() => {});
    const parentWindow = vi.spyOn(window, "parent", "get").mockReturnValue(parent);
    try {
      render(
        <QuickAccessWizard
          open
          user={{ id: 1, loginName: "tester" }}
          projects={[project]}
          projectId="p1"
          onClose={onClose}
        />,
      );
      expect(screen.getByRole("heading", { name: "凭据与授权", level: 3 })).toBeVisible();
      expect(screen.getByText("第 1 步，共 3 步")).toBeVisible();
      expect(screen.getByText("配置任意一项即可继续。下一步选择项目和仓库。")).toBeVisible();
      const next = screen.getByRole("button", { name: "下一步" });
      await waitFor(() => expect(next).toBeEnabled());
      await userAction.click(next);
      expect(screen.getByRole("alert")).toHaveTextContent("请先添加一个 Git 凭据或执行器。");

      await userAction.click(screen.getByRole("button", { name: "执行器配置", exact: true }));
      expect(post).toHaveBeenCalledWith(
        { type: "agentflow-chat-open-view", view: "settings", credential: "executors", returnToQuickAccess: true },
        location.origin,
      );
      expect(onClose).not.toHaveBeenCalled();
      mockState.savedCredentials = { codeBuddyApiKeyConfigured: true };
      const data = { type: "agentflow-credentials-changed" };
      await act(async () => {
        window.dispatchEvent(new MessageEvent("message", { data, origin: location.origin, source: window }));
        window.dispatchEvent(new MessageEvent("message", { data, origin: "https://other.example.org", source: parent }));
      });
      expect(screen.getByText("尚未配置执行器")).toBeVisible();

      await act(async () => {
        window.dispatchEvent(new MessageEvent("message", { data, origin: location.origin, source: parent }));
        window.dispatchEvent(new MessageEvent("message", { data: { type: "agentflow-chat-resume-quick-access" }, origin: location.origin, source: parent }));
      });
      expect(screen.getByRole("heading", { name: "凭据与授权", level: 3 })).toHaveFocus();
      expect(screen.getByText(/可以继续选择项目和仓库/)).toBeVisible();
      await userAction.click(next);
      expect(await screen.findByRole("heading", { name: "项目与仓库", level: 3 })).toBeInTheDocument();
      expect(onClose).not.toHaveBeenCalled();
    } finally {
      parentWindow.mockRestore();
      post.mockRestore();
      parentFrame.remove();
      mockState.unconfigured = false;
      mockState.savedCredentials = {};
    }
  });

  test("validates the optional GitHub token before advancing or saving it", async () => {
    mockState.unconfigured = true;
    const actions = userEvent.setup();
    try {
      render(<QuickAccessWizard open user={{ id: 1, loginName: "tester" }} projects={[project]} projectId="p1" />);
      await actions.click(screen.getByText("使用访问令牌（高级）"));
      await actions.type(screen.getByLabelText("GitHub 个人访问令牌"), "token-value");
      mockState.gitVerification = { valid: false, error: "测试校验拒绝" };
      const next = screen.getByRole("button", { name: "下一步" });
      await actions.click(next);
      expect(await screen.findByText("测试校验拒绝")).toBeVisible();
      expect(writeMock.mock.calls.some(([path]) => path === "/api/users/1/git-http-credentials")).toBe(false);
      mockState.gitVerification = { valid: true };
      await actions.click(next);
      expect(await screen.findByRole("heading", { name: "项目与仓库", level: 3 })).toBeVisible();
      expect(writeMock).toHaveBeenCalledWith(
        "/api/users/1/git-http-credentials", expect.objectContaining({ host: "github.com", token: "token-value" }), "PUT",
      );
    } finally {
      mockState.unconfigured = false;
      mockState.gitVerification = null;
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
    expect(screen.getByText(/平台共享模型可用，可直接继续/)).toBeVisible();
    expect(screen.getByText("可使用平台共享模型")).toBeVisible();
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

describe("QuickAccessWizard repository permission (#122)", () => {
  const welcome = {
    id: "welcome", key: "WELCOME", name: "LoopForge 体验项目", ownerId: 99,
    members: [{ userId: 1, role: "member" }], workflowId: "generic-backend-delivery",
  };
  const member = { ...user, isPlatformAdmin: false };
  const renderWizard = () =>
    render(
      <QuickAccessWizard
        open
        user={member}
        projects={[welcome]}
        projectId="welcome"
        sponsoredModel="m1"
        onClose={vi.fn()}
        onCompleted={vi.fn()}
        onOpenView={vi.fn()}
      />,
    );
  const openProjectStep = async (userAction) => {
    await screen.findByRole("dialog");
    await userAction.click(screen.getByRole("button", { name: "下一步" }));
    await screen.findByText("项目与仓库", { selector: "h3" });
  };

  test("explains on the project step that a member cannot bind the shared project", async () => {
    apiMock.mockImplementation(async (path) => {
      if (path === "/api/projects") return [welcome];
      if (path === "/api/users") return [member];
      if (path === "/api/workflows") return [{ id: "generic-backend-delivery", name: "通用后端交付 SOP", generic: true, nodes: [] }];
      return [];
    });
    writeMock.mockClear();
    const userAction = userEvent.setup();
    renderWizard();
    await openProjectStep(userAction);
    expect(screen.getByRole("note")).toHaveTextContent("只有它的 Owner 或平台管理员可以绑定仓库");
    const input = screen.getByPlaceholderText("https://github.com/owner/repo");
    await userAction.type(input, "https://github.com/team/repo");
    await userAction.click(screen.getByRole("button", { name: "下一步" }));
    // Blocked here, not on the last step, and nothing was written.
    expect(await screen.findByText(/不能为它绑定仓库/)).toBeInTheDocument();
    expect(screen.queryByText("默认角色与 SOP", { selector: "h3" })).toBeNull();
    expect(writeMock).not.toHaveBeenCalledWith("/api/repositories", expect.anything());
    expect(writeMock).not.toHaveBeenCalledWith("/api/repositories", expect.anything(), expect.anything());

    // "Create my own project" keeps the typed repository.
    await userAction.click(screen.getByRole("button", { name: "新建自己的项目并绑定" }));
    expect(screen.getByRole("tab", { name: /新建项目/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByPlaceholderText("https://github.com/owner/repo")).toHaveValue("https://github.com/team/repo");
    expect(screen.queryByRole("note")).toBeNull();
    apiMock.mockReset();
  });

  test("skipping the binding continues with the shared project and writes no repository", async () => {
    apiMock.mockImplementation(async (path) => {
      if (path === "/api/projects") return [welcome];
      if (path === "/api/users") return [member];
      if (path === "/api/workflows") return [{ id: "generic-backend-delivery", name: "通用后端交付 SOP", generic: true, nodes: [] }];
      return [];
    });
    writeMock.mockClear();
    const userAction = userEvent.setup();
    renderWizard();
    await openProjectStep(userAction);
    await userAction.type(screen.getByPlaceholderText("https://github.com/owner/repo"), "https://github.com/team/repo");
    await userAction.click(screen.getByRole("button", { name: "跳过绑定，继续体验" }));
    await userAction.click(screen.getByRole("button", { name: "下一步" }));
    await screen.findByText("默认角色与 SOP", { selector: "h3" });
    expect(screen.getByText(/使用已有项目「LoopForge 体验项目」/)).toBeInTheDocument();
    expect(screen.queryByText(/绑定 repo/)).toBeNull();
    await userAction.click(screen.getByRole("button", { name: "完成接入" }));
    expect(await screen.findByText("快速接入已完成")).toBeInTheDocument();
    expect(writeMock.mock.calls.some(([path]) => path === "/api/repositories")).toBe(false);
    apiMock.mockReset();
  });
});
