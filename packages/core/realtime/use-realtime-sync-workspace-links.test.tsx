/** @vitest-environment jsdom */
import { QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import { WSClient } from "../api/ws-client";
import { createQueryClient } from "../query-client";
import { workspaceLinkKeys, workspaceLinksOptions } from "../workspace-links/queries";

import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

vi.mock("../platform/workspace-storage", () => ({
  getCurrentWsId: () => "ws-1",
  getCurrentSlug: () => "test-ws",
  createWorkspaceAwareStorage: (adapter: unknown) => adapter,
  registerForWorkspaceRehydration: () => {},
}));
vi.mock("../paths", () => ({
  useHasOnboarded: () => true,
  resolvePostAuthDestination: () => "/",
}));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});


it("refreshes link lists, views and audit after CLI changes and reconnect", async () => {
 const qc=createQueryClient();
 let revision=0;
 const listWorkspaceLinks=vi.fn(async()=>({links:[{id:String(revision)}],can:{}}));
 setApiInstance({listWorkspaceLinks} as unknown as ApiClient);
 const handlers: Record<string,(p:unknown)=>void>={};
 let reconnect:()=>unknown=()=>{};
 const ws={on:(e:string,h:(p:unknown)=>void)=>{handlers[e]=h;return ()=>{}},onAny:()=>()=>{},onReconnect:(h:()=>unknown)=>{reconnect=h;return ()=>{}}} as unknown as WSClient;
 const stores={authStore:{getState:()=>({user:{id:"u1"}})}} as unknown as RealtimeSyncStores;
 function Wrapper({children}:{children:ReactNode}) {return <QueryClientProvider client={qc}>{children}</QueryClientProvider>}
 const page=renderHook(()=>useQuery(workspaceLinksOptions("ws-1")),{wrapper:Wrapper});
 renderHook(()=>useRealtimeSync(ws,stores),{wrapper:Wrapper});
 const keys=[workspaceLinkKeys.view("ws-1","l1",null),workspaceLinkKeys.audit("ws-1"),workspaceLinkKeys.list("other")];
 keys.forEach(key=>qc.setQueryData(key,{}));
 await waitFor(()=>expect(listWorkspaceLinks).toHaveBeenCalledTimes(1));
 revision=1;
 await act(async()=>handlers["workspace_link:changed"]!({workspace_id:"ws-1"}));
 await waitFor(()=>expect(page.result.current.data?.links[0]?.id).toBe("1"));
 keys.slice(0,2).forEach(key=>expect(qc.getQueryState(key)?.isInvalidated).toBe(true));
 expect(qc.getQueryState(keys[2]!)?.isInvalidated).toBe(false);
 revision=2;
 await act(async()=>{await reconnect()});
 await waitFor(()=>expect(page.result.current.data?.links[0]?.id).toBe("2"));
 qc.clear();
});
