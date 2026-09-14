"use client";

import { useCallback, useEffect, useState } from "react";
import {
  canSetLocalDirectorySharedOverride,
  listLocalDirectorySharedOverrides,
  localDirectoryOverrideKey,
  setLocalDirectorySharedOverride,
} from "./local-directory";

/**
 * Local skip-mutex overrides for folders stored as in_place on a server that
 * does not accept execution_mode=shared (official cloud). Empty on web.
 */
export function useLocalDirectorySharedOverrides() {
  const [keys, setKeys] = useState<Set<string>>(() => new Set());
  const canPersist = canSetLocalDirectorySharedOverride();

  const refresh = useCallback(async () => {
    if (!canPersist) {
      setKeys(new Set());
      return;
    }
    const rows = await listLocalDirectorySharedOverrides();
    setKeys(
      new Set(
        rows.map((row) => localDirectoryOverrideKey(row.daemonId, row.localPath)),
      ),
    );
  }, [canPersist]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const hasOverride = useCallback(
    (daemonId: string | null | undefined, localPath: string) => {
      if (!daemonId) return false;
      return keys.has(localDirectoryOverrideKey(daemonId, localPath));
    },
    [keys],
  );

  const setOverride = useCallback(
    async (daemonId: string, localPath: string, enabled: boolean) => {
      const result = await setLocalDirectorySharedOverride({
        daemonId,
        localPath,
        enabled,
      });
      await refresh();
      return result;
    },
    [refresh],
  );

  return { canPersist, hasOverride, setOverride, refresh };
}
