import { useEffect, useState } from "react";
import type { UpdateSnapshot, UpdaterPreferences } from "../../../shared/updater-types";

const EMPTY_SNAPSHOT: UpdateSnapshot = {
  phase: "idle",
  version: null,
  percent: null,
  manualDownloadUrl: null,
  installMode: "manual",
  releaseChannel: "stable",
  error: null,
  errorCode: null,
};

const EMPTY_PREFERENCES: UpdaterPreferences = {
  automaticUpdates: true,
  releaseChannel: "stable",
};

export function useDesktopUpdate() {
  const [snapshot, setSnapshot] = useState<UpdateSnapshot>(EMPTY_SNAPSHOT);
  const [preferences, setPreferences] =
    useState<UpdaterPreferences>(EMPTY_PREFERENCES);
  const [preferencesReady, setPreferencesReady] = useState(false);

  useEffect(() => {
    let mounted = true;
    void window.updater
      .getPreferences()
      .then((next) => {
        if (mounted) setPreferences(next);
      })
      .catch(() => {
        // The main process falls back to enabled + stable when the file
        // cannot be read. Keep that default if IPC itself is unavailable.
      })
      .finally(() => {
        if (mounted) setPreferencesReady(true);
      });
    // A state event that arrives while getState is in flight is newer than
    // the snapshot that call will return. Don't let the late response
    // overwrite it.
    let sawLiveState = false;
    void window.updater
      .getState()
      .then((next) => {
        if (mounted && !sawLiveState) setSnapshot(next);
      })
      .catch(() => undefined);
    const unsubscribe = window.updater.onState((next) => {
      sawLiveState = true;
      if (mounted) setSnapshot(next);
    });
    return () => {
      mounted = false;
      unsubscribe();
    };
  }, []);

  return { snapshot, preferences, setPreferences, preferencesReady };
}
