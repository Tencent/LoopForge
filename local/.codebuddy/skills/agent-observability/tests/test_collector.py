from __future__ import annotations

import json
import tempfile
import time
import unittest
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "scripts"
if str(SCRIPTS) not in sys.path:
    sys.path.insert(0, str(SCRIPTS))

from core import agent_identity, agentlens, collector, emitter, runtime, state as st  # type: ignore


class CollectorTests(unittest.TestCase):
    def test_cost_of_uses_known_model_price_table(self):
        tokens = {"input": 1000, "output": 200, "cache_read": 0, "total": 1200}
        cost = emitter.cost_of(tokens, "gpt-4o")
        self.assertIsNotNone(cost)
        self.assertGreaterEqual(cost, 0.0)

    def test_cost_of_returns_none_for_unknown_model(self):
        tokens = {"input": 1000, "output": 200, "cache_read": 0, "total": 1200}
        self.assertIsNone(emitter.cost_of(tokens, "unknown-model"))

    def test_emit_adds_codebuddy_cli_data_source(self):
        with tempfile.TemporaryDirectory() as td:
            log_path = Path(td) / "metrics.ndjson"

            emitter.emit(log_path, {"event": "start", "sid": "s-data-source"})

            record = json.loads(log_path.read_text(encoding="utf-8").strip())
            self.assertEqual(record["data_source"], "codebuddy-cli")

    def test_emit_preserves_explicit_data_source(self):
        with tempfile.TemporaryDirectory() as td:
            log_path = Path(td) / "metrics.ndjson"

            emitter.emit(log_path, {"event": "start", "sid": "s-data-source", "data_source": "manual"})

            record = json.loads(log_path.read_text(encoding="utf-8").strip())
            self.assertEqual(record["data_source"], "manual")

    def test_emit_session_duration_uses_first_log_ts_when_state_was_recreated(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            log_path = root / "metrics.ndjson"
            state_path = root / ".state.json"
            log_path.write_text(
                json.dumps({"event": "start", "sid": "s-duration", "ts": 100.0}) + "\n",
                encoding="utf-8",
            )
            state_path.write_text(
                json.dumps({"s-duration": {"started_at": 190.0}}, ensure_ascii=False),
                encoding="utf-8",
            )

            emitter.emit(log_path, {"event": "usage", "sid": "s-duration", "ts": 200.0, "tokens": {"input": 1, "output": 2}})

            records = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
            self.assertEqual(records[-1]["session_duration_sec"], 100.0)

    def test_emit_session_duration_starts_at_zero_without_prior_log_or_state(self):
        with tempfile.TemporaryDirectory() as td:
            log_path = Path(td) / "metrics.ndjson"

            emitter.emit(log_path, {"event": "start", "sid": "s-new", "ts": 100.0})

            record = json.loads(log_path.read_text(encoding="utf-8").strip())
            self.assertEqual(record["session_duration_sec"], 0.0)

    def test_parse_transcript_tail_extracts_tokens(self):
        transcript = ROOT / "tests" / "fixtures" / "transcript.jsonl"
        out = collector.parse_transcript_tail(str(transcript), max_lines=20)
        self.assertIsInstance(out, dict)
        self.assertIsInstance(out.get("tokens"), dict)
        self.assertEqual(out["tokens"].get("input_tokens"), 2240)
        self.assertEqual(out["tokens"].get("output_tokens"), 120)
        usage_records = out.get("usage_records") or []
        tool_records = out.get("tool_records") or []
        self.assertEqual(len(usage_records), 3)
        self.assertEqual(usage_records[0]["tokens"].get("input_tokens"), 1820)
        self.assertEqual(usage_records[-1]["tokens"].get("input_tokens"), 2240)
        self.assertEqual(usage_records[0].get("message_id"), "msg-001")
        self.assertEqual(usage_records[-1].get("message_id"), "msg-003")
        self.assertLess(usage_records[0]["offset"], usage_records[-1]["offset"])
        self.assertEqual(len(tool_records), 8)
        self.assertEqual(tool_records[0]["tool"], "read_file")
        self.assertEqual(tool_records[0]["message_id"], "msg-001")
        self.assertEqual(tool_records[0]["next_message_id"], "msg-002")
        self.assertEqual(tool_records[-1]["tool"], "Bash")
        self.assertEqual(tool_records[-1]["message_id"], "msg-002")
        self.assertEqual(tool_records[-1]["next_message_id"], "msg-003")

    def test_realtime_usage_is_request_level_and_dedupes_across_phase(self):
        transcript = ROOT / "tests" / "fixtures" / "transcript.jsonl"
        with tempfile.TemporaryDirectory() as td:
            state_path = Path(td) / ".state.json"
            sid = "s-transcript"

            scan = collector.collect_transcript_entries(
                state_path=state_path,
                sid=sid,
                transcript_path=str(transcript),
                max_lines=20,
            )
            entries = scan.get("entries") or []

            self.assertEqual(len(entries), 3)
            self.assertEqual(entries[0]["tokens"]["input"], 1820)
            self.assertGreater(entries[0]["offset"], 0)
            self.assertEqual(entries[0]["message_id"], "msg-001")
            self.assertEqual(entries[1]["tokens"]["input"], 2100)
            self.assertEqual(entries[1]["tokens"]["total"], 2158)
            self.assertEqual(len(scan.get("tool_records") or []), 8)

            usage_key = collector.build_transcript_event_key(
                kind="usage",
                tool="Read",
                entry=entries[0],
            )
            stop_key = collector.build_transcript_event_key(
                kind="stop",
                tool="__stop__",
                entry=entries[0],
            )
            other_tool_key = collector.build_transcript_event_key(
                kind="usage",
                tool="Bash",
                entry=entries[0],
            )

            self.assertEqual(usage_key, stop_key)
            self.assertEqual(usage_key, other_tool_key)
            self.assertTrue(st.claim_transcript_event(state_path, sid, usage_key, str(transcript)))
            self.assertFalse(st.claim_transcript_event(state_path, sid, stop_key, str(transcript)))

    def test_find_current_tool_message_id_prefers_transcript_tool_records(self):
        transcript = ROOT / "tests" / "fixtures" / "transcript.jsonl"
        with tempfile.TemporaryDirectory() as td:
            state_path = Path(td) / ".state.json"
            sid = "s-tool-mid"

            read_mid = collector.find_current_tool_message_id(
                state_path,
                sid,
                str(transcript),
                "Read",
            )
            bash_mid = collector.find_current_tool_message_id(
                state_path,
                sid,
                str(transcript),
                "Bash",
            )

            self.assertEqual(read_mid, "msg-002")
            self.assertEqual(bash_mid, "msg-003")

    def test_resolve_subagent_transcript_alias_and_tool_context(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            sid = "session-123"
            main = root / f"{sid}.jsonl"
            bundle = root / sid
            subagents = bundle / "subagents"
            subagents.mkdir(parents=True)
            alias_session_id = "subagent-session-1"
            subagent = subagents / "agent-a.jsonl"
            main.write_text("", encoding="utf-8")
            subagent.write_text(
                "\n".join(
                    [
                        json.dumps(
                            {
                                "type": "message",
                                "sessionId": alias_session_id,
                                "providerData": {"agent": "Explore"},
                            }
                        ),
                        json.dumps(
                            {
                                "type": "function_call",
                                "sessionId": alias_session_id,
                                "name": "Grep",
                                "callId": "call-1",
                                "providerData": {
                                    "agent": "Explore",
                                    "messageId": "msg-sub-001",
                                },
                            }
                        ),
                    ]
                )
                + "\n",
                encoding="utf-8",
            )

            aliased_path = str(root / f"{alias_session_id}.jsonl")
            resolved = collector.resolve_transcript_path_alias(sid, aliased_path)
            self.assertEqual(resolved, str(subagent.resolve()))

            state_path = root / ".state.json"
            context = collector.find_current_tool_context(
                state_path=state_path,
                sid=sid,
                transcript_path=aliased_path,
                tool_name="Grep",
            )
            self.assertEqual(
                context,
                {
                    "message_id": "msg-sub-001",
                    "transcript_path": str(subagent.resolve()),
                    "agent": "explore",
                    "tool_details": {"call_id": "call-1"},
                },
            )

    def test_find_current_tool_context_prefers_nearest_timestamp(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            transcript = root / "session.jsonl"
            transcript.write_text(
                "\n".join(
                    [
                        json.dumps(
                            {
                                "timestamp": 1000,
                                "type": "function_call",
                                "name": "Bash",
                                "callId": "call-1",
                                "providerData": {"messageId": "msg-early"},
                            }
                        ),
                        json.dumps(
                            {
                                "timestamp": 5000,
                                "type": "function_call",
                                "name": "Bash",
                                "callId": "call-2",
                                "providerData": {"messageId": "msg-late"},
                            }
                        ),
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            state_path = root / ".state.json"

            early = collector.find_current_tool_context(
                state_path=state_path,
                sid="s-nearest",
                transcript_path=str(transcript),
                tool_name="Bash",
                event_ts=1.2,
            )
            late = collector.find_current_tool_context(
                state_path=state_path,
                sid="s-nearest",
                transcript_path=str(transcript),
                tool_name="Bash",
                event_ts=4.9,
            )

            self.assertEqual(early["message_id"], "msg-early")
            self.assertEqual(late["message_id"], "msg-late")

    def test_find_current_tool_context_claims_distinct_call_ids_with_same_timestamp(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            transcript = root / "session.jsonl"
            transcript.write_text(
                "\n".join(
                    [
                        json.dumps(
                            {
                                "timestamp": 5000,
                                "type": "function_call",
                                "name": "Bash",
                                "callId": "call-1",
                                "arguments": "{\"command\":\"echo one\"}",
                                "providerData": {"messageId": "msg-shared"},
                            }
                        ),
                        json.dumps(
                            {
                                "timestamp": 5000,
                                "type": "function_call",
                                "name": "Bash",
                                "callId": "call-2",
                                "arguments": "{\"command\":\"echo two\"}",
                                "providerData": {"messageId": "msg-shared"},
                            }
                        ),
                        json.dumps(
                            {
                                "timestamp": 5000,
                                "type": "function_call_result",
                                "name": "Bash",
                                "callId": "call-1",
                                "providerData": {
                                    "messageId": "msg-shared",
                                    "toolResult": {"content": "result one"},
                                },
                            }
                        ),
                        json.dumps(
                            {
                                "timestamp": 5000,
                                "type": "function_call_result",
                                "name": "Bash",
                                "callId": "call-2",
                                "providerData": {
                                    "messageId": "msg-shared",
                                    "toolResult": {"content": "result two"},
                                },
                            }
                        ),
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            state_path = root / ".state.json"

            first = collector.find_current_tool_context(
                state_path=state_path,
                sid="s-claim-shared",
                transcript_path=str(transcript),
                tool_name="Bash",
                event_ts=4.9,
                claim=True,
            )
            second = collector.find_current_tool_context(
                state_path=state_path,
                sid="s-claim-shared",
                transcript_path=str(transcript),
                tool_name="Bash",
                event_ts=4.9,
                claim=True,
            )
            third = collector.find_current_tool_context(
                state_path=state_path,
                sid="s-claim-shared",
                transcript_path=str(transcript),
                tool_name="Bash",
                event_ts=4.9,
                claim=True,
            )

            self.assertEqual(first["message_id"], "msg-shared")
            self.assertEqual(second["message_id"], "msg-shared")
            self.assertNotEqual(first["tool_details"]["call_id"], second["tool_details"]["call_id"])
            self.assertIn("arguments", first["tool_details"])
            self.assertIn("result_content", first["tool_details"])
            self.assertEqual(third, {"duplicate": True})

    def test_find_fallback_usage_event_prefers_latest_matching_usage(self):
        tool_event = {
            "tool": "Bash",
            "agent": "main",
            "transcript_path": "/tmp/demo.jsonl",
        }
        usage_events = [
            {
                "agent": "main",
                "tool": "Read",
                "transcript_path": "/tmp/demo.jsonl",
                "message_id": "msg-old",
                "source_offset": 100,
            },
            {
                "agent": "main",
                "tool": "Bash",
                "transcript_path": "/tmp/demo.jsonl",
                "message_id": "msg-current",
                "source_offset": 200,
            },
        ]

        matched = collector.find_fallback_usage_event(tool_event, usage_events)
        self.assertIsNotNone(matched)
        self.assertEqual(matched["message_id"], "msg-current")

    def test_find_current_tool_context_prefers_explicit_call_id_over_nearest_timestamp(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            transcript = root / "session.jsonl"
            transcript.write_text(
                "\n".join(
                    [
                        json.dumps(
                            {
                                "timestamp": 1000,
                                "type": "function_call",
                                "name": "Bash",
                                "callId": "call-early",
                                "providerData": {"messageId": "msg-early"},
                            }
                        ),
                        json.dumps(
                            {
                                "timestamp": 1001,
                                "type": "function_call_result",
                                "name": "Bash",
                                "callId": "call-early",
                                "providerData": {
                                    "messageId": "msg-early",
                                    "toolResult": {"content": "early result"},
                                },
                            }
                        ),
                        json.dumps(
                            {
                                "timestamp": 5000,
                                "type": "function_call",
                                "name": "Bash",
                                "callId": "call-late",
                                "providerData": {"messageId": "msg-late"},
                            }
                        ),
                        json.dumps(
                            {
                                "timestamp": 5001,
                                "type": "function_call_result",
                                "name": "Bash",
                                "callId": "call-late",
                                "providerData": {
                                    "messageId": "msg-late",
                                    "toolResult": {"content": "late result"},
                                },
                            }
                        ),
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            state_path = root / ".state.json"

            context = collector.find_current_tool_context(
                state_path=state_path,
                sid="s-call-id-priority",
                transcript_path=str(transcript),
                tool_name="Bash",
                call_id="call-early",
                event_ts=4.9,
            )

            self.assertEqual(context["message_id"], "msg-early")
            self.assertEqual(context["tool_details"]["call_id"], "call-early")
            self.assertEqual(context["tool_details"]["result_content"], "early result")

    def test_find_current_tool_context_uses_next_message_id_when_available(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            transcript = root / "session.jsonl"
            transcript.write_text(
                "\n".join(
                    [
                        json.dumps(
                            {
                                "timestamp": 1000,
                                "type": "function_call",
                                "name": "Read",
                                "callId": "call-1",
                                "providerData": {"messageId": "msg-tool"},
                            }
                        ),
                        json.dumps(
                            {
                                "timestamp": 1001,
                                "type": "function_call_result",
                                "name": "Read",
                                "callId": "call-1",
                                "providerData": {
                                    "messageId": "msg-tool",
                                    "toolResult": {"content": "<tool_use_error>missing</tool_use_error>"},
                                },
                            }
                        ),
                        json.dumps(
                            {
                                "timestamp": 2000,
                                "type": "assistant",
                                "providerData": {"messageId": "msg-next"},
                                "content": "next step",
                            }
                        ),
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            state_path = root / ".state.json"

            context = collector.find_current_tool_context(
                state_path=state_path,
                sid="s-next-mid",
                transcript_path=str(transcript),
                tool_name="Read",
                call_id="call-1",
            )

            self.assertEqual(context["message_id"], "msg-next")
            self.assertEqual(context["tool_details"]["original_message_id"], "msg-tool")
            self.assertEqual(context["tool_details"]["next_message_id"], "msg-next")
            self.assertTrue(context["tool_details"]["message_id_reassigned"])

    def test_flush_pending_tool_events_emits_after_transcript_catches_up(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            state_path = root / ".state.json"
            log_path = root / "metrics.ndjson"
            transcript = root / "session.jsonl"
            transcript.write_text("", encoding="utf-8")
            sid = "s-pending"

            tool_event = {
                "event": "tool",
                "sid": sid,
                "agent": "main",
                "tool": "Bash",
                "ms": 42,
                "transcript_path": str(transcript),
                "turn_id": "turn-1",
                "ts": 1.2,
                "skill": [],
                "rule": [],
                "cwd": str(ROOT),
            }
            st.append_pending_tool_emit(state_path, sid, tool_event)

            transcript.write_text(
                json.dumps(
                    {
                        "timestamp": 1300,
                        "type": "function_call",
                        "name": "Bash",
                        "callId": "call-1",
                        "providerData": {"messageId": "msg-later"},
                    }
                )
                + "\n",
                encoding="utf-8",
            )

            emitted_calls: list[dict[str, object]] = []
            original_emit_post_step = agentlens.emit_post_step
            try:
                agentlens.emit_post_step = lambda **kwargs: emitted_calls.append(kwargs)
                runtime.flush_pending_tool_events(
                    state_path=state_path,
                    log_path=log_path,
                    sid=sid,
                )
            finally:
                agentlens.emit_post_step = original_emit_post_step

            self.assertEqual(st.get_pending_tool_emits(state_path, sid), [])
            lines = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines() if line.strip()]
            self.assertEqual(len(lines), 1)
            self.assertEqual(lines[0]["message_id"], "msg-later")
            self.assertEqual(len(emitted_calls), 1)
            self.assertEqual(emitted_calls[0]["tool_event"]["message_id"], "msg-later")

    def test_related_transcript_paths_include_subagents(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            sid = "session-123"
            main = root / f"{sid}.jsonl"
            bundle = root / sid
            subagents = bundle / "subagents"
            subagents.mkdir(parents=True)
            sub_a = subagents / "agent-a.jsonl"
            sub_b = subagents / "agent-b.jsonl"
            for path in (main, sub_a, sub_b):
                path.write_text("", encoding="utf-8")

            paths = collector.related_transcript_paths(sid, str(main))

            self.assertEqual(
                paths,
                [
                    str(main.resolve()),
                    str(sub_a.resolve()),
                    str(sub_b.resolve()),
                ],
            )

    def test_subagent_transcript_maps_to_role_name(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            subagent = root / "subagents" / "agent-27e41af0.jsonl"
            subagent.parent.mkdir(parents=True)
            subagent.write_text(
                '{"content":[{"text":"<teammate-message teammate_id=\\"team-lead\\" summary=\\"Initial task assignment for knowledge-engineer\\">"}]}\n',
                encoding="utf-8",
            )

            self.assertEqual(
                agent_identity.agent_for_transcript_path(str(subagent), "main"),
                "knowledge-engineer",
            )

    def test_subagent_transcript_prefers_assignment_role_over_general_provider_agent(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            subagent = root / "subagents" / "agent-165e361f.jsonl"
            subagent.parent.mkdir(parents=True)
            subagent.write_text(
                '{"type":"message","providerData":{"agent":"general-purpose"},"content":[{"text":"<teammate-message teammate_id=\\"team-lead\\" summary=\\"Initial task assignment for developer\\">\\n待命。\\n你在本 devflow 中的角色：developer（开发角色）"}]}\n',
                encoding="utf-8",
            )

            self.assertEqual(
                agent_identity.agent_for_transcript_path(str(subagent), "main"),
                "developer",
            )

    def test_inbox_standby_assignment_does_not_switch_current_agent(self):
        messages = [
            {
                "mailbox_name": "architect",
                "mailbox_role": "architect",
                "from_role": "main",
                "summary": "Initial task assignment for architect",
                "text": "待命，监听 main 唤醒。",
                "payload": None,
                "is_shutdown": False,
                "is_standby": True,
            }
        ]
        current, dispatched, meta = agent_identity.infer_identity_from_messages(messages)
        self.assertIsNone(current)
        self.assertIsNone(dispatched)
        self.assertEqual(meta["messages_seen"], 1)

    def test_record_pre_post_duration(self):
        with tempfile.TemporaryDirectory() as td:
            pending = Path(td) / ".pending.json"
            pre = {"session_id": "s1", "tool_name": "read_file"}
            post = {"session_id": "s1", "tool_name": "read_file"}
            collector.record_pre(pending, pre)
            time.sleep(0.02)
            ms = collector.record_post(pending, post)
            self.assertIsInstance(ms, int)
            self.assertGreaterEqual(ms, 0)

    def test_record_tool_usage_counts_skill_and_rule(self):
        with tempfile.TemporaryDirectory() as td:
            state_path = Path(td) / ".state.json"
            sid = "s-tool"
            # 先确保 session 结构
            s = st.load_state(state_path)
            st.ensure_session(s, sid)
            st.save_state(state_path, s)

            data = {
                "tool_name": "use_skill",
                "tool_input": {"command": "pdf", "filePath": "README.md"},
            }
            skills_meta = {"pdf": {"source": "user", "version": "1.0.0"}}
            rules_meta = {
                "security": {"source": "project", "alwaysApply": True, "enabled": True, "globs": []}
            }
            collector.record_tool_usage(
                state_path=state_path,
                sid=sid,
                data=data,
                skills_meta=skills_meta,
                rules_meta=rules_meta,
                active_agent="main",
                collect_skills=True,
            )

            skills_usage, rules_usage = collector.get_session_usage(state_path, sid)
            self.assertEqual(skills_usage.get("pdf", {}).get("count"), 1)
            self.assertEqual(rules_usage.get("security", {}).get("count"), 1)

    def test_step_span_registry_is_idempotent(self):
        with tempfile.TemporaryDirectory() as td:
            state_path = Path(td) / ".state.json"
            sid = "s-step"
            carrier = {"traceparent": "00-" + ("1" * 32) + "-" + ("2" * 16) + "-01"}
            st.begin_turn(state_path, sid, "turn-1", carrier)

            first = st.upsert_step_span(
                state_path,
                sid,
                "main",
                "msg-001",
                {"traceparent": "00-" + ("1" * 32) + "-" + ("3" * 16) + "-01"},
                transcript_path="/tmp/demo.jsonl",
            )
            second = st.upsert_step_span(
                state_path,
                sid,
                "main",
                "msg-001",
                {"traceparent": "00-" + ("1" * 32) + "-" + ("4" * 16) + "-01"},
                transcript_path="/tmp/demo.jsonl",
            )

            self.assertEqual(first, second)
            self.assertEqual(st.get_step_span_carrier(state_path, sid, "main", "msg-001"), first)


if __name__ == "__main__":
    unittest.main()
