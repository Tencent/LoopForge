import { test } from "vitest";
import assert from "node:assert/strict";
import { effortOptions, modelEffort } from "../src/effort.js";

test("effort choices follow the current model catalog and keep the native default", () => {
  assert.deepEqual(effortOptions({ efforts: ["low", "high", "max"] }).map((v) => v.id), ["", "low", "high", "max"]);
  assert.deepEqual(effortOptions({}).map((v) => v.id), [""]);
  assert.equal(modelEffort({ efforts: ["high", "max"] }, "max"), "max");
  assert.equal(modelEffort({ efforts: ["high"] }, "max"), "");
  assert.equal(modelEffort(undefined, "high"), "");
});

test("effort levels none, minimal and ultra are offered when the model lists them", () => {
  const options = effortOptions({ efforts: ["none", "minimal", "low", "ultra", "bogus"] });
  assert.deepEqual(options.map((v) => v.id), ["", "none", "minimal", "low", "ultra"]);
  for (const option of options) {
    assert.ok(option.label && option.icon && option.detail, `effort ${option.id} has display metadata`);
  }
  assert.equal(modelEffort({ efforts: ["none", "ultra"] }, "ultra"), "ultra");
});

test("models with a thinking switch show default, off and on", () => {
  const model = { efforts: ["none", "thinking"] };
  assert.deepEqual(effortOptions(model).map(({ id, label }) => [id, label]), [
    ["", "默认"],
    ["none", "关闭"],
    ["thinking", "开启"],
  ]);
  assert.equal(modelEffort(model, "thinking"), "thinking");
});

test("custom model effort metadata can expose ultracode or only the native default", () => {
  assert.deepEqual(effortOptions({ efforts: ["high", "ultracode"] }).map(({ id }) => id), ["", "high", "ultracode"]);
  assert.deepEqual(effortOptions({ efforts: [] }).map(({ id }) => id), [""]);
});
