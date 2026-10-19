import { useCurrentWorkspace } from "@multica/core/paths";
import { isWorkspaceMagpieEnabled } from "@multica/core/workspace/magpie";

/**
 * Whether the current workspace has magpie mode on. Read from the workspace
 * payload already in cache, so pages switch without an extra request; a
 * workspace that has not loaded yet counts as off (the old behaviour).
 */
export function useWorkspaceMagpieEnabled(): boolean {
  return isWorkspaceMagpieEnabled(useCurrentWorkspace()?.settings);
}
