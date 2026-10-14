"use client";

import { useCallback, useEffect, useState } from "react";

// Desktop-only: whether macOS gave the app Full Disk Access. Redeclared here
// so views don't depend on the desktop preload types; on web (no
// `window.desktopAPI`) or an older desktop build the status stays
// "unsupported" and callers render nothing.

export interface FullDiskAccess {
  status: "granted" | "missing" | "unsupported";
  appName: string;
}

interface DesktopFullDiskAccessAPI {
  getFullDiskAccess?: () => Promise<FullDiskAccess>;
  openFullDiskAccessSettings?: () => Promise<void>;
}

function readDesktopAPI(): DesktopFullDiskAccessAPI | undefined {
  if (typeof window === "undefined") return undefined;
  return (window as unknown as { desktopAPI?: DesktopFullDiskAccessAPI })
    .desktopAPI;
}

const UNSUPPORTED: FullDiskAccess = { status: "unsupported", appName: "" };

/**
 * Checks on mount and again whenever the window regains focus, so the hint
 * disappears as soon as someone comes back from System Settings.
 */
export function useFullDiskAccess(): {
  access: FullDiskAccess;
  openSettings: () => void;
} {
  const [access, setAccess] = useState<FullDiskAccess>(UNSUPPORTED);

  useEffect(() => {
    const api = readDesktopAPI();
    if (!api?.getFullDiskAccess) return;
    let cancelled = false;
    const check = () => {
      api.getFullDiskAccess?.().then(
        (next) => {
          if (!cancelled) setAccess(next);
        },
        () => {
          // Leave the last known state; the next focus retries.
        },
      );
    };
    check();
    window.addEventListener("focus", check);
    return () => {
      cancelled = true;
      window.removeEventListener("focus", check);
    };
  }, []);

  const openSettings = useCallback(() => {
    void readDesktopAPI()?.openFullDiskAccessSettings?.();
  }, []);

  return { access, openSettings };
}
