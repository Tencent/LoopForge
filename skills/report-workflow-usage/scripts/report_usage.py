#!/usr/bin/env python3
"""Report a completed external workflow; retain the JSON file for safe retries."""
import argparse
from datetime import datetime, timedelta, timezone
import math
import re
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None



def validate_report(value):
    """Reject incomplete or inconsistent facts before attempting an upload."""
    def fail(message):
        raise ValueError(message)

    def fields(obj, allowed, label):
        if not isinstance(obj, dict) or set(obj) - set(allowed.split()):
            fail(label + " must be an object with only contract fields")

    def text(obj, key, limit, required=True):
        item = obj.get(key, "")
        if not isinstance(item, str) or (required and not item.strip()) or len(item.encode("utf-8")) > limit:
            fail(key + " is missing, invalid or too long")

    def integer(item, limit, label):
        if type(item) is not int or not 0 <= item <= limit:
            fail(label + " must be a non-negative integer within the contract limit")

    def timestamp(key):
        item = value.get(key)
        if not isinstance(item, str) or not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})", item):
            fail(key + " must be RFC3339 with a timezone")
        # Python 3.9/3.10 accept microseconds; RFC3339Nano producers may emit nine digits.
        normalized = re.sub(r"(\.\d{6})\d+(?=Z|[+-]\d{2}:\d{2}$)", r"\1", item)
        return datetime.fromisoformat(normalized.replace("Z", "+00:00")).astimezone(timezone.utc)

    fields(value, "id type externalExecutionId workflow externalUserId url startedAt finishedAt executionDurationMs status usage", "report")
    for key in ("id", "externalExecutionId"):
        text(value, key, 256)
    if value.get("type") != "workflow_execution" or value.get("status") not in ("succeeded", "failed", "cancelled"):
        fail("report must describe a completed workflow_execution")
    workflow = value.get("workflow")
    fields(workflow, "id name", "workflow")
    for key in ("id", "name"):
        text(workflow, key, 256)
    text(value, "externalUserId", 128, False)
    text(value, "url", 2048, False)
    if value.get("url"):
        parsed = urllib.parse.urlsplit(value["url"])
        if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.username is not None or parsed.password is not None:
            fail("url must be HTTP(S) without credentials")
    start, end = timestamp("startedAt"), timestamp("finishedAt")
    if start.year < 2000 or end < start or end > datetime.now(timezone.utc) + timedelta(minutes=5):
        fail("execution times are outside the contract range")
    if value.get("executionDurationMs") is not None:
        integer(value["executionDurationMs"], 315360000000, "executionDurationMs")
    usage = value.get("usage")
    if not isinstance(usage, list) or not 1 <= len(usage) <= 64:
        fail("usage must contain 1–64 model summaries")
    seen = set()
    for item in usage:
        fields(item, "provider model inputTokens outputTokens cacheReadTokens cacheWriteTokens reasoningTokens costUsd costStatus", "usage item")
        for key in ("provider", "model"):
            text(item, key, 128)
        identity = (item["provider"], item["model"])
        if identity in seen:
            fail("merge duplicate provider/model entries before reporting")
        seen.add(identity)
        for key in ("inputTokens", "outputTokens"):
            integer(item.get(key), 10**12, key)
        for key in ("cacheReadTokens", "cacheWriteTokens", "reasoningTokens"):
            integer(item.get(key, 0), 10**12, key)
        if item.get("cacheReadTokens", 0) + item.get("cacheWriteTokens", 0) > item["inputTokens"] or item.get("reasoningTokens", 0) > item["outputTokens"]:
            fail("cache/reasoning counts must be subsets of input/output")
        status, amount = item.get("costStatus"), item.get("costUsd")
        if status == "unknown":
            if amount is not None:
                fail("unknown cost must be null")
        elif status in ("reported", "estimated"):
            if type(amount) not in (int, float) or not 0 <= amount <= 10**9 or not math.isfinite(amount):
                fail("known cost must be a finite, non-negative USD amount")
        else:
            fail("costStatus must be reported, estimated or unknown")
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("file", nargs="?", default=os.environ.get("AGENTFLOW_USAGE_FILE"), help="Completed execution JSON file, or AGENTFLOW_USAGE_FILE (never deleted)")
    parser.add_argument("--check", action="store_true", help="Validate the file without credentials or network access")
    parser.add_argument("--url", default=os.environ.get("AGENTFLOW_URL"), help="AgentFlow base URL, or AGENTFLOW_URL")
    parser.add_argument("--attempts", type=int, default=5)
    args = parser.parse_args()
    if not args.file:
        parser.error("Provide a JSON file or set AGENTFLOW_USAGE_FILE")
    if not 1 <= args.attempts <= 10:
        parser.error("--attempts must be between 1 and 10")
    with open(args.file, "rb") as source:
        body = source.read((1 << 20) + 1)
    if len(body) > 1 << 20:
        parser.error("JSON file exceeds 1 MiB")
    report = validate_report(json.loads(body))
    if args.check:
        print(json.dumps({"status": "valid", "id": report["id"], "externalExecutionId": report["externalExecutionId"]}, ensure_ascii=False))
        return 0
    token = os.environ.get("AGENTFLOW_INSIGHT_TOKEN", "")
    if not token or not args.url:
        parser.error("Set AGENTFLOW_INSIGHT_TOKEN and AGENTFLOW_URL (or --url)")
    parsed = urllib.parse.urlsplit(args.url)
    if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment:
        parser.error("--url must be an HTTP(S) base URL without credentials, query or fragment")
    endpoint = args.url.rstrip("/") + "/api/insights/v1/ingest"
    opener = urllib.request.build_opener(NoRedirect)
    for attempt in range(args.attempts):
        request = urllib.request.Request(endpoint, data=body, method="POST", headers={
            "Authorization": "Bearer " + token, "Content-Type": "application/json",
        })
        delay = min(2 ** attempt, 30)
        try:
            with opener.open(request, timeout=30) as response:
                result = json.load(response)
                if result.get("status") not in ("accepted", "duplicate"):
                    raise ValueError("Server did not acknowledge persistence")
                print(json.dumps(result, ensure_ascii=False))
                return 0
        except urllib.error.HTTPError as error:
            if error.code != 429 and not 500 <= error.code <= 599:
                print(f"Upload rejected (HTTP {error.code}); fix the record or credential. File retained.", file=sys.stderr)
                return 1
            retry_after = error.headers.get("Retry-After", "")
            if retry_after.isdigit():
                delay = min(int(retry_after), 60)
        except (urllib.error.URLError, TimeoutError):
            pass
        if attempt + 1 < args.attempts:
            time.sleep(delay)
    print("Upload not acknowledged; JSON file retained. Retry with the same record ID and contents.", file=sys.stderr)
    return 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError) as error:
        print(f"Report failed: {error}", file=sys.stderr)
        sys.exit(1)
