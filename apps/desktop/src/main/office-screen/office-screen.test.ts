// @vitest-environment node
import { EventEmitter } from "events";
import { mkdtempSync, readFileSync, statSync, writeFileSync, mkdirSync } from "fs";
import { tmpdir } from "os";
import { join, resolve } from "path";
import { PassThrough } from "stream";
import type { ChildProcessWithoutNullStreams } from "child_process";
import { describe, expect, it, vi } from "vitest";
import { latchPath, parseLatch, readLatch, resumeLatch, stopLatch } from "./latch";
import { computeReceipt } from "./receipt";
import { CaptureProcess, HEARTBEAT_TIMEOUT_MS, lineSplitter, type CaptureStatus } from "./capture";

const fixtures = JSON.parse(
  readFileSync(resolve(__dirname, "../../../native/office-vd/test/latch-fixtures.json"), "utf8"),
) as Array<{ name: string; raw: string | null; state: string; generation: number | null; readError: boolean }>;

const tmpHome = () => mkdtempSync(join(tmpdir(), "office-screen-test-"));

describe("stop latch (shared fixtures with broker and daemon)", () => {
  for (const f of fixtures) {
    it(f.name, () => {
      const l = parseLatch(f.raw);
      expect(l.state).toBe(f.state);
      expect(l.generation).toBe(f.generation);
      expect(Boolean(l.readError)).toBe(f.readError);
    });
  }

  it("stop persists with 0600, is idempotent, and resume needs the generation it saw", () => {
    const home = tmpHome();
    expect(readLatch(home).state).toBe("running");
    const s = stopLatch(home);
    expect([s.state, s.generation]).toEqual(["stopped", 1]);
    expect(stopLatch(home).generation).toBe(1);
    expect(statSync(latchPath(home)).mode & 0o777).toBe(0o600);

    expect(resumeLatch(home, 0).ok).toBe(false);
    const r = resumeLatch(home, 1);
    expect(r.ok).toBe(true);
    expect([r.latch.state, r.latch.generation]).toEqual(["running", 2]);
    const again = stopLatch(home);
    expect(again.generation).toBe(3);
    expect(resumeLatch(home, 1).ok).toBe(false);
    expect(readLatch(home).state).toBe("stopped");
  });

  it("an unreadable latch reads stopped, can't be resumed, and stop moves past it", () => {
    const home = tmpHome();
    mkdirSync(join(home, "control"), { recursive: true });
    writeFileSync(latchPath(home), "{oops");
    expect(readLatch(home).state).toBe("stopped");
    expect(resumeLatch(home, null).ok).toBe(false);
    const s = stopLatch(home);
    expect(s.readError).toBeUndefined();
    expect(s.generation).toBeGreaterThanOrEqual(1);
  });
});

describe("stop receipt", () => {
  const stopped = parseLatch('{"version":1,"state":"stopped","generation":7}');
  const broker = (gen: number | null, protocol: number | undefined = 2) => ({
    protocol, connected: true, latch: { state: "stopped", generation: gen },
  });
  const daemon = (gen: number | null, active: number) => ({
    status: "running", stop_latch: { state: "stopped", generation: gen, claims_paused: true, active_tasks: active },
  });

  it("is absent while running", () => {
    expect(computeReceipt(parseLatch(null), broker(0), daemon(0, 0))).toBeNull();
  });

  it("is complete only when both processes confirm the same generation and no run is left", () => {
    expect(computeReceipt(stopped, broker(7), daemon(7, 0))).toMatchObject({
      requested: "done", inputSealed: "done", tasksStopped: "done", activeTasks: 0,
    });
    expect(computeReceipt(stopped, broker(6), daemon(7, 2))).toMatchObject({
      inputSealed: "pending", tasksStopped: "pending", activeTasks: 2,
    });
  });

  it("says outdated when a process can't know about the latch", () => {
    const r = computeReceipt(stopped, { connected: true }, { status: "running", active_task_count: 1 });
    expect(r).toMatchObject({ inputSealed: "outdated", tasksStopped: "outdated" });
  });

  it("treats a process that isn't running as not running, not as confirmed", () => {
    expect(computeReceipt(stopped, null, null)).toMatchObject({
      inputSealed: "not_running", tasksStopped: "not_running", activeTasks: null,
    });
  });
});

describe("capture helper process", () => {
  function fakeChild() {
    const streams = { stdin: new PassThrough(), stdout: new PassThrough(), stderr: new PassThrough() };
    const child = Object.assign(new EventEmitter(), streams, { kill: vi.fn(() => true) });
    return child as unknown as ChildProcessWithoutNullStreams & typeof streams & { kill: typeof child.kill };
  }

  it("splits long lines across chunks", () => {
    const lines: string[] = [];
    const feed = lineSplitter((l) => lines.push(l));
    feed('{"a":1}\n{"b"');
    feed(':2}\n\n');
    expect(lines).toEqual(['{"a":1}', '{"b":2}']);
  });

  it("turns helper lines into frames and status, and closes stdin on stop", () => {
    const child = fakeChild();
    const statuses: CaptureStatus[] = [];
    const frames: number[] = [];
    const cap = new CaptureProcess("helper", [], {
      onFrame: (f) => frames.push(f.seq),
      onStatus: (s) => statuses.push(s),
    }, () => child);
    cap.start();
    child.stdout.write('{"type":"status","state":"blocked","reason":"unowned_window","windows":0,"owned":1}\n');
    child.stdout.write('{"type":"status","state":"streaming","reason":"ok","windows":2,"owned":2}\n');
    child.stdout.write('{"type":"frame","seq":3,"width":1280,"height":720,"jpeg":"AAAA"}\n');
    expect(statuses.map((s) => [s.status, s.reason])).toEqual([
      ["starting", null], ["blocked", "unowned_window"], ["streaming", "ok"],
    ]);
    expect(frames).toEqual([3]);
    const endSpy = vi.spyOn(child.stdin, "end");
    cap.stop();
    expect(endSpy).toHaveBeenCalled();
    expect(statuses.at(-1)?.status).toBe("off");
  });

  it("kills a helper that stops sending heartbeats", () => {
    vi.useFakeTimers();
    try {
      let now = 0;
      const child = fakeChild();
      const statuses: CaptureStatus[] = [];
      const cap = new CaptureProcess("helper", [], { onFrame: () => {}, onStatus: (s) => statuses.push(s) }, () => child, () => now);
      cap.start();
      now = HEARTBEAT_TIMEOUT_MS + 1;
      vi.advanceTimersByTime(1_000);
      expect(statuses.at(-1)).toMatchObject({ status: "blocked", reason: "stale" });
      expect(child.kill).toHaveBeenCalledWith("SIGKILL");
      cap.stop();
    } finally {
      vi.useRealTimers();
    }
  });
});
