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
