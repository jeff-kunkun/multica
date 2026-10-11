import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerForWorkspaceRehydration,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";

const DEFAULTS = {
  selectedByProvider: {} as Record<string, string>,
  hiddenProviders: [] as string[],
};

interface ProviderQuotaSourceState {
  /** Provider slug → runtime id last chosen in the status bar. */
  selectedByProvider: Record<string, string>;
  setSelectedRuntime: (provider: string, runtimeId: string) => void;
  /** Provider slugs the user unticked; the status bar skips them. */
  hiddenProviders: string[];
  setProviderHidden: (provider: string, hidden: boolean) => void;
}

export const useProviderQuotaSourceStore = create<ProviderQuotaSourceState>()(
  persist(
    (set) => ({
      selectedByProvider: DEFAULTS.selectedByProvider,
      hiddenProviders: DEFAULTS.hiddenProviders,
      setProviderHidden: (provider, hidden) => {
        const key = provider.trim().toLowerCase();
        if (!key) return;
        set((state) => {
          if (state.hiddenProviders.includes(key) === hidden) return state;
          return {
            hiddenProviders: hidden
              ? [...state.hiddenProviders, key]
              : state.hiddenProviders.filter((p) => p !== key),
          };
        });
      },
      setSelectedRuntime: (provider, runtimeId) => {
        const key = provider.trim().toLowerCase();
        if (!key || !runtimeId) return;
        set((state) => {
          if (state.selectedByProvider[key] === runtimeId) return state;
          return {
            selectedByProvider: {
              ...state.selectedByProvider,
              [key]: runtimeId,
            },
          };
        });
      },
    }),
    {
      name: "multica_provider_quota_source",
      storage: createJSONStorage(() =>
        createWorkspaceAwareStorage(defaultStorage),
      ),
      partialize: (state) => ({
        selectedByProvider: state.selectedByProvider,
        hiddenProviders: state.hiddenProviders,
      }),
      merge: (persisted, current) => {
        if (!persisted) return { ...current, ...DEFAULTS };
        const p = persisted as Partial<typeof DEFAULTS>;
        return {
          ...current,
          selectedByProvider: p.selectedByProvider ?? DEFAULTS.selectedByProvider,
          hiddenProviders: Array.isArray(p.hiddenProviders)
            ? p.hiddenProviders
            : DEFAULTS.hiddenProviders,
        };
      },
    },
  ),
);

registerForWorkspaceRehydration(() =>
  useProviderQuotaSourceStore.persist.rehydrate(),
);
