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
