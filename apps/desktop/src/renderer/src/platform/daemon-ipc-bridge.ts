"use client";

import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { runtimeKeys } from "@multica/core/runtimes";
import type { AgentRuntime, PlanLimitsSnapshot } from "@multica/core/types";

/**
 * DesktopAPI exposes a richer DaemonStatus shape than the public AgentRuntime
 * type — we redeclare the fields we consume here to avoid coupling the bridge
 * to the desktop preload typings (which live in apps/desktop/src/preload).
 */
export interface DaemonStatusLike {
  state:
    | "running"
    | "stopped"
    | "starting"
    | "stopping"
    | "installing_cli"
    | "cli_not_found"
    | "recovery_paused"
    | "auth_expired";
  daemonId?: string;
  planLimits?: Record<string, PlanLimitsSnapshot>;
  agyLoggedInDirs?: string[];
}

/**
 * Merges a local DaemonStatus into an AgentRuntime row. Status flips stay
 * server-compatible; plan_limits from /health overlay live Claude/Codex 5h/7d
 * windows that official cloud APIs do not persist. Custom-profile runtimes
 * keep the server snapshot because they may use a different provider account.
 */
export function applyLocalDaemonStatus(
  rt: AgentRuntime,
  status: DaemonStatusLike,
): AgentRuntime {
  let next = mergeDaemonStatus(rt, status);
  next = mergeLocalPlanLimits(next, status);
  next = mergeAgyLoggedInDirs(next, status);
  return next;
}

function mergeDaemonStatus(rt: AgentRuntime, status: DaemonStatusLike): AgentRuntime {
  if (
    status.state === "stopped" ||
    status.state === "stopping" ||
    status.state === "recovery_paused" ||
    status.state === "auth_expired"
  ) {
    return { ...rt, status: "offline" };
  }
  if (status.state === "running") {
    return {
      ...rt,
      status: "online",
      last_seen_at: new Date().toISOString(),
    };
  }
  return rt;
}

function mergeLocalPlanLimits(
  rt: AgentRuntime,
  status: DaemonStatusLike,
): AgentRuntime {
  if (rt.profile_id) return rt;
  const overlay = status.planLimits?.[rt.provider];
  if (!overlay || overlay.observed_at <= 0) return rt;
  return { ...rt, plan_limits: overlay };
}

function mergeAgyLoggedInDirs(
  rt: AgentRuntime,
  status: DaemonStatusLike,
): AgentRuntime {
  if (!status.agyLoggedInDirs) return rt;
  return {
    ...rt,
    metadata: {
      ...rt.metadata,
      agy_logged_in_dirs: status.agyLoggedInDirs,
    },
  };
}

/**
 * Subscribes to local daemon status changes via Electron IPC and writes them
 * into the runtimes Query cache for the active workspace.
 *
 * Why: the server-side runtime sweeper takes up to 75s to flip a runtime to
 * offline (heartbeat timeout 45s + sweep interval 30s). On the desktop app
 * we know about local daemon state instantly via IPC, so we use it to
 * pre-populate the cache and give users a sub-second feedback loop. Web and
 * "looking at someone else's daemon" still go through the server path.
 *
 * Same-daemon-multiple-runtimes: a single daemon can back several runtimes
 * in the same workspace (one per provider). We map across all matches so
 * every related runtime row sees the same status flip and quota overlay.
 */
export function useDaemonIPCBridge(wsId: string | undefined): void {
  const qc = useQueryClient();

  useEffect(() => {
    if (!wsId) return;
    if (typeof window === "undefined") return;
    const daemonAPI = (
      window as unknown as {
        daemonAPI?: {
          onStatusChange?: (cb: (s: DaemonStatusLike) => void) => () => void;
        };
      }
    ).daemonAPI;
    if (!daemonAPI?.onStatusChange) return;

    const unsubscribe = daemonAPI.onStatusChange((status) => {
      if (!status.daemonId) return;
      qc.setQueryData<AgentRuntime[]>(runtimeKeys.list(wsId), (old) => {
        if (!old) return old;
        return old.map((rt) =>
          rt.daemon_id === status.daemonId
            ? applyLocalDaemonStatus(rt, status)
            : rt,
        );
      });
    });

    return unsubscribe;
  }, [wsId, qc]);
}
