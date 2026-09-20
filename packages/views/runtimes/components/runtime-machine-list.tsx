"use client";

import { useMemo, useState } from "react";
import { Lock, Search } from "lucide-react";
import { isRuntimeUsableForUser } from "@multica/core/runtimes";
import type { MemberWithUser, RuntimeDevice } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { ProviderLogo } from "./provider-logo";
import {
  buildRuntimeMachines,
  filterRuntimeMachines,
  runtimeRowLabel,
} from "./runtime-machines";

/**
 * The machine LIST, shared by every surface that asks the user to choose one.
 *
 * It lives here rather than inside `RuntimePicker` because a second rendering of
 * the machine rows is how two pickers drift apart (DENE-443): the alignment
 * entry's configuration panel needs the same list — grouping, per-row owner,
 * offline dot, the private-runtime lock — inside its own popover, and copying
 * that markup is how one surface ends up able to pick a machine the other
 * refuses to show.
 *
 * The host owns two things this deliberately does not. The mine/all `filter` is
 * passed in because changing it re-selects in `RuntimePicker` (a picker's
 * selection must exist in its own list) and must NOT re-select in a
 * configuration panel, where the machine is one of four choices already made.
 * And `onSelect` is the host's, so it can close its own popover — or, in the
 * panel, leave it open because there are three more choices below.
 */

/** Above this many runtimes the flat list becomes hard to scan, so a search box
 *  appears. */
export const RUNTIME_SEARCH_THRESHOLD = 6;

/** The viewing filter over the machine list. `mine` is the default on every
 *  surface that has one. */
export type RuntimeFilter = "mine" | "all";

/** The mine/all segmented control, shared so its two states read the same
 *  everywhere. */
export function RuntimeFilterToggle({
  filter,
  onFilterChange,
  disabled,
}: {
  filter: RuntimeFilter;
  onFilterChange: (next: RuntimeFilter) => void;
  disabled?: boolean;
}) {
  const { t } = useT("agents");
  return (
    <div className="flex shrink-0 items-center gap-0.5 rounded-md bg-muted p-0.5">
      <button
        type="button"
        disabled={disabled}
        onClick={() => onFilterChange("mine")}
        className={cn(
          "rounded-xs px-2 py-0.5 text-caption font-medium transition-colors disabled:pointer-events-none disabled:opacity-50",
          filter === "mine"
            ? "bg-background text-foreground shadow-sm"
            : "text-muted-foreground hover:text-foreground",
        )}
      >
        {t(($) => $.create_dialog.runtime_filter_mine)}
      </button>
      <button
        type="button"
        disabled={disabled}
        onClick={() => onFilterChange("all")}
        className={cn(
          "rounded-xs px-2 py-0.5 text-caption font-medium transition-colors disabled:pointer-events-none disabled:opacity-50",
          filter === "all"
            ? "bg-background text-foreground shadow-sm"
            : "text-muted-foreground hover:text-foreground",
        )}
      >
        {t(($) => $.create_dialog.runtime_filter_all)}
      </button>
    </div>
  );
}

/**
 * The scrollable machine list: optional search, machine groups, one row per
 * runtime.
 *
 * `runtimes` is the host's full list and `filter` is applied here, so every host
 * gets the same scoping rule as the picker. A runtime the viewer may not use is
 * rendered disabled with the reason rather than hidden — the machine exists in
 * the workspace, and a list that silently omits it reads as a broken list.
 */
export function RuntimeMachineList({
  runtimes,
  filter = "mine",
  members,
  currentUserId,
  selectedRuntimeId,
  onSelect,
  disabled,
}: {
  runtimes: RuntimeDevice[];
  filter?: RuntimeFilter;
  members: MemberWithUser[];
  currentUserId: string | null;
  selectedRuntimeId: string;
  onSelect: (runtimeId: string) => void;
  /** Blocks every row while the surrounding surface cannot honour a choice yet
   *  (a reply or a rebind in flight). */
  disabled?: boolean;
}) {
  const { t } = useT("agents");
  const [search, setSearch] = useState("");

  const getOwnerMember = (ownerId: string | null) => {
    if (!ownerId) return null;
    return members.find((m) => m.user_id === ownerId) ?? null;
  };

  const filteredRuntimes = useMemo(
    () => computeFilteredRuntimes(runtimes, filter, currentUserId),
    [runtimes, filter, currentUserId],
  );

  // Group the (searched) base list by machine so 20+ runtimes read as a handful
  // of named machines, online-first, current machine first.
  const machines = useMemo(() => {
    const all = buildRuntimeMachines(filteredRuntimes, {
      now: Date.now(),
      currentUserId,
    });
    return filterRuntimeMachines(all, search, "all");
  }, [filteredRuntimes, search, currentUserId]);

  const showSearch = runtimes.length > RUNTIME_SEARCH_THRESHOLD;

  return (
    <>
      {showSearch && (
        <div className="relative mb-1 shrink-0">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <input
            type="text"
            autoFocus
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t(($) => $.create_dialog.runtime_search_placeholder)}
            className="w-full rounded-md border border-border bg-background py-1.5 pl-8 pr-2 text-body outline-none focus:ring-1 focus:ring-ring"
          />
        </div>
      )}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {machines.length === 0 ? (
          <div className="px-3 py-6 text-center text-caption text-muted-foreground">
            {t(($) => $.create_dialog.runtime_no_results)}
          </div>
        ) : (
          machines.map((machine) => (
            <div key={machine.id}>
              {/* Always show the machine header — even when a search or a
                  single-machine workspace narrows it to one group — so the
                  grouping stays consistent instead of collapsing to a flat
                  list. */}
              <div className="flex items-center justify-between gap-2 px-2 pb-0.5 pt-2 text-micro font-medium text-muted-foreground">
                <span className="truncate">{machine.title}</span>
                <span className="shrink-0 tabular-nums">
                  {t(($) => $.create_dialog.runtime_group_online, {
                    online: machine.onlineCount,
                    total: machine.runtimes.length,
                  })}
                </span>
              </div>
              {machine.runtimes.map((device) => {
                const ownerMember = getOwnerMember(device.owner_id);
                const usable = isRuntimeUsableForUser(device, currentUserId);
                const rowDisabled = disabled === true || !usable;
                const disabledTitle = !usable
                  ? t(($) => $.create_dialog.runtime_private_locked_tooltip)
                  : undefined;
                return (
                  <button
                    key={device.id}
                    type="button"
                    disabled={rowDisabled}
                    title={disabledTitle}
                    onClick={() => {
                      if (rowDisabled) return;
                      onSelect(device.id);
                    }}
                    className={cn(
                      "flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-body transition-colors",
                      rowDisabled
                        ? "cursor-not-allowed opacity-50"
                        : device.id === selectedRuntimeId
                          ? "bg-accent"
                          : "hover:bg-accent/50",
                    )}
                  >
                    <ProviderLogo
                      provider={device.provider}
                      className="h-4 w-4 shrink-0"
                    />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="truncate font-medium">
                          {runtimeRowLabel(device, machine.title)}
                        </span>
                        {device.runtime_mode === "cloud" && (
                          <span className="shrink-0 rounded-xs bg-info/10 px-1.5 py-0.5 text-caption font-medium text-info">
                            {t(($) => $.create_dialog.runtime_cloud_badge)}
                          </span>
                        )}
                        {!usable && (
                          <span className="inline-flex shrink-0 items-center gap-1 rounded-xs bg-muted px-1.5 py-0.5 text-micro font-medium text-muted-foreground">
                            <Lock className="h-3 w-3" />
                            {t(($) => $.create_dialog.runtime_private_badge)}
                          </span>
                        )}
                      </div>
                      <div className="mt-0.5 flex items-center gap-1 text-caption text-muted-foreground">
                        {ownerMember ? (
                          <>
                            <ActorAvatar
                              actorType="member"
                              actorId={ownerMember.user_id}
                              size="xs"
                            />
                            <span className="truncate">{ownerMember.name}</span>
                          </>
                        ) : (
                          <span className="truncate">{device.device_info}</span>
                        )}
                      </div>
                    </div>
                    <span
                      className={cn(
                        "h-2 w-2 shrink-0 rounded-full",
                        device.status === "online"
                          ? "bg-success"
                          : "bg-muted-foreground/40",
                      )}
                    />
                  </button>
                );
              })}
            </div>
          ))
        )}
      </div>
    </>
  );
}

/**
 * The list order: the viewer's own machines first, then the ones they may use,
 * then everything else.
 *
 * Exported because the seed a host computes must agree with the order the list
 * shows — a picker that opens on a machine the list buries has two orderings.
 */
export function computeFilteredRuntimes(
  runtimes: RuntimeDevice[],
  filter: RuntimeFilter,
  currentUserId: string | null,
): RuntimeDevice[] {
  const filtered =
    filter === "mine" && currentUserId
      ? runtimes.filter((r) => r.owner_id === currentUserId)
      : runtimes;
  return filtered.toSorted((a, b) => {
    const aMine = a.owner_id === currentUserId;
    const bMine = b.owner_id === currentUserId;
    if (aMine && !bMine) return -1;
    if (!aMine && bMine) return 1;
    const aUsable = isRuntimeUsableForUser(a, currentUserId);
    const bUsable = isRuntimeUsableForUser(b, currentUserId);
    if (aUsable && !bUsable) return -1;
    if (!aUsable && bUsable) return 1;
    return 0;
  });
}
