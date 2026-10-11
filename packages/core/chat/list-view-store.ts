"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { sessionStorageAdapter } from "../platform/storage";
import {
  createWorkspaceAwareStorage,
  getCurrentSlug,
  registerForWorkspaceRehydration,
} from "../platform/workspace-storage";
import type { ChatProjectFilter } from "./project-bar";

/**
 * Where the person was in the chat list: which project they were browsing,
 * what they had typed into the search box, whether the archive was open and
 * whether the recent-chats preview was expanded (a restored scroll offset
 * below the fold needs the rows that were showing).
 *
 * Session-scoped, not a preference. It lives in sessionStorage (namespaced
 * per workspace), so it survives the list unmounting — opening a chat on a
 * phone replaces the list route — and a phone browser reloading a discarded
 * tab, but a new tab starts from the full list again. Remembering a search
 * term or an open archive across days would be surprising; losing them to a
 * tab switch is what made the phone list feel forgetful.
 */
export type ChatListView = "history" | "archived";

interface ChatListViewState {
  /** Prevent mount effects overwriting a workspace snapshot before rehydration. */
  hydratedWorkspaceSlug: string | null;
  projectFilter: ChatProjectFilter;
  /** Only an explicit All pick stops the list following the conversation. */
  keepAllProjects: boolean;
  search: string;
  view: ChatListView;
  historyExpanded: boolean;
  setProjectFilter: (filter: ChatProjectFilter) => void;
  selectProjectFilter: (filter: ChatProjectFilter) => void;
  followProjects: (ids: readonly string[]) => void;
  setSearch: (search: string) => void;
  setView: (view: ChatListView) => void;
  setHistoryExpanded: (expanded: boolean) => void;
}

const DEFAULTS = {
  hydratedWorkspaceSlug: null as string | null,
  projectFilter: { type: "all" } as ChatProjectFilter,
  keepAllProjects: false,
  search: "",
  view: "history" as ChatListView,
  historyExpanded: false,
};

function isProjectFilter(value: unknown): value is ChatProjectFilter {
  if (!value || typeof value !== "object") return false;
  const filter = value as { type?: unknown; id?: unknown };
  if (filter.type === "all" || filter.type === "none") return true;
  return filter.type === "project" && typeof filter.id === "string" && filter.id !== "";
}

export const useChatListViewStore = create<ChatListViewState>()(
  persist(
    (set, get) => ({
      ...DEFAULTS,
      setProjectFilter: (projectFilter) => set({ projectFilter }),
      selectProjectFilter: (projectFilter) => set({
        projectFilter,
        keepAllProjects: projectFilter.type === "all",
      }),
      followProjects: (ids) => {
        const { projectFilter, keepAllProjects, hydratedWorkspaceSlug } = get();
        if (!hydratedWorkspaceSlug || hydratedWorkspaceSlug !== getCurrentSlug()) return;
        if (keepAllProjects && projectFilter.type === "all") return;
        // Multi-project chats keep the browsed project when it still belongs.
        if (projectFilter.type === "project" && ids.includes(projectFilter.id)) return;
        if (ids.length === 0 && projectFilter.type === "none") return;
        set({ projectFilter: ids[0] ? { type: "project", id: ids[0] } : { type: "none" } });
      },
      setSearch: (search) => set({ search }),
      setView: (view) => set({ view }),
      setHistoryExpanded: (historyExpanded) => set({ historyExpanded }),
    }),
    {
      name: "multica_chat_list_view",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(sessionStorageAdapter)),
      partialize: (state) => ({
        projectFilter: state.projectFilter,
        keepAllProjects: state.keepAllProjects,
        search: state.search,
        view: state.view,
        historyExpanded: state.historyExpanded,
      }),
      // A workspace with nothing saved starts from the full list rather than
      // inheriting the previous workspace's project (its ids mean nothing
      // here). Each field is validated on its own: sessionStorage is shared
      // with other code on the origin and a malformed value must degrade to
      // the default, not crash the list.
      merge: (persisted, current) => {
        const p = (persisted ?? {}) as Partial<Record<keyof typeof DEFAULTS, unknown>>;
        return {
          ...current,
          hydratedWorkspaceSlug: getCurrentSlug(),
          projectFilter: isProjectFilter(p.projectFilter) ? p.projectFilter : DEFAULTS.projectFilter,
          keepAllProjects: p.keepAllProjects === true && isProjectFilter(p.projectFilter) && p.projectFilter.type === "all",
          search: typeof p.search === "string" ? p.search : DEFAULTS.search,
          view: p.view === "archived" ? "archived" : DEFAULTS.view,
          historyExpanded: p.historyExpanded === true,
        };
      },
    },
  ),
);

registerForWorkspaceRehydration(() => useChatListViewStore.persist.rehydrate());
