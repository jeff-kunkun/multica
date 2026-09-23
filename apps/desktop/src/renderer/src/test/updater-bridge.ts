import { vi } from "vitest";
import type {
  UpdateAvailablePayload,
  UpdateCheckRecord,
  UpdateDownloadProgressPayload,
  UpdaterCapabilities,
  UpdaterCheckingPayload,
  UpdaterErrorPayload,
} from "../../../shared/updater-types";
import { useUpdaterStore } from "../stores/updater-store";

type Listener<T> = (payload: T) => void;

/**
 * A fake `window.updater` for renderer tests. Records every subscription so a
 * test can push IPC events exactly the way the preload bridge would, and
 * exposes vi.fn()s for every invoke-style method.
 */
export interface UpdaterBridgeFake {
  emit: {
    checking: (payload: UpdaterCheckingPayload) => void;
    checkResult: (record: UpdateCheckRecord) => void;
    updateAvailable: (info: UpdateAvailablePayload) => void;
    downloadProgress: (progress: UpdateDownloadProgressPayload) => void;
    updateDownloaded: (info: UpdateAvailablePayload) => void;
    error: (error: UpdaterErrorPayload) => void;
  };
  fns: {
    downloadUpdate: ReturnType<typeof vi.fn>;
    installUpdate: ReturnType<typeof vi.fn>;
    getCapabilities: ReturnType<typeof vi.fn>;
    getLastCheck: ReturnType<typeof vi.fn>;
    openLogFile: ReturnType<typeof vi.fn>;
    getPreferences: ReturnType<typeof vi.fn>;
    setAutomaticUpdates: ReturnType<typeof vi.fn>;
    setReleaseChannel: ReturnType<typeof vi.fn>;
    checkForUpdates: ReturnType<typeof vi.fn>;
    openExternal: ReturnType<typeof vi.fn>;
  };
  /** How many live listeners exist per channel — proves cleanup ran. */
  listenerCounts: () => Record<string, number>;
}

export const SUPPORTED_CAPABILITIES: UpdaterCapabilities = {
  autoUpdateSupported: true,
  blocker: null,
  releasePageUrl: "https://github.com/jeff-kunkun/multica/releases/latest",
  logPath: "/logs/main.log",
};

export const MAC_UNSIGNED_CAPABILITIES: UpdaterCapabilities = {
  ...SUPPORTED_CAPABILITIES,
  autoUpdateSupported: false,
  blocker: "mac-unsigned",
};

export function installUpdaterBridge(
  overrides: { capabilities?: UpdaterCapabilities; lastCheck?: UpdateCheckRecord | null } = {},
): UpdaterBridgeFake {
  useUpdaterStore.getState().reset();

  const listeners: Record<string, Set<Listener<never>>> = {};
  const subscribe =
    <T>(channel: string) =>
    (listener: Listener<T>) => {
      const set = (listeners[channel] ??= new Set());
      set.add(listener as Listener<never>);
      return () => {
        set.delete(listener as Listener<never>);
      };
    };
  const emitTo =
    <T>(channel: string) =>
    (payload: T) => {
      for (const listener of listeners[channel] ?? []) {
        (listener as Listener<T>)(payload);
      }
    };

  const fns = {
    downloadUpdate: vi.fn().mockResolvedValue(undefined),
    installUpdate: vi.fn().mockResolvedValue(undefined),
    getCapabilities: vi
      .fn()
      .mockResolvedValue(overrides.capabilities ?? SUPPORTED_CAPABILITIES),
    getLastCheck: vi.fn().mockResolvedValue(overrides.lastCheck ?? null),
    openLogFile: vi.fn().mockResolvedValue({ success: true }),
    getPreferences: vi
      .fn()
      .mockResolvedValue({ automaticUpdates: true, releaseChannel: "stable" }),
    setAutomaticUpdates: vi.fn(),
    setReleaseChannel: vi.fn(),
    checkForUpdates: vi.fn(),
    openExternal: vi.fn().mockResolvedValue(undefined),
  };

  Object.defineProperty(window, "desktopAPI", {
    configurable: true,
    value: { appInfo: { version: "1.2.3", os: "macos" }, openExternal: fns.openExternal },
  });
  Object.defineProperty(window, "updater", {
    configurable: true,
    value: {
      onChecking: subscribe<UpdaterCheckingPayload>("checking"),
      onCheckResult: subscribe<UpdateCheckRecord>("check-result"),
      onUpdateAvailable: subscribe<UpdateAvailablePayload>("update-available"),
      onDownloadProgress: subscribe<UpdateDownloadProgressPayload>("download-progress"),
      onUpdateDownloaded: subscribe<UpdateAvailablePayload>("update-downloaded"),
      onError: subscribe<UpdaterErrorPayload>("error"),
      ...fns,
    },
  });

  return {
    emit: {
      checking: emitTo("checking"),
      checkResult: emitTo("check-result"),
      updateAvailable: emitTo("update-available"),
      downloadProgress: emitTo("download-progress"),
      updateDownloaded: emitTo("update-downloaded"),
      error: emitTo("error"),
    },
    fns,
    listenerCounts: () =>
      Object.fromEntries(
        Object.entries(listeners).map(([channel, set]) => [channel, set.size]),
      ),
  };
}
