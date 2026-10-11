import { create } from "zustand";
import type { ChatLinkedProjectRef } from "@multica/core/types";

const EMPTY: ChatLinkedProjectRef[] = [];
interface State {
  workspaceId: string | null;
  refs: ChatLinkedProjectRef[];
  set: (workspaceId: string, refs: ChatLinkedProjectRef[]) => void;
  clear: () => void;
}
export const useChatReferencesDraftStore = create<State>((set) => ({
  workspaceId: null, refs: EMPTY,
  set: (workspaceId, refs) => set({ workspaceId, refs }),
  clear: () => set({ workspaceId: null, refs: EMPTY }),
}));
export function chatDraftReferences(wsId: string | null) {
  const draft = useChatReferencesDraftStore.getState();
  return draft.workspaceId === wsId ? draft.refs : EMPTY;
}
