import { useQueryClient } from "@tanstack/react-query";
import { useWSSubscriptions } from "@/lib/use-ws-subscriptions";
import { refreshWorkspaceLinks } from "./workspace-link-ws-updaters";
import { workspaceLinkKeys } from "@/data/queries/workspace-links";
import { chatKeys } from "@/data/queries/chat";

export function useWorkspaceLinksRealtime() {
  const qc = useQueryClient();
  useWSSubscriptions((ws, wsId) => {
    const refresh = () => { void refreshWorkspaceLinks(qc, wsId); };
    return [
      ws.on("workspace_link:changed", (p) => { if (p.workspace_id === wsId) refresh(); }),
      ws.on("inbox:new", ({ item }) => { if (item.workspace_id === wsId && item.type.startsWith("workspace_link_")) refresh(); }),
      ws.on("chat:session_updated", () => {
        void qc.invalidateQueries({ queryKey: [...workspaceLinkKeys.all(wsId), "chat"] });
        void qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
      }),
      ws.onReconnect(refresh),
    ];
  }, [qc]);
}
