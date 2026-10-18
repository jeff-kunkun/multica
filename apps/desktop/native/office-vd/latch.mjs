// latch.mjs: the device stop latch shared by everything that acts for office agents on this Mac.
// One file, <vd home>/control/latch.json, is the only source of truth:
//   {"version":1,"state":"running"|"stopped","generation":<int>,"changedAt":<iso>,"source":<str>,"reason":<str>}
// The broker, the daemon (Go, server/internal/daemon/stop_latch.go) and the desktop app
// (apps/desktop/src/main/office-screen/latch.ts) all read it on their own and all apply the same rules,
// pinned by test/latch-fixtures.json:
//   - no file: running, generation 0 (a Mac that never pressed stop)
//   - anything unreadable or unknown: stopped (fail closed)
// Every change bumps the generation. Stop always wins; resume must name the generation it saw,
// so a stale resume can never undo a newer stop. Nothing resumes by itself on restart or reconnect.
import fs from 'fs'; import os from 'os'; import path from 'path'; import { pathToFileURL } from 'url';

export const LATCH_VERSION = 1;

export function vdHome(env = process.env) {
  return env.OFFICE_VD_HOME || path.join(os.homedir(), '.agents', 'vd');
}

export function latchPath(home = vdHome()) {
  return path.join(home, 'control', 'latch.json');
}

// parseLatch turns raw file content (null when the file does not exist) into a latch state.
export function parseLatch(raw) {
  if (raw === null) return { state: 'running', generation: 0, changedAt: null, source: null, reason: null };
  const closed = readError => ({ state: 'stopped', generation: null, changedAt: null, source: null, reason: null, readError });
  let v;
  try { v = JSON.parse(raw); } catch { return closed('unparseable'); }
  if (!v || typeof v !== 'object' || Array.isArray(v)) return closed('not_an_object');
  if (v.version !== LATCH_VERSION) return closed('unknown_version');
  if (v.state !== 'running' && v.state !== 'stopped') return closed('unknown_state');
  if (!Number.isSafeInteger(v.generation) || v.generation < 0) return closed('bad_generation');
  return {
    state: v.state, generation: v.generation,
    changedAt: typeof v.changedAt === 'string' ? v.changedAt : null,
    source: typeof v.source === 'string' ? v.source : null,
    reason: typeof v.reason === 'string' ? v.reason : null,
  };
}

export function readLatch(home = vdHome()) {
  let raw;
  try { raw = fs.readFileSync(latchPath(home), 'utf8'); } catch (e) {
    if (e.code === 'ENOENT') raw = null;
    else return { ...parseLatch('{}'), readError: `read_failed:${e.code || 'unknown'}` };
  }
  return parseLatch(raw);
}

// writeDurable replaces a file atomically and makes the rename survive a power cut.
export function writeDurable(file, content) {
  fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
  const tmp = `${file}.${process.pid}.${Date.now()}.tmp`;
  const fd = fs.openSync(tmp, 'w', 0o600);
  try { fs.writeSync(fd, content); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
  fs.renameSync(tmp, file);
  const dir = fs.openSync(path.dirname(file), 'r');
  try { fs.fsyncSync(dir); } finally { fs.closeSync(dir); }
}

function write(home, state, generation, source, reason) {
  const next = { version: LATCH_VERSION, state, generation, changedAt: new Date().toISOString(), source, reason };
  writeDurable(latchPath(home), JSON.stringify(next) + '\n');
  return parseLatch(JSON.stringify(next));
}

// nextGeneration never reuses a number, even after an unreadable latch.
function nextGeneration(cur, home) {
  if (cur.generation !== null) return cur.generation + 1;
  try { return Math.max(1, Math.floor(fs.statSync(latchPath(home)).mtimeMs)); } catch { return 1; }
}

export function stop({ home = vdHome(), source = 'cli', reason = 'stop' } = {}) {
  const cur = readLatch(home);
  if (cur.state === 'stopped' && cur.generation !== null && !cur.readError) return cur;
  return write(home, 'stopped', nextGeneration(cur, home), source, reason);
}

// resume only succeeds against the exact stopped generation the caller looked at.
export function resume({ home = vdHome(), expectGeneration, source = 'cli', reason = 'resume' } = {}) {
  const cur = readLatch(home);
  if (cur.state !== 'stopped' || cur.readError || cur.generation !== expectGeneration) {
    return { ok: false, latch: cur, error: 'generation_mismatch' };
  }
  return { ok: true, latch: write(home, 'running', cur.generation + 1, source, reason) };
}

// CLI: node latch.mjs status | stop [reason] | resume <generation>
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [cmd, arg] = process.argv.slice(2);
  if (cmd === 'stop') console.log(JSON.stringify(stop({ reason: arg || 'stop' })));
  else if (cmd === 'resume') {
    const r = resume({ expectGeneration: Number(arg) });
    console.log(JSON.stringify(r)); process.exit(r.ok ? 0 : 1);
  } else if (cmd === 'status' || !cmd) console.log(JSON.stringify(readLatch()));
  else { console.error('usage: latch.mjs status | stop [reason] | resume <generation>'); process.exit(1); }
}
