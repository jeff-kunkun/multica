import type { QueryClient } from "@tanstack/react-query";
import { workspaceLinkKeys } from "@/data/queries/workspace-links";
import { projectKeys, referenceProjectOptions } from "@/data/queries/projects";
import { chatKeys } from "@/data/queries/chat";

export async function refreshWorkspaceLinks(qc: QueryClient, wsId: string) {
  const keys = [workspaceLinkKeys.all(wsId), referenceProjectOptions(wsId).queryKey, chatKeys.sessions(wsId)];
  await Promise.all(keys.map(async (queryKey) => {
    await qc.cancelQueries({ queryKey });
    await qc.invalidateQueries({ queryKey });
  }));
  await qc.invalidateQueries({ queryKey: projectKeys.all(wsId), predicate: (q) => q.queryKey.at(-1) === "references" });
}
