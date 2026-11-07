import { test } from "vitest";
import assert from "node:assert/strict";
import { canChatInProject } from "../src/api.js";

test("only owners, members and platform admins chat in a project", () => {
  const user = { id: 7 };
  const project = { id: "p", ownerId: 1, members: [{ userId: 2 }] };
  assert.equal(canChatInProject(null, user), true, "personal chat");
  assert.equal(canChatInProject(project, user), false, "new user");
  assert.equal(canChatInProject(project, null), false, "not loaded");
  assert.equal(canChatInProject({ ...project, ownerId: 7 }, user), true);
  assert.equal(
    canChatInProject({ ...project, members: [{ userId: 7 }] }, user),
    true,
  );
  assert.equal(
    canChatInProject(project, { ...user, isPlatformAdmin: true }),
    true,
  );
  assert.equal(
    canChatInProject(project, { ...user, platformRole: "admin" }),
    true,
  );
  assert.equal(
    canChatInProject({ id: "open" }, user),
    true,
    "a project without owner or members is open",
  );
});
