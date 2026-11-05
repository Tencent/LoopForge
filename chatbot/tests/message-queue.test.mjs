import { describe, expect, test } from "vitest";
import {
  QUEUE_STORAGE_KEY,
  enqueue,
  holdQueue,
  nextQueued,
  queuedMessage,
  readQueues,
  removeQueued,
  restoreQueued,
  writeQueues,
} from "../src/message-queue.js";

function memoryStorage() {
  const data = new Map();
  return {
    getItem: (k) => (data.has(k) ? data.get(k) : null),
    setItem: (k, v) => data.set(k, String(v)),
    removeItem: (k) => data.delete(k),
    data,
  };
}

describe("message queue", () => {
  test("keeps messages per conversation in order", () => {
    const a = queuedMessage({ text: " 先看 CI " });
    const b = queuedMessage({ text: "再看日志", attachments: [{ id: "f1" }] });
    let queues = enqueue({}, "s1", a);
    queues = enqueue(queues, "s1", b);
    queues = enqueue(queues, "s2", queuedMessage({ text: "别的会话" }));
    expect(a.text).toBe("先看 CI");
    expect(nextQueued(queues, "s1")).toBe(a);
    queues = removeQueued(queues, "s1", a.id);
    expect(nextQueued(queues, "s1")).toBe(b);
    expect(nextQueued(queues, "s2").text).toBe("别的会话");
    queues = removeQueued(queues, "s1", b.id);
    expect(queues.s1).toBeUndefined();
    expect(nextQueued(queues, "s1")).toBeNull();
  });

  test("holding stops dispatch until resumed, and a failed send goes back to the front", () => {
    const a = queuedMessage({ text: "a" });
    const b = queuedMessage({ text: "b" });
    let queues = enqueue(enqueue({}, "s1", a), "s1", b);
    queues = holdQueue(queues, "s1", true);
    expect(nextQueued(queues, "s1")).toBeNull();
    queues = holdQueue(queues, "s1", false);
    expect(nextQueued(queues, "s1")).toBe(a);
    queues = removeQueued(queues, "s1", a.id);
    queues = restoreQueued(queues, "s1", a);
    expect(queues.s1.items.map((i) => i.text)).toEqual(["a", "b"]);
    expect(queues.s1.held).toBe(true);
    expect(holdQueue({}, "none", true)).toEqual({});
  });

  test("survives a reload but never sends behind the user's back", () => {
    const storage = memoryStorage();
    const queues = enqueue({}, "s1", queuedMessage({ text: "继续" }));
    writeQueues(queues, storage);
    const restored = readQueues(storage);
    expect(restored.s1.items[0].text).toBe("继续");
    expect(restored.s1.held).toBe(true);
    writeQueues({}, storage);
    expect(storage.data.has(QUEUE_STORAGE_KEY)).toBe(false);
    storage.setItem(QUEUE_STORAGE_KEY, "{broken");
    expect(readQueues(storage)).toEqual({});
    storage.setItem(QUEUE_STORAGE_KEY, JSON.stringify({ s1: { items: [{ nope: 1 }] } }));
    expect(readQueues(storage)).toEqual({});
  });
});
