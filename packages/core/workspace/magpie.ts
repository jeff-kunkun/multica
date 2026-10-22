import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { workspaceKeys } from "./queries";

/**
 * The workspace's Magpie switch, read from workspace.settings. On is the
 * office line (Magpie routes and groups); off — the default — behaves as
 * official Multica. Anything but an explicit `enabled: true` is off.
 */
export function isWorkspaceMagpieEnabled(settings: unknown): boolean {
  if (!settings || typeof settings !== "object") return false;
  const magpie = (settings as Record<string, unknown>).magpie;
  if (!magpie || typeof magpie !== "object") return false;
  return (magpie as Record<string, unknown>).enabled === true;
}

/** Flips the switch through its owner-only endpoint, then refetches workspaces. */
export function useUpdateWorkspaceMagpie(wsId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (enabled: boolean) => api.updateWorkspaceMagpie(wsId, enabled),
    onSettled: () => client.invalidateQueries({ queryKey: workspaceKeys.list() }),
  });
}
