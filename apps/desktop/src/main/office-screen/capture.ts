import { spawn as nodeSpawn, type ChildProcessWithoutNullStreams } from "child_process";
import type { OfficeScreenFrame, OfficeViewStatus } from "../../shared/office-screen";

// Runs the ScreenCaptureKit helper (native/office-vd/src/screen-capture.swift)
// while someone is looking, and turns its JSON lines into frames and status.
// The helper exits when its stdin closes, so it can't outlive this process.

export interface CaptureStatus {
  status: OfficeViewStatus;
  reason: string | null;
  windows: number;
}

export interface CaptureEvents {
  onFrame: (frame: OfficeScreenFrame) => void;
  onStatus: (status: CaptureStatus) => void;
}

type Spawn = (cmd: string, args: string[]) => ChildProcessWithoutNullStreams;

/** No heartbeat for this long means the helper is wedged; restart it. */
export const HEARTBEAT_TIMEOUT_MS = 5_000;
const RESTART_DELAY_MS = 3_000;

/** Splits a byte stream into lines; frames are long single lines. */
export function lineSplitter(onLine: (line: string) => void): (chunk: Buffer | string) => void {
  let buf = "";
  return (chunk) => {
    buf += chunk.toString();
    let i: number;
    while ((i = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, i);
      buf = buf.slice(i + 1);
      if (line.trim()) onLine(line);
    }
  };
}

export class CaptureProcess {
  private child: ChildProcessWithoutNullStreams | null = null;
  private wanted = false;
  private lastBeat = 0;
  private watchdog: ReturnType<typeof setInterval> | null = null;
  private restart: ReturnType<typeof setTimeout> | null = null;

  constructor(
    private readonly helper: string,
    private readonly args: string[],
    private readonly events: CaptureEvents,
    private readonly spawn: Spawn = (cmd, a) => nodeSpawn(cmd, a, { stdio: ["pipe", "pipe", "pipe"] }),
    private readonly now: () => number = Date.now,
  ) {}

  get running(): boolean {
    return this.child !== null;
  }

  start(): void {
    this.wanted = true;
    if (this.child || this.restart) return;
    this.launch();
  }

  stop(): void {
    this.wanted = false;
    if (this.restart) clearTimeout(this.restart);
    this.restart = null;
    this.kill();
    this.events.onStatus({ status: "off", reason: null, windows: 0 });
  }

  private launch(): void {
    this.events.onStatus({ status: "starting", reason: null, windows: 0 });
    let child: ChildProcessWithoutNullStreams;
    try {
      child = this.spawn(this.helper, this.args);
    } catch {
      this.events.onStatus({ status: "unavailable", reason: "helper_missing", windows: 0 });
      return;
    }
    this.child = child;
    this.lastBeat = this.now();
    child.stdout.on("data", lineSplitter((line) => this.onLine(line)));
    child.stderr.on("data", () => {});
    child.on("error", () => {});
    child.on("exit", () => {
      if (this.child !== child) return;
      this.child = null;
      this.clearWatchdog();
      if (!this.wanted) return;
      this.events.onStatus({ status: "blocked", reason: "helper_exited", windows: 0 });
      this.restart = setTimeout(() => {
        this.restart = null;
        if (this.wanted) this.launch();
      }, RESTART_DELAY_MS);
    });
    this.watchdog = setInterval(() => {
      if (this.now() - this.lastBeat > HEARTBEAT_TIMEOUT_MS) {
        this.events.onStatus({ status: "blocked", reason: "stale", windows: 0 });
        this.child?.kill("SIGKILL");
      }
    }, 1_000);
  }

  private onLine(line: string): void {
    let m: Record<string, unknown>;
    try {
      m = JSON.parse(line) as Record<string, unknown>;
    } catch {
      return;
    }
    this.lastBeat = this.now();
    if (m.type === "frame" && typeof m.jpeg === "string") {
      this.events.onFrame({
        seq: Number(m.seq) || 0,
        width: Number(m.width) || 0,
        height: Number(m.height) || 0,
        src: `data:image/jpeg;base64,${m.jpeg}`,
        at: this.now(),
      });
    } else if (m.type === "status") {
      const state = m.state === "streaming" ? "streaming" : "blocked";
      this.events.onStatus({
        status: state,
        reason: typeof m.reason === "string" ? m.reason : null,
        windows: typeof m.windows === "number" ? m.windows : 0,
      });
    }
  }

  private kill(): void {
    this.clearWatchdog();
    const child = this.child;
    this.child = null;
    if (!child) return;
    try {
      child.stdin.end();
    } catch {
      // already gone
    }
    child.kill("SIGTERM");
  }

  private clearWatchdog(): void {
    if (this.watchdog) clearInterval(this.watchdog);
    this.watchdog = null;
  }
}
