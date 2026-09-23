import { create } from "zustand";
import type {
  UpdateCheckRecord,
  UpdateCheckTrigger,
  UpdaterCapabilities,
} from "../../../shared/updater-types";

/**
 * The one update state machine for the renderer. Both the corner
 * notification card and Settings → Updates read from here so they can never
 * disagree about whether a download is running or what failed.
 *
 *   idle → checking → up-to-date
 *                   → available → downloading(percent) → downloaded
 *                                                      → error (retryable)
 *
 * A check that runs while a download is in flight (the hourly poll) must not
 * knock the UI back to "checking": phases at or past `downloading` win over
 * check-driven transitions.
 */
export type UpdatePhase =
  | { status: "idle" }
  | { status: "checking"; trigger: UpdateCheckTrigger }
  | { status: "up-to-date" }
  | { status: "available"; version: string }
  | { status: "downloading"; version: string; percent: number }
  | { status: "downloaded"; version: string }
  | { status: "error"; message: string; version: string | null };

export interface UpdaterStoreState {
  phase: UpdatePhase;
  lastCheck: UpdateCheckRecord | null;
  capabilities: UpdaterCapabilities | null;
}

interface UpdaterStoreActions {
  checking: (trigger: UpdateCheckTrigger) => void;
  checkResult: (record: UpdateCheckRecord) => void;
  updateAvailable: (version: string) => void;
  downloadProgress: (percent: number) => void;
  downloadStarted: () => void;
  updateDownloaded: (version: string) => void;
  failed: (message: string) => void;
  setCapabilities: (capabilities: UpdaterCapabilities) => void;
  setLastCheck: (record: UpdateCheckRecord | null) => void;
  reset: () => void;
}

export type UpdaterStore = UpdaterStoreState & UpdaterStoreActions;

const INITIAL_STATE: UpdaterStoreState = {
  phase: { status: "idle" },
  lastCheck: null,
  capabilities: null,
};

function isDownloadPhase(phase: UpdatePhase): boolean {
  return phase.status === "downloading" || phase.status === "downloaded";
}

function knownVersion(phase: UpdatePhase): string | null {
  switch (phase.status) {
    case "available":
    case "downloading":
    case "downloaded":
      return phase.version;
    case "error":
      return phase.version;
    default:
      return null;
  }
}

export const useUpdaterStore = create<UpdaterStore>((set) => ({
  ...INITIAL_STATE,

  checking: (trigger) =>
    set((state) =>
      isDownloadPhase(state.phase) ? state : { phase: { status: "checking", trigger } },
    ),

  checkResult: (record) =>
    set((state) => {
      if (isDownloadPhase(state.phase)) return { lastCheck: record };
      if (!record.ok) {
        return {
          lastCheck: record,
          phase: { status: "error", message: record.error, version: null },
        };
      }
      return {
        lastCheck: record,
        phase: record.available
          ? { status: "available", version: record.latestVersion }
          : { status: "up-to-date" },
      };
    }),

  updateAvailable: (version) =>
    set((state) =>
      isDownloadPhase(state.phase) && knownVersion(state.phase) === version
        ? state
        : { phase: { status: "available", version } },
    ),

  downloadStarted: () =>
    set((state) => ({
      phase: {
        status: "downloading",
        version: knownVersion(state.phase) ?? "",
        percent: 0,
      },
    })),

  downloadProgress: (percent) =>
    set((state) => ({
      phase: {
        status: "downloading",
        version: knownVersion(state.phase) ?? "",
        percent: Math.max(0, Math.min(100, percent)),
      },
    })),

  updateDownloaded: (version) =>
    set({ phase: { status: "downloaded", version } }),

  failed: (message) =>
    set((state) => ({
      phase: { status: "error", message, version: knownVersion(state.phase) },
    })),

  setCapabilities: (capabilities) => set({ capabilities }),
  setLastCheck: (record) => set({ lastCheck: record }),
  reset: () => set(INITIAL_STATE),
}));
