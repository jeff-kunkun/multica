import { useCurrentWorkspace } from "@multica/core/paths";
import { isWorkspaceMagpieEnabled } from "@multica/core/workspace/magpie";

/**
 * Whether the current workspace has the Magpie switch on. Read from the
 * workspace payload already in cache; a workspace not loaded yet counts as off.
 */
export function useWorkspaceMagpieEnabled(): boolean {
  return isWorkspaceMagpieEnabled(useCurrentWorkspace()?.settings);
}
