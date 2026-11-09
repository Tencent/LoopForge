import { test } from "vitest";
import assert from "node:assert/strict";
import { projectPickerOptions, UNJOINED_PROJECTS_SECTION } from "../src/api.js";

const user = { id: 7 };
const joined = (id) => ({ id, name: id.toUpperCase(), ownerId: 7 });
const unjoined = (id) => ({ id, name: id.toUpperCase(), ownerId: 1 });

test("available projects come first, unjoined ones last, each in API order", () => {
  const options = projectPickerOptions(
    [joined("a"), unjoined("b"), joined("c"), unjoined("d")],
    user,
    null,
  );
  assert.deepEqual(
    options.map((o) => o.id),
    ["", "a", "c", "b", "d"],
  );
  assert.equal(options[0].label, "个人对话");
});

test("unjoined projects carry the section and no description", () => {
  const options = projectPickerOptions(
    [joined("a"), unjoined("b")],
    user,
    null,
  );
  const b = options.find((o) => o.id === "b");
  assert.equal(b.section, UNJOINED_PROJECTS_SECTION);
  assert.equal(b.section, "未加入的项目");
  assert.equal(b.unavailable, true);
  assert.equal(b.description, undefined);
  assert.equal(b.label, "B");
  const a = options.find((o) => o.id === "a");
  assert.equal(a.section, undefined);
  assert.equal(a.unavailable, undefined);
});

test("platform admins see no unjoined section", () => {
  const options = projectPickerOptions(
    [unjoined("a"), unjoined("b")],
    { ...user, isPlatformAdmin: true },
    null,
  );
  assert.deepEqual(
    options.map((o) => o.id),
    ["", "a", "b"],
  );
  assert.ok(options.every((o) => !o.section && !o.unavailable));
});

test("no projects leaves only personal chat", () => {
  assert.deepEqual(
    projectPickerOptions([], user, null).map((o) => o.id),
    [""],
  );
  assert.deepEqual(
    projectPickerOptions(undefined, user, null).map((o) => o.id),
    [""],
  );
});

test("all unjoined, or user not loaded, puts every project in the section", () => {
  for (const u of [user, null]) {
    const options = projectPickerOptions(
      [unjoined("a"), unjoined("b")],
      u,
      null,
    );
    assert.deepEqual(
      options.map((o) => o.id),
      ["", "a", "b"],
    );
    assert.ok(
      options.slice(1).every((o) => o.section === UNJOINED_PROJECTS_SECTION),
    );
  }
});

test("available projects offer binding an unbound session", () => {
  const projects = [joined("a"), unjoined("b")];
  const unbound = projectPickerOptions(projects, user, { id: "s" });
  assert.equal(
    unbound.find((o) => o.id === "a").description,
    "绑定当前会话到此项目",
  );
  assert.equal(unbound.find((o) => o.id === "b").description, undefined);
  const bound = projectPickerOptions(projects, user, {
    id: "s",
    projectId: "a",
  });
  assert.equal(bound.find((o) => o.id === "a").description, undefined);
});

test("a bound or shared session says another project opens a new chat", () => {
  const projects = [joined("a"), joined("c")];
  const bound = projectPickerOptions(projects, user, { id: "s", projectId: "a" });
  assert.equal(bound.find((o) => o.id === "c").description, "在此项目新开对话");
  const shared = projectPickerOptions(projects, user, { id: "s", shared: true });
  assert.equal(shared.find((o) => o.id === "a").description, "在此项目新开对话");
  const draft = projectPickerOptions(projects, user, null);
  assert.equal(draft.find((o) => o.id === "c").description, undefined);
});
