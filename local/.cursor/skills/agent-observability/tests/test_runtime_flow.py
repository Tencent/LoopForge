from __future__ import annotations

import tempfile
import unittest
from pathlib import Path
from unittest import mock
import sys

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "scripts"
if str(SCRIPTS) not in sys.path:
    sys.path.insert(0, str(SCRIPTS))

from core import runtime  # type: ignore


class RuntimeFlowTests(unittest.TestCase):
    def test_resolve_active_agent_prefers_inbox_inferred_agent_when_available(self):
        data = {
            "tool_name": "Task",
            "tool_input": {"subagent_name": "developer"},
        }
        with tempfile.TemporaryDirectory() as td:
            state_path = Path(td) / ".state.json"
            with mock.patch.object(runtime.agent_identity, "merge_agent_identity_from_inbox", return_value=("qa", "developer")) as merge_identity:
                active, dispatched = runtime.agent_identity.resolve_active_agent_for_event(
                    state_path=state_path,
                    sid="s-inbox",
                    cwd=td,
                    data=data,
                )

        self.assertEqual(active, "qa")
        self.assertEqual(dispatched, "developer")
        merge_identity.assert_called_once()

    def test_handle_post_routes_through_expected_collaborators(self):
        data = {
            "session_id": "s-runtime",
            "cwd": "/tmp/demo",
            "tool_name": "Read",
            "transcript_path": "/tmp/demo.jsonl",
        }
        with tempfile.TemporaryDirectory() as td:
            with mock.patch.object(runtime, "build_runtime_paths") as mock_paths, \
                 mock.patch.object(runtime.collector, "record_post", return_value=12), \
                 mock.patch.object(runtime.collector, "load_cached_inventory", return_value=({}, {})), \
                 mock.patch.object(runtime.scanner, "scan_skills_and_rules", return_value=({}, {})), \
                 mock.patch.object(runtime.collector, "cache_inventory"), \
                 mock.patch.object(runtime.collector, "record_tool_usage", return_value=([], [])), \
                 mock.patch.object(runtime.agent_identity, "resolve_active_agent_for_event", return_value=("main", None)), \
                 mock.patch.object(runtime, "current_turn_id", return_value="turn-1"), \
                 mock.patch.object(runtime.collector, "extract_tool_call_id", return_value=None), \
                 mock.patch.object(runtime, "flush_pending_tool_events"), \
                 mock.patch.object(runtime.collector, "find_current_tool_context", return_value=None), \
                 mock.patch.object(runtime.collector, "related_transcript_paths", return_value=["/tmp/demo.jsonl"]), \
                 mock.patch.object(runtime, "emit_transcript_events", return_value=[]), \
                 mock.patch.object(runtime.collector, "find_fallback_usage_event", return_value=None), \
                 mock.patch.object(runtime.st, "append_pending_tool_emit"), \
                 mock.patch.object(runtime.agentlens, "emit_post_step"):
                mock_paths.return_value = runtime.RuntimePaths(
                    base_dir=Path(td),
                    state_path=Path(td) / ".state.json",
                    pending_path=Path(td) / ".pending.json",
                    log_path=Path(td) / "metrics.ndjson",
                )
                runtime.handle_post(data)

    def test_main_dispatches_supported_phase_to_handler(self):
        with mock.patch.object(runtime, "handle_post") as handle_post, \
             mock.patch.object(runtime.collector, "read_stdin_json", return_value={"session_id": "s1"}):
            rc = runtime.main(["post"])
        self.assertEqual(rc, 0)
        handle_post.assert_called_once()
