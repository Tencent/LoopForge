import { afterEach, expect, test } from "vitest";
import { localId } from "../src/local-id.js";

afterEach(() => {
  delete globalThis.crypto.randomUUID;
});

test("uses crypto.randomUUID when the page is a secure context", () => {
  Object.defineProperty(globalThis.crypto, "randomUUID", {
    configurable: true,
    value: () => "11111111-2222-3333-4444-555555555555",
  });
  expect(localId("pending")).toBe("pending-11111111-2222-3333-4444-555555555555");
});

test("still yields unique ids over plain HTTP without crypto.randomUUID", () => {
  Object.defineProperty(globalThis.crypto, "randomUUID", {
    configurable: true,
    value: undefined,
  });
  const ids = new Set(Array.from({ length: 200 }, () => localId("pending")));
  expect(ids.size).toBe(200);
  for (const id of ids) expect(id).toMatch(/^pending-[0-9a-z]+-[0-9a-z]+-[0-9a-z]+$/);
});
