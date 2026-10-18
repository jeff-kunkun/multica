"use client";

import { useCallback, useEffect, useSyncExternalStore } from "react";

// Desktop-only: the office stop latch, its receipts and the live picture of
// the virtual display (DENE-2261). Types are redeclared here so views don't
// depend on the desktop preload; on web or an older desktop build there is no
// `desktopAPI.officeScreen`, the state stays null and callers render nothing.

export type OfficeStopStep = "done" | "pending" | "not_running" | "outdated";

export interface OfficeScreenState {
  supported: boolean;
  latch: {
    state: "running" | "stopped";
    generation: number | null;
    changedAt: string | null;
    readError?: string;
  };
  receipt: {
    generation: number | null;
    requested: OfficeStopStep;
    inputSealed: OfficeStopStep;
    tasksStopped: OfficeStopStep;
    activeTasks: number | null;
  } | null;
  view: {
    enabled: boolean;
    status: "off" | "starting" | "streaming" | "blocked" | "unavailable";
    reason: string | null;
    windows: number;
    lastFrameAt: number | null;
  };
  permission: "granted" | "missing" | "unknown";
}

export interface OfficeScreenFrame {
  seq: number;
  width: number;
  height: number;
  src: string;
  at: number;
}

export interface OfficePagePreview {
  targetId: string;
  src: string;
}

type ResumeResult =
  | { ok: true; state: OfficeScreenState }
  | { ok: false; error: string; state: OfficeScreenState };

interface OfficeScreenAPI {
  subscribe: () => Promise<OfficeScreenState>;
  unsubscribe: () => Promise<void>;
  stop: () => Promise<OfficeScreenState>;
  resume: (expectGeneration: number | null) => Promise<ResumeResult>;
  setViewEnabled: (enabled: boolean) => Promise<OfficeScreenState>;
  requestPermission: () => Promise<OfficeScreenState>;
  openPermissionSettings: () => Promise<void>;
  pagePreviews: () => Promise<OfficePagePreview[]>;
  onState: (cb: (s: OfficeScreenState) => void) => () => void;
  onFrame: (cb: (f: OfficeScreenFrame) => void) => () => void;
}

function readAPI(): OfficeScreenAPI | undefined {
  if (typeof window === "undefined") return undefined;
  return (window as unknown as { desktopAPI?: { officeScreen?: OfficeScreenAPI } })
    .desktopAPI?.officeScreen;
}

// One subscription for the whole renderer, however many panels are mounted.
interface Snapshot {
  state: OfficeScreenState | null;
  frame: OfficeScreenFrame | null;
}
let snapshot: Snapshot = { state: null, frame: null };
const listeners = new Set<() => void>();
let refs = 0;
let detach: (() => void) | null = null;

function publish(next: Partial<Snapshot>) {
  snapshot = { ...snapshot, ...next };
  for (const l of listeners) l();
}

function acquire(api: OfficeScreenAPI) {
  if (refs++ > 0) return;
  const offState = api.onState((state) => publish({ state }));
  const offFrame = api.onFrame((frame) => publish({ frame }));
  detach = () => {
    offState();
    offFrame();
  };
  api.subscribe().then(
    (state) => publish({ state }),
    () => {},
  );
}

function release(api: OfficeScreenAPI) {
  if (--refs > 0) return;
  detach?.();
  detach = null;
  void api.unsubscribe().catch(() => {});
}

/** Test hook: forget the shared subscription between tests. */
export function resetOfficeScreenStore() {
  snapshot = { state: null, frame: null };
  listeners.clear();
  refs = 0;
  detach = null;
}

export interface OfficeScreenControls {
  available: boolean;
  state: OfficeScreenState | null;
  frame: OfficeScreenFrame | null;
  stop: () => Promise<void>;
  /** false when the latch changed since it was last seen. */
  resume: () => Promise<boolean>;
  setViewEnabled: (enabled: boolean) => Promise<void>;
  requestPermission: () => Promise<void>;
  openPermissionSettings: () => void;
  pagePreviews: () => Promise<OfficePagePreview[]>;
}

export function useOfficeScreen(): OfficeScreenControls {
  const api = readAPI();
  const snap = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => snapshot,
    () => snapshot,
  );

  useEffect(() => {
    if (!api) return;
    acquire(api);
    return () => release(api);
  }, [api]);

  const stop = useCallback(async () => {
    if (api) publish({ state: await api.stop() });
  }, [api]);
  const resume = useCallback(async () => {
    if (!api) return false;
    const r = await api.resume(snapshot.state?.latch.generation ?? null);
    publish({ state: r.state });
    return r.ok;
  }, [api]);
  const setViewEnabled = useCallback(async (enabled: boolean) => {
    if (api) publish({ state: await api.setViewEnabled(enabled), ...(enabled ? {} : { frame: null }) });
  }, [api]);
  const requestPermission = useCallback(async () => {
    if (api) publish({ state: await api.requestPermission() });
  }, [api]);
  const openPermissionSettings = useCallback(() => {
    void api?.openPermissionSettings();
  }, [api]);
  const pagePreviews = useCallback(async () => (api ? api.pagePreviews() : []), [api]);

  return {
    available: Boolean(api),
    state: snap.state,
    frame: snap.frame,
    stop,
    resume,
    setViewEnabled,
    requestPermission,
    openPermissionSettings,
    pagePreviews,
  };
}
