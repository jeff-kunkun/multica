import {
  closeSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readFileSync,
  renameSync,
  statSync,
  writeSync,
} from "fs";
import { homedir } from "os";
import { dirname, join } from "path";
import type { OfficeLatch } from "../../shared/office-screen";

// The device stop latch, the desktop's copy of the rules in
// apps/desktop/native/office-vd/latch.mjs (format owner) and
// server/internal/daemon/stop_latch.go. All three are pinned by
// native/office-vd/test/latch-fixtures.json:
//   - no file: running, generation 0
//   - anything unreadable or unknown: stopped (fail closed)
//   - every write bumps the generation; resume must name the stopped
//     generation it saw, so a stale resume never undoes a newer stop.

export const LATCH_VERSION = 1;

export function officeVdHome(env: NodeJS.ProcessEnv = process.env): string {
  return env.OFFICE_VD_HOME || join(homedir(), ".agents", "vd");
}

export function latchPath(home: string): string {
  return join(home, "control", "latch.json");
}

function closed(readError: string): OfficeLatch {
  return { state: "stopped", generation: null, changedAt: null, source: null, reason: null, readError };
}

/** raw is the file content, or null when the file does not exist. */
export function parseLatch(raw: string | null): OfficeLatch {
  if (raw === null) {
    return { state: "running", generation: 0, changedAt: null, source: null, reason: null };
  }
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return closed("unparseable");
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) return closed("not_an_object");
  const o = v as Record<string, unknown>;
  if (o.version !== LATCH_VERSION) return closed("unknown_version");
  if (o.state !== "running" && o.state !== "stopped") return closed("unknown_state");
  const g = o.generation;
  if (typeof g !== "number" || !Number.isSafeInteger(g) || g < 0) return closed("bad_generation");
  const str = (x: unknown) => (typeof x === "string" ? x : null);
  return {
    state: o.state,
    generation: g,
    changedAt: str(o.changedAt),
    source: str(o.source),
    reason: str(o.reason),
  };
}

export function readLatch(home: string): OfficeLatch {
  let raw: string | null;
  try {
    raw = readFileSync(latchPath(home), "utf8");
  } catch (err) {
    const code = (err as NodeJS.ErrnoException).code;
    if (code !== "ENOENT") return closed(`read_failed:${code ?? "unknown"}`);
    raw = null;
  }
  return parseLatch(raw);
}

/** Atomic replace that survives a power cut: temp file, fsync, rename, dir fsync. */
export function writeDurable(file: string, content: string): void {
  mkdirSync(dirname(file), { recursive: true, mode: 0o700 });
  const tmp = `${file}.${process.pid}.${Date.now()}.tmp`;
  const fd = openSync(tmp, "w", 0o600);
  try {
    writeSync(fd, content);
    fsyncSync(fd);
  } finally {
    closeSync(fd);
  }
  renameSync(tmp, file);
  const dir = openSync(dirname(file), "r");
  try {
    fsyncSync(dir);
  } finally {
    closeSync(dir);
  }
}

function write(home: string, state: OfficeLatch["state"], generation: number, source: string, reason: string): OfficeLatch {
  const next = { version: LATCH_VERSION, state, generation, changedAt: new Date().toISOString(), source, reason };
  const body = JSON.stringify(next);
  writeDurable(latchPath(home), body + "\n");
  return parseLatch(body);
}

function nextGeneration(cur: OfficeLatch, home: string): number {
  if (cur.generation !== null) return cur.generation + 1;
  // Unreadable latch: never reuse a number a reader may have seen.
  try {
    return Math.max(1, Math.floor(statSync(latchPath(home)).mtimeMs));
  } catch {
    return 1;
  }
}

/** Stop always wins. Pressing it again keeps the current stopped generation. */
export function stopLatch(home: string, source = "desktop", reason = "stop"): OfficeLatch {
  const cur = readLatch(home);
  if (cur.state === "stopped" && cur.generation !== null && !cur.readError) return cur;
  return write(home, "stopped", nextGeneration(cur, home), source, reason);
}

export function resumeLatch(
  home: string,
  expectGeneration: number | null,
  source = "desktop",
  reason = "resume",
): { ok: true; latch: OfficeLatch } | { ok: false; latch: OfficeLatch } {
  const cur = readLatch(home);
  if (cur.state !== "stopped" || cur.readError || cur.generation === null || cur.generation !== expectGeneration) {
    return { ok: false, latch: cur };
  }
  return { ok: true, latch: write(home, "running", cur.generation + 1, source, reason) };
}
