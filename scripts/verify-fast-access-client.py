#!/usr/bin/env python3
"""Run the unmodified public Python client against the Go integration fixture.
Invoked by TestFastAccessPublicClientIntegration, never against production.
"""
import importlib.util
import json
import sqlite3
import sys
import time
from datetime import datetime, timedelta, timezone
from urllib.parse import urlencode

path, first, second, principal, ws1, ws2, issue, directory = sys.argv[1:]
spec = importlib.util.spec_from_file_location("public_client", path)
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
scope = {"mode": "workspace_ids", "workspace_ids": [ws1, ws2]}
transport = c.HTTP(first, fixture=True)
replica = c.HTTP(second, fixture=True)
cache = c.Cache(directory, first, principal, scope)
try:
    baseline = c.sync(cache, transport)
    assert len(baseline["active_runs"]) == 17
    assert sum(x["kind"] == "issue" for x in baseline["summaries"]) == 200
    before = cache.state()["checkpoint"]
    def change(title):
        with transport.open("/fixture/change?" + urlencode({"title": title})) as r:
            assert r.status == 200
    def receive(http, checkpoint):
        frames = http.events(checkpoint)
        captured = []
        try:
            for frame in frames:
                captured.append(frame)
                if frame["event"] in ("checkpoint", "read_required", "scope_reset", "reset_required"):
                    break
        finally:
            frames.close()  # Actual TCP stream disconnect.
        return captured
    change("SSE observed")
    frames = receive(transport, before)
    assert any(f["event"] == "change" for f in frames)
    c.consume(cache, transport, iter(frames))
    assert next(x for x in cache.view()["summaries"] if x["id"] == issue)["title"] == "SSE observed"
    disconnected = cache.state()["checkpoint"]
    cache.stale("disconnected")
    change("replayed across instance")
    frames = receive(replica, disconnected)
    assert any(f["event"] == "change" for f in frames)
    c.consume(cache, replica, iter(frames))
    assert cache.state()["checkpoint"] != disconnected
    with sqlite3.connect(cache.path) as db:
        rows = [json.loads(r[0]) for r in db.execute("SELECT body FROM objects WHERE body IS NOT NULL")]
    assert next(x for x in rows if x["id"] == issue)["title"] == "replayed across instance"
    assert cache.view()["stale_reason"] == "offline_unverified"
    print("PASS baseline: 200 issues, 17 active runs; change -> TCP SSE -> disconnect -> other handler replay -> SQLite")
    print("PASS applied read checkpoint advances only through sync; offline cache reports offline_unverified")
    # since30m with fixed upper pagination, active-first, and uncompressed wire
    # size including envelope. Keep all 30 samples, measure full logical batch.
    since = (datetime.now(timezone.utc) - timedelta(minutes=30)).isoformat()
    durations, sizes, page_counts = [], [], []
    for sample in range(30):
        request = {"scope": scope, "since": since, "limit": 100, "max_bytes": 50000}
        started = time.monotonic()
        issues, active, events, pages, frozen = set(), set(), set(), 0, None
        while True:
            with transport.open("/api/fast-access/v1/read", request) as response:
                raw = response.read(50001)
            assert len(raw) <= 50000
            sizes.append(len(raw))
            page = json.loads(raw)
            pages += 1
            frozen = frozen or page["as_of"]
            assert page["as_of"] == frozen
            if pages == 1:
                assert len(page["active_runs"]) == 17 and page["active_runs_complete"]
            for item in page["changes"]:
                if item.get("event_id"):
                    assert item["event_id"] not in events
                    events.add(item["event_id"])
                if item["kind"] == "issue":
                    issues.add(item["id"])
            active.update(x["id"] for x in page["active_runs"])
            if page["complete"]:
                assert page["checkpoint"] and not page["next_cursor"]
                break
            assert page["checkpoint"] is None and page["next_cursor"]
            request = {"cursor": page["next_cursor"], "limit": 100, "max_bytes": 50000}
        assert len(issues) == 200 and len(active) == 17
        durations.append((time.monotonic()-started)*1000)
        page_counts.append(pages)
    ordered = sorted(durations)
    print(json.dumps({"fixture": "local two workspaces, since30m, 200 issue changes, 17 active runs", "samples_ms": durations,
                      "p50_ms": ordered[14], "p95_ms": ordered[28], "max_uncompressed_response_bytes": max(sizes),
                      "pages_each": page_counts, "active_first": True, "complete_without_loss": True}))
finally:
    cache.close()
