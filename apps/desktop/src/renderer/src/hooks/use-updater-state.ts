import { useEffect, useState } from "react";
import {
  DESKTOP_RELEASES_PAGE_URL,
  clampUpdatePercent,
  type ManualUpdateCheckResult,
  type UpdateAvailableInfo,
  type UpdateCheckRecord,
  type UpdaterErrorInfo,
  type UpdaterPhase,
  type UpdaterSnapshot,
} from "../../../shared/updater-types";

export interface UpdaterViewState {
  phase: UpdaterPhase;
  version: string | null;
  percent: number | null;
  errorMessage: string | null;
  manualDownloadRequired: boolean;
  releasePageUrl: string;
  lastCheck: UpdateCheckRecord | null;
  checking: boolean;
}

type Listener = (state: UpdaterViewState) => void;

function createState(): UpdaterViewState {
  return {
    phase: "idle",
    version: null,
    percent: null,
    errorMessage: null,
    manualDownloadRequired: false,
    releasePageUrl: DESKTOP_RELEASES_PAGE_URL,
    lastCheck: null,
    checking: false,
  };
}

let state = createState();
let serial = 0;
const listeners = new Set<Listener>();
let started = false;
let stopIpc: (() => void) | null = null;

function emit(): void {
  for (const listener of listeners) listener(state);
}

function patch(partial: Partial<UpdaterViewState>): void {
  state = { ...state, ...partial };
  serial += 1;
  emit();
}

function applySnapshot(snapshot: UpdaterSnapshot): void {
  state = {
    ...state,
    phase: snapshot.phase,
    version: snapshot.version,
    percent: snapshot.percent,
    errorMessage: snapshot.error,
    manualDownloadRequired: snapshot.manualDownloadRequired,
    releasePageUrl: snapshot.releasePageUrl,
    lastCheck: snapshot.lastCheck,
  };
  emit();
}

function onAvailable(info: UpdateAvailableInfo): void {
  const manualDownloadRequired = info.manualDownloadRequired;
  const releasePageUrl = info.releasePageUrl || state.releasePageUrl;
  if (state.phase === "downloading" || state.phase === "downloaded") {
    patch({
      manualDownloadRequired,
      releasePageUrl,
      version: state.version ?? info.version,
    });
    return;
  }
  patch({
    phase: "available",
    version: info.version,
    percent: null,
    errorMessage: null,
    manualDownloadRequired,
    releasePageUrl,
    checking: false,
  });
}

function onProgress(progress: { percent: number }): void {
  if (state.manualDownloadRequired) return;
  patch({
    phase: "downloading",
    percent: clampUpdatePercent(progress.percent),
    errorMessage: null,
    checking: false,
  });
}

function onDownloaded(info: { version: string }): void {
  patch({
    phase: "downloaded",
    version: info.version,
    percent: 100,
    errorMessage: null,
    checking: false,
  });
}

function onUpdaterError(info: UpdaterErrorInfo): void {
  patch({
    phase: "error",
    errorMessage: info.message,
    percent: null,
    checking: false,
  });
}

function onCheckResult(record: UpdateCheckRecord): void {
  const base: Partial<UpdaterViewState> = { lastCheck: record, checking: false };
  if (!record.ok) {
    if (state.phase === "downloading" || state.phase === "downloaded") {
      patch(base);
      return;
    }
    patch({
      ...base,
      phase: "error",
      errorMessage: record.error ?? "Update check failed",
    });
    return;
  }
  if (record.available) {
    const version = record.latestVersion ?? state.version;
    if (state.phase === "idle" || state.phase === "checking") {
      patch({
        ...base,
        phase: "available",
        version,
        errorMessage: null,
      });
      return;
    }
    patch({ ...base, version: state.version ?? version });
    return;
  }
  if (state.phase === "downloading" || state.phase === "downloaded") {
    patch(base);
    return;
  }
  patch({
    ...base,
    phase: "idle",
    version: null,
    percent: null,
    errorMessage: null,
  });
}

function manualRecord(result: ManualUpdateCheckResult): UpdateCheckRecord {
  const checkedAt = new Date().toISOString();
  if (!result.ok) {
    return { checkedAt, source: "manual", ok: false, error: result.error };
  }
  return {
    checkedAt,
    source: "manual",
    ok: true,
    available: result.available,
    latestVersion: result.latestVersion,
    currentVersion: result.currentVersion,
  };
}

async function hydrate(): Promise<void> {
  const seen = serial;
  try {
    const snapshot = await window.updater.getSnapshot();
    if (seen !== serial) return;
    applySnapshot(snapshot);
  } catch {
    // Live IPC events still move the state when the snapshot cannot be read.
  }
}

function ensureStarted(): void {
  if (started) return;
  started = true;
  // Both surfaces share these subscriptions. Removing one leaves that event
  // invisible in the notification card and the settings tab together.
  const unsubs = [
    window.updater.onUpdateAvailable(onAvailable),
    window.updater.onDownloadProgress(onProgress),
    window.updater.onUpdateDownloaded(onDownloaded),
    window.updater.onUpdaterError(onUpdaterError),
    window.updater.onCheckResult(onCheckResult),
  ];
  stopIpc = () => {
    for (const unsub of unsubs) unsub();
  };
  void hydrate();
}

function subscribe(listener: Listener): () => void {
  listeners.add(listener);
  ensureStarted();
  listener(state);
  return () => {
    listeners.delete(listener);
  };
}

export function canOfferInAppDownload(view: UpdaterViewState): boolean {
  if (view.manualDownloadRequired) return false;
  if (view.phase === "available") return true;
  return view.phase === "error" && Boolean(view.version);
}

export function shouldOfferReleasePage(view: UpdaterViewState): boolean {
  if (!view.manualDownloadRequired) return false;
  return view.phase === "available" || view.phase === "error";
}

export async function requestUpdateCheck(): Promise<void> {
  const preserve = state.phase === "downloading" || state.phase === "downloaded";
  patch({ checking: true, phase: preserve ? state.phase : "checking" });
  try {
    onCheckResult(manualRecord(await window.updater.checkForUpdates()));
  } catch (err) {
    onCheckResult({
      checkedAt: new Date().toISOString(),
      source: "manual",
      ok: false,
      error: err instanceof Error ? err.message : String(err),
    });
  }
}

export async function requestDownload(): Promise<void> {
  if (state.manualDownloadRequired) return;
  patch({
    phase: "downloading",
    percent: state.percent ?? 0,
    errorMessage: null,
  });
  try {
    await window.updater.downloadUpdate();
  } catch (err) {
    patch({
      phase: "error",
      errorMessage: err instanceof Error ? err.message : String(err),
    });
  }
}

export function requestInstall(): Promise<void> {
  return window.updater.installUpdate();
}

export function requestOpenReleasePage(): Promise<void> {
  return window.desktopAPI.openExternal(state.releasePageUrl);
}

export function useUpdaterState(): {
  state: UpdaterViewState;
  checkForUpdates: () => Promise<void>;
  downloadUpdate: () => Promise<void>;
  installUpdate: () => Promise<void>;
  openReleasePage: () => Promise<void>;
} {
  const [view, setView] = useState(state);
  useEffect(() => subscribe(setView), []);
  return {
    state: view,
    checkForUpdates: requestUpdateCheck,
    downloadUpdate: requestDownload,
    installUpdate: requestInstall,
    openReleasePage: requestOpenReleasePage,
  };
}

/** Drops the shared subscription so the next render binds the current IPC mock. */
export function resetUpdaterStoreForTests(): void {
  stopIpc?.();
  stopIpc = null;
  started = false;
  serial = 0;
  state = createState();
  listeners.clear();
}
