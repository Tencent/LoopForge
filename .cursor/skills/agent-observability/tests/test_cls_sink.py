from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "scripts"
if str(SCRIPTS) not in sys.path:
    sys.path.insert(0, str(SCRIPTS))

from core import cls_sink, emitter  # type: ignore


class CLSSinkTests(unittest.TestCase):
    def _write_cls_env(self, root: Path, *extra_lines: str) -> None:
        lines = [
            "CLS_TOPIC_ID=test-topic-id",
            "CLS_ENDPOINT=cls.internal.tencentcloudapi.com",
            "CLS_SERVICE_NAME=agent-observability",
            "CLS_UPLOAD_TIMEOUT_SECONDS=20",
            *extra_lines,
        ]
        (root / ".env").write_text("\n".join(lines) + "\n", encoding="utf-8")

    def test_load_config_supports_cls_and_tc_credentials(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            self._write_cls_env(
                root,
                "TC_SECRET_ID=test-secret-id",
                "TC_SECRET_KEY=test-secret-key",
            )
            with mock.patch.dict(os.environ, {}, clear=True):
                config = cls_sink.load_config(td)

        self.assertTrue(config.enabled)
        self.assertEqual(config.endpoint, "cls.internal.tencentcloudapi.com")
        self.assertEqual(config.topic_id, "test-topic-id")
        self.assertEqual(config.secret_id, "test-secret-id")
        self.assertEqual(config.secret_key, "test-secret-key")
        self.assertEqual(config.secret_token, "")
        self.assertEqual(config.service_name, "agent-observability")
        self.assertEqual(config.timeout_seconds, 20)
        self.assertTrue(config.ready)

    def test_load_config_supports_tencentcloud_scoped_credentials(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            self._write_cls_env(root)
            with mock.patch.dict(
                os.environ,
                {
                    "TENCENTCLOUD_SECRET_ID_438167613": "scoped-secret-id",
                    "TENCENTCLOUD_SECRET_KEY_438167613": "scoped-secret-key",
                },
                clear=True,
            ):
                config = cls_sink.load_config(td)

        self.assertEqual(config.secret_id, "scoped-secret-id")
        self.assertEqual(config.secret_key, "scoped-secret-key")
        self.assertEqual(config.timeout_seconds, 20)
        self.assertTrue(config.ready)

    def test_mirror_record_invokes_node_uploader(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            self._write_cls_env(
                root,
                "CLS_SECRET_ID=test-secret-id",
                "CLS_SECRET_KEY=test-secret-key",
            )
            with mock.patch.object(cls_sink, "_put_logs_via_api", return_value=(False, "python-failed")), \
                 mock.patch.object(cls_sink.subprocess, "run", return_value=mock.Mock(returncode=0, stdout="", stderr="")) as run, \
                 mock.patch.dict(os.environ, {}, clear=True):
                ok = cls_sink.mirror_record({"event": "tool", "sid": "s-1"}, cwd=td)

        self.assertTrue(ok)
        run.assert_called_once()
        payload = json.loads(run.call_args.kwargs["input"])
        self.assertEqual(payload["records"][0]["event"], "tool")
        self.assertEqual(payload["topicId"], "test-topic-id")
        self.assertEqual(payload["secretToken"], "")

    def test_load_config_requires_credentials_from_env(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            self._write_cls_env(root)
            with mock.patch.dict(os.environ, {}, clear=True):
                config = cls_sink.load_config(td)

        self.assertEqual(config.secret_id, "")
        self.assertEqual(config.secret_key, "")
        self.assertEqual(config.secret_token, "")
        self.assertEqual(config.timeout_seconds, 20)
        self.assertFalse(config.ready)

    def test_load_config_reads_secret_token_aliases(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            self._write_cls_env(
                root,
                "CLS_SECRET_ID=test-secret-id",
                "CLS_SECRET_KEY=test-secret-key",
                "TC_SESSION_TOKEN=test-session-token",
            )
            with mock.patch.dict(os.environ, {}, clear=True):
                config = cls_sink.load_config(td)

        self.assertEqual(config.secret_token, "test-session-token")

    def test_load_config_supports_timeout_override(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            self._write_cls_env(
                root,
                "CLS_SECRET_ID=test-secret-id",
                "CLS_SECRET_KEY=test-secret-key",
                "CLS_UPLOAD_TIMEOUT_SECONDS=45",
            )
            with mock.patch.dict(os.environ, {}, clear=True):
                config = cls_sink.load_config(td)

        self.assertEqual(config.timeout_seconds, 45)

    def test_mirror_record_writes_debug_when_uploader_fails(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            debug_path = root / "cls-push-debug.ndjson"
            self._write_cls_env(
                root,
                "CLS_SECRET_ID=test-secret-id",
                "CLS_SECRET_KEY=test-secret-key",
            )
            with mock.patch.object(cls_sink, "debug_log_path", return_value=debug_path), \
                 mock.patch.object(cls_sink, "_put_logs_via_api", return_value=(False, "python-failed")), \
                 mock.patch.object(cls_sink.subprocess, "run", return_value=mock.Mock(returncode=1, stdout="", stderr="boom")), \
                 mock.patch.dict(os.environ, {}, clear=True):
                ok = cls_sink.mirror_record({"event": "tool", "sid": "s-2"}, cwd=td)

            entries = [json.loads(line) for line in debug_path.read_text("utf-8").splitlines() if line.strip()]

        self.assertFalse(ok)
        self.assertEqual(entries[-1]["stage"], "uploader_failed")
        self.assertEqual(entries[-1]["stderr"], "boom")
        self.assertEqual(entries[-1]["timeout_seconds"], 20)

    def test_mirror_record_writes_success_debug_when_uploader_succeeds(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            debug_path = root / "cls-push-debug.ndjson"
            self._write_cls_env(
                root,
                "CLS_SECRET_ID=test-secret-id",
                "CLS_SECRET_KEY=test-secret-key",
            )
            with mock.patch.object(cls_sink, "debug_log_path", return_value=debug_path), \
                 mock.patch.object(cls_sink, "_put_logs_via_api", return_value=(True, "ok")), \
                 mock.patch.dict(os.environ, {}, clear=True):
                ok = cls_sink.mirror_record({"event": "tool", "sid": "s-3"}, cwd=td)

            entries = [json.loads(line) for line in debug_path.read_text("utf-8").splitlines() if line.strip()]

        self.assertTrue(ok)
        self.assertEqual(entries[0]["stage"], "uploader_start")
        self.assertEqual(entries[-1]["stage"], "uploader_ok")
        self.assertEqual(entries[-1]["method"], "python_api_v3")

    def test_emit_keeps_local_log_when_cls_push_raises(self):
        with tempfile.TemporaryDirectory() as td:
            log_path = Path(td) / "metrics.ndjson"
            with mock.patch.object(emitter.cls_sink, "mirror_record", side_effect=RuntimeError("boom")):
                emitter.emit(log_path, {"event": "start", "sid": "s-emit"})

            records = [json.loads(line) for line in log_path.read_text("utf-8").splitlines() if line.strip()]

        self.assertEqual(len(records), 1)
        self.assertEqual(records[0]["event"], "start")
        self.assertEqual(records[0]["sid"], "s-emit")
