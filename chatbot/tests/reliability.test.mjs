import { test } from "vitest";
import assert from "node:assert/strict";
import JSZip from "jszip";
import { createPresentation, presentationPages } from "../src/presentation.js";
import {
  partialReplyMessage,
  mergeEvents,
  restoreStream,
  collectArtifacts,
} from "../src/api.js";
import { conversationRunIDs } from "../src/runs.js";

test("PPT export creates editable OOXML slides with all Chinese page text", async () => {
  const source =
    "# 商城概览\n副标题\n\n---\n\n# 项目背景\n- 精确库存\n- 订单系统\n\n---\n\n# 里程碑\n第一阶段";
  const pptx = await createPresentation(source);
  const buffer = await pptx.write({ outputType: "nodebuffer" });
  const zip = await JSZip.loadAsync(buffer);
  assert.equal(presentationPages(source).length, 3);
  const slides = Object.keys(zip.files).filter((p) =>
    /^ppt\/slides\/slide\d+\.xml$/.test(p),
  );
  assert.equal(slides.length, 3);
  assert.match(
    await zip.file("ppt/slides/slide2.xml").async("string"),
    /精确库存/,
  );
  assert.match(
    await zip.file("ppt/slides/slide3.xml").async("string"),
    /第一阶段/,
  );
  assert.ok(zip.file("[Content_Types].xml"));
  assert.equal(
    collectArtifacts({
      messages: [
        {
          role: "assistant",
          content: JSON.stringify({
            message: "```slides\n" + source + "\n```",
          }),
        },
      ],
    })[0].name.endsWith(".pptx"),
    true,
  );
});
test("snapshot plus future tokens restores all output, including partial draft text", () => {
  const state = restoreStream({
    sequence: 900,
    answer: "旧内容",
    activity: [{ kind: "tool", id: "one", tool: "Read", detail: "README.md" }],
    draftJSON: '{"message":"第一',
    draftMessage: "第一",
  });
  const merged = mergeEvents(state, [
    { sequence: 901, data: { kind: "draftDelta", detail: '行\\n第二行"}' } },
  ]);
  assert.equal(merged.answer, "第一行\n第二行");
  assert.equal(merged.tools.length, 1);
  assert.equal(partialReplyMessage('{"message":"你好\\u4f'), "你好");
});
test("only explicit conversation runs are considered; unconfirmed drafts do not start a rail", () => {
  const session = {
    runId: "run-1",
    messages: [
      {
        role: "assistant",
        runIds: ["run-2"],
        content: JSON.stringify({
          message: "本次运行 `run-3`",
          drafts: [
            {
              tool: "runs_start",
              saved: false,
              savedResult: { id: "run-ignored" },
            },
            {
              tool: "issues_dispatch",
              saved: true,
              savedResult: { runId: "run-4" },
            },
          ],
        }),
      },
    ],
  };
  assert.deepEqual(conversationRunIDs(session), [
    "run-1",
    "run-2",
    "run-4",
    "run-3",
  ]);
  assert.deepEqual(
    conversationRunIDs({
      messages: [
        { role: "assistant", content: '{"message":"生成了 PPT","drafts":[]}' },
      ],
    }),
    [],
  );
});
