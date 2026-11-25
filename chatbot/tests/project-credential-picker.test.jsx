import React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import ProjectCredentialPicker from "../src/ProjectCredentialPicker";

afterEach(() => vi.unstubAllGlobals());

test("a session owner can clear bindings when the project credential list is forbidden", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
    ok: false,
    status: 403,
    json: async () => ({ error: "无权访问项目凭据" }),
  }));
  const selectProjectCredentials = vi.fn();
  render(<ProjectCredentialPicker chat={{
    activeId: "session", projectId: "project", projectCredentialIds: ["registry"], selectProjectCredentials,
  }} />);
  const trigger = screen.getByRole("button", { name: /^项目凭据/ });
  trigger.getBoundingClientRect = () => ({ top: 500, bottom: 528, left: 200, right: 450, width: 250, height: 28 });
  const user = userEvent.setup();
  await user.click(trigger);
  expect(await screen.findByText("无权访问项目凭据")).toBeInTheDocument();
  await user.click(screen.getByRole("option", { name: "清除绑定" }));
  expect(selectProjectCredentials).toHaveBeenCalledWith([]);
});
