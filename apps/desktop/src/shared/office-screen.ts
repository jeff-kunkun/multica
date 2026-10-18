/**
 * The office screen (DENE-2261): the device stop latch for office agents on
 * this Mac, the receipts that show the stop took effect, and the live picture
 * of the virtual display they work on.
 */

export interface OfficeLatch {
  state: "running" | "stopped";
  /** null when the latch file can't be read (which reads as stopped). */
  generation: number | null;
  changedAt: string | null;
  source: string | null;
  reason: string | null;
  readError?: string;
}

/**
 * One step of the stop receipt:
 * - done: confirmed by that process reading the stopped generation
 * - pending: that process hasn't caught up yet
 * - not_running: the process isn't running; it reads the latch when it starts
 * - outdated: that process is too old to know about the latch — the stop does
 *   NOT reach it, so the receipt is incomplete
 */
export type OfficeStopStep = "done" | "pending" | "not_running" | "outdated";

export interface OfficeStopReceipt {
  generation: number | null;
  /** The latch file says stopped. */
  requested: OfficeStopStep;
  /** The browser broker refuses input under this generation. */
  inputSealed: OfficeStopStep;
  /** The daemon claims nothing and has no run left. */
  tasksStopped: OfficeStopStep;
  /** Runs still exiting, when the daemon reports them. */
  activeTasks: number | null;
}

export type OfficeViewStatus =
  | "off"
  | "starting"
  | "streaming"
  | "blocked"
  | "unavailable";

export interface OfficeViewState {
  /** Kun's choice to show the picture; persisted, independent of stop. */
  enabled: boolean;
  status: OfficeViewStatus;
  /**
   * Why there is no picture: permission, vd_missing, vd_is_main,
   * not_office_display, unowned_window, window_straddles_display,
   * no_windows, helper_missing, helper_exited, stale, ...
   */
  reason: string | null;
  windows: number;
  lastFrameAt: number | null;
}

export interface OfficeScreenState {
  /** macOS with the office tools bundled. */
  supported: boolean;
  latch: OfficeLatch;
  /** Present while stopped. */
  receipt: OfficeStopReceipt | null;
  view: OfficeViewState;
  permission: "granted" | "missing" | "unknown";
}

export interface OfficeScreenFrame {
  seq: number;
  width: number;
  height: number;
  /** data: URL of a JPEG. */
  src: string;
  at: number;
}

export type OfficeResumeResult =
  | { ok: true; state: OfficeScreenState }
  | { ok: false; error: "generation_mismatch" | "unsupported"; state: OfficeScreenState };

/** Degraded picture: a screenshot of one page the agents opened. */
export interface OfficePagePreview {
  targetId: string;
  src: string;
}
