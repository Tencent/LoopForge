#!/usr/bin/env python3
"""Small, pinned local BGE-M3 ONNX HTTP sidecar (public sources only).

The model is a public Hugging Face repo pinned by revision, and the ONNX
artifact is verified by SHA-256 before use. The platform talks to a narrow
protocol: POST /embed (or /v1/embeddings) with
{"model": "...", "input": ["..."], "task": "query|document"} and gets back
{"embeddings": [[...]]}. A real inference runs before the server reports
ready, so a healthy container means inference works, not just a listening port.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import logging
import math
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.request import urlopen

LOG = logging.getLogger("kb-embedding")

# Public model source. Pin the repo, the revision and the ONNX checksum so a
# collection always contains vectors from one immutable artifact.
DEFAULT_MODEL = "gpahal/bge-m3-onnx-int8"
DEFAULT_REVISION = "2b34e84df040034d4b9eabb62383a87c18955822"
DEFAULT_ONNX_SHA256 = "16de7ea1146ca427e14938ec3e9abfdcaff0e6ac76434cd693ac35d761250bcb"
DEFAULT_DIM = 1024
MAX_INPUT_CHARS = 64 * 1024
MAX_REQUEST_BYTES = 4 * 1024 * 1024
MAX_BATCH = 64


class EmbeddingRuntime:
    """Lazy model wrapper; heavyweight imports stay inside load()."""

    def __init__(self) -> None:
        self.model = os.environ.get("EMBEDDING_MODEL", DEFAULT_MODEL)
        self.revision = os.environ.get("EMBEDDING_REVISION", DEFAULT_REVISION)
        self.onnx_sha256 = os.environ.get("EMBEDDING_ONNX_SHA256", DEFAULT_ONNX_SHA256)
        self.dim = int(os.environ.get("EMBEDDING_DIM", str(DEFAULT_DIM)))
        self.max_length = int(os.environ.get("EMBEDDING_MAX_LENGTH", "256"))
        if self.max_length <= 0 or self.dim <= 0:
            raise ValueError("EMBEDDING_DIM / EMBEDDING_MAX_LENGTH must be positive")
        self.cache_dir = Path(os.environ.get("EMBEDDING_CACHE_DIR", "/models/cache"))
        self._tokenizer: Any = None
        self._session: Any = None
        self._lock = threading.Lock()
        self._ready_lock = threading.Lock()
        self._ready_at = 0.0

    def load(self) -> None:
        if self._session is not None:
            return
        from huggingface_hub import hf_hub_download
        from transformers import AutoTokenizer
        import onnxruntime as ort

        self.cache_dir.mkdir(parents=True, exist_ok=True)
        self._tokenizer = AutoTokenizer.from_pretrained(
            self.model, revision=self.revision, cache_dir=str(self.cache_dir)
        )
        onnx_path = hf_hub_download(
            repo_id=self.model,
            filename="model_quantized.onnx",
            revision=self.revision,
            cache_dir=str(self.cache_dir),
        )
        if self.onnx_sha256:
            digest = hashlib.sha256()
            with open(onnx_path, "rb") as model_file:
                for block in iter(lambda: model_file.read(8 << 20), b""):
                    digest.update(block)
            if digest.hexdigest() != self.onnx_sha256:
                raise ValueError("downloaded ONNX artifact checksum mismatch")
        options = ort.SessionOptions()
        options.intra_op_num_threads = max(0, int(os.environ.get("EMBEDDING_INTRA_THREADS", "0")))
        options.inter_op_num_threads = max(0, int(os.environ.get("EMBEDDING_INTER_THREADS", "0")))
        self._session = ort.InferenceSession(
            onnx_path, options, providers=["CPUExecutionProvider"]
        )
        output_shape = self._session.get_outputs()[0].shape
        if output_shape and isinstance(output_shape[-1], int) and output_shape[-1] != self.dim:
            raise ValueError(
                f"model output dimension {output_shape[-1]} != configured {self.dim}"
            )

    def embed_batch(self, texts: list[str], task: str) -> list[list[float]]:
        if task not in {"query", "document"}:
            raise ValueError("task must be query or document")
        if not texts:
            raise ValueError("input list must not be empty")
        for text in texts:
            if not isinstance(text, str) or not text.strip():
                raise ValueError("input must be a non-empty string")
            if len(text) > MAX_INPUT_CHARS:
                raise ValueError("input is too large")
        with self._lock:
            self.load()
            encoded = self._tokenizer(
                texts, padding=True, truncation=True, max_length=self.max_length, return_tensors="np"
            )
            inputs = {
                "input_ids": encoded["input_ids"].astype("int64"),
                "attention_mask": encoded["attention_mask"].astype("int64"),
            }
            if "token_type_ids" in encoded:
                inputs["token_type_ids"] = encoded["token_type_ids"].astype("int64")
            output_name = self._session.get_outputs()[0].name
            values = self._session.run([output_name], inputs)[0]
            vectors: list[list[float]] = []
            for row in values:
                norm = math.sqrt(float((row * row).sum()))
                if norm <= 1e-12:
                    raise ValueError("model returned a zero vector")
                vector = (row / norm).tolist()
                if len(vector) != self.dim:
                    raise ValueError(f"model returned dimension {len(vector)} != {self.dim}")
                vectors.append([float(value) for value in vector])
            return vectors

    def ready(self) -> dict[str, Any]:
        # Real inference probe, not a process check.
        with self._lock:
            self.embed_batch(["knowledge-base readiness probe"], "query")
        self._ready_at = time.monotonic()
        return self._metadata(ready=True)

    def _metadata(self, ready: bool) -> dict[str, Any]:
        return {
            "ready": ready,
            "model": self.model,
            "revision": self.revision,
            "artifact_sha256": self.onnx_sha256,
            "dim": self.dim,
            "max_length": self.max_length,
        }


class Handler(BaseHTTPRequestHandler):
    runtime: EmbeddingRuntime

    def log_message(self, fmt: str, *args: Any) -> None:
        # Request bodies can contain customer knowledge; never log them.
        LOG.info("%s - %s", self.address_string(), fmt % args)

    def _write(self, status: int, payload: dict[str, Any]) -> None:
        encoded = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        try:
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)
        except (BrokenPipeError, ConnectionResetError):
            return

    def do_GET(self) -> None:  # noqa: N802
        if self.path not in {"/healthz", "/version"}:
            self._write(404, {"error": "not found"})
            return
        try:
            if self.path == "/version":
                self._write(200, self.runtime._metadata(ready=True))
            else:
                self._write(200, self.runtime.ready())
        except Exception as exc:
            LOG.error("embedding readiness failed: %s", exc)
            self._write(503, {"ready": False, "model": self.runtime.model, "dim": self.runtime.dim})

    def do_POST(self) -> None:  # noqa: N802
        if self.path not in {"/embed", "/v1/embeddings"}:
            self._write(404, {"error": "not found"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length <= 0 or length > MAX_REQUEST_BYTES:
                raise ValueError("invalid request size")
            body = json.loads(self.rfile.read(length))
            if not isinstance(body, dict):
                raise ValueError("request must be an object")
            if body.get("model", self.runtime.model) != self.runtime.model:
                raise ValueError("model mismatch")
            raw_input = body.get("input", "")
            if isinstance(raw_input, list):
                if not raw_input or len(raw_input) > MAX_BATCH:
                    raise ValueError("input list must have 1..64 items")
                if any(not isinstance(item, str) for item in raw_input):
                    raise ValueError("input list items must be strings")
                inputs = raw_input
            elif isinstance(raw_input, str):
                inputs = [raw_input]
            else:
                raise ValueError("input must be a string or list of strings")
            task = str(body.get("task", "document"))
            vectors = self.runtime.embed_batch(inputs, task)
            response = {
                "embedding": vectors[0],
                "embeddings": vectors,
                "model": self.runtime.model,
                "revision": self.runtime.revision,
                "artifact_sha256": self.runtime.onnx_sha256,
                "dim": self.runtime.dim,
                "max_length": self.runtime.max_length,
            }
            if self.path == "/v1/embeddings":
                response["data"] = [
                    {"index": i, "embedding": vector} for i, vector in enumerate(vectors)
                ]
            self._write(200, response)
        except (ValueError, json.JSONDecodeError) as exc:
            self._write(400, {"error": str(exc)})
        except Exception as exc:
            LOG.error("embedding request failed: %s", exc)
            self._write(503, {"error": "embedding backend unavailable"})


def serve() -> None:
    runtime = EmbeddingRuntime()
    runtime.ready()
    Handler.runtime = runtime
    host = os.environ.get("EMBEDDING_HOST", "0.0.0.0")
    port = int(os.environ.get("EMBEDDING_PORT", "8081"))
    LOG.info("local embedding ready: model=%s revision=%s dim=%s", runtime.model, runtime.revision, runtime.dim)
    ThreadingHTTPServer((host, port), Handler).serve_forever()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--healthcheck", action="store_true")
    args = parser.parse_args()
    logging.basicConfig(level=os.environ.get("LOG_LEVEL", "INFO"))
    if args.healthcheck:
        host = os.environ.get("EMBEDDING_HEALTH_HOST", "127.0.0.1")
        port = os.environ.get("EMBEDDING_PORT", "8081")
        timeout = float(os.environ.get("EMBEDDING_HEALTH_TIMEOUT", "170"))
        with urlopen(f"http://{host}:{port}/healthz", timeout=timeout) as response:
            payload = json.load(response)
        if not payload.get("ready") or payload.get("dim") != int(os.environ.get("EMBEDDING_DIM", str(DEFAULT_DIM))):
            raise SystemExit(1)
        return 0
    serve()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
