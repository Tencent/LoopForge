from __future__ import annotations

import tempfile
import unittest
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "scripts"
if str(SCRIPTS) not in sys.path:
    sys.path.insert(0, str(SCRIPTS))

from core.agent_identity import AgentIdentityResolver  # type: ignore
from core import state as st  # type: ignore


class AgentIdentityResolverTests(unittest.TestCase):
    def test_extract_identity_from_send_message_json(self):
        resolver = AgentIdentityResolver()
        data = {
            "tool_name": "send_message",
            "tool_input": {
                "content": '{"from_role":"developer","next_target":{"role_name":"leader"}}'
            },
        }
        current, dispatched = resolver.extract_identity(data)
        self.assertEqual(current, "developer")
        self.assertEqual(dispatched, "leader")

    def test_track_returns_active_and_dispatched(self):
        resolver = AgentIdentityResolver()
        with tempfile.TemporaryDirectory() as td:
            state_path = Path(td) / ".state.json"
            sid = "s2"
            state_data = st.load_state(state_path)
            st.ensure_session(state_data, sid)
            st.save_state(state_path, state_data)

            data = {
                "tool_name": "Task",
                "tool_input": {"subagent_name": "developer"},
            }
            active, dispatched = resolver.track(state_path=state_path, sid=sid, data=data)
            self.assertEqual(active, "main")
            self.assertEqual(dispatched, "developer")

            state2 = st.load_state(state_path)
            sess = state2.get(sid, {})
            self.assertEqual(sess.get("current_agent"), "developer")
            self.assertEqual(sess.get("dispatched", {}).get("developer"), 1)

    def test_track_recognizes_agent_tool_subagent_type(self):
        """Claude Code 的 Agent 工具用 subagent_type（不是 CodeBuddy 原生 Task 的
        subagent_name）标识派发目标；这条路径之前没有测试覆盖，_extract_from_task
        漏认这个字段会导致 dispatch 统计对所有 Agent 工具派发的场景失明。"""
        resolver = AgentIdentityResolver()
        with tempfile.TemporaryDirectory() as td:
            state_path = Path(td) / ".state.json"
            sid = "s3"
            state_data = st.load_state(state_path)
            st.ensure_session(state_data, sid)
            st.save_state(state_path, state_data)

            data = {
                "tool_name": "Agent",
                "tool_input": {"subagent_type": "worker", "description": "spawn a team"},
            }
            active, dispatched = resolver.track(state_path=state_path, sid=sid, data=data)
            self.assertEqual(active, "main")
            self.assertEqual(dispatched, "worker")

            state2 = st.load_state(state_path)
            sess = state2.get(sid, {})
            self.assertEqual(sess.get("dispatched", {}).get("worker"), 1)
            hist = sess.get("agent_history") or []
            self.assertTrue(any(
                h.get("agent") == "worker" and str(h.get("evidence") or "").startswith("dispatch")
                for h in hist
            ))

    def test_extract_from_task_prefers_subagent_type_over_subagent_name(self):
        resolver = AgentIdentityResolver()
        result = resolver._extract_from_task({"subagent_type": "explorer", "subagent_name": "developer"})
        self.assertEqual(result, "explorer")
