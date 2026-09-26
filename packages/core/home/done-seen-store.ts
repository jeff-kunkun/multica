"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerForWorkspaceRehydration,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";

// When the viewer last looked at the inbox's "done today" lane, per workspace.
// Finished issues show once; after a visit they fold away (DENE-882).

interface DoneSeenState {
  seenAt: string | null;
  markSeen: (at: string) => void;
}

export const useDoneSeenStore = create<DoneSeenState>()(
  persist(
    (set) => ({
      seenAt: null,
      markSeen: (at) => set({ seenAt: at }),
    }),
    {
      name: "multica_inbox_done_seen",
      version: 1,
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      partialize: (state) => ({ seenAt: state.seenAt }),
      // A workspace with no mark starts fresh instead of borrowing the last one.
      merge: (persisted, current) => ({
        ...current,
        seenAt: (persisted as Partial<DoneSeenState> | undefined)?.seenAt ?? null,
      }),
    },
  ),
);

registerForWorkspaceRehydration(() => useDoneSeenStore.persist.rehydrate());
