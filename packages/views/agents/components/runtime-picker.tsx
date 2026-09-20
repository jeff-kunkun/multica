"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { ChevronDown, Cloud, Loader2 } from "lucide-react";
import { ProviderLogo } from "../../runtimes/components/provider-logo";
import {
  isRuntimeUsableForUser,
  runtimeDisplayName,
} from "@multica/core/runtimes";
import type { MemberWithUser, RuntimeDevice } from "@multica/core/types";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from "@multica/ui/components/ui/popover";
import { Label } from "@multica/ui/components/ui/label";
import { PillButton } from "../../common/pill-button";
import { useT } from "../../i18n";
import {
  computeFilteredRuntimes,
  RuntimeFilterToggle,
  RuntimeMachineList,
  type RuntimeFilter,
} from "../../runtimes/components/runtime-machine-list";

export type { RuntimeFilter };

export function RuntimePicker({
  runtimes,
  runtimesLoading,
  members,
  currentUserId,
  selectedRuntimeId,
  onSelect,
  disabled = false,
  variant = "field",
}: {
  runtimes: RuntimeDevice[];
  runtimesLoading?: boolean;
  members: MemberWithUser[];
  currentUserId: string | null;
  selectedRuntimeId: string;
  onSelect: (id: string) => void;
  /** Blocks opening the picker while the selection cannot be honoured yet
   *  (e.g. a builder reply or a runtime rebind is in flight). */
  disabled?: boolean;
  /**
   * How the trigger reads. `field` is the labelled, full-width form row the
   * agent forms and the draft preview panel use. `pill` is the same picker on
   * a create toolbar, where it sits beside the project pill and must read as
   * one of them: no label row (the pill's own icon and name are the label),
   * and the mine/all toggle moves inside the popup, which is the only place a
   * pill has room for it. The LIST is shared either way — a second rendering
   * of the machine rows is how two pickers drift apart (DENE-443).
   */
  variant?: "field" | "pill";
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState<RuntimeFilter>("mine");

  const getOwnerMember = (ownerId: string | null) => {
    if (!ownerId) return null;
    return members.find((m) => m.user_id === ownerId) ?? null;
  };

  const hasOtherRuntimes = runtimes.some((r) => r.owner_id !== currentUserId);

  // Base list honours the mine/all toggle and drives auto-selection; it is
  // intentionally independent of the search box so typing never changes the
  // seeded selection. The list BODY applies the same filter again for what it
  // renders — one shared rule (`computeFilteredRuntimes`), not two.
  const filteredRuntimes = useMemo(
    () => computeFilteredRuntimes(runtimes, filter, currentUserId),
    [runtimes, filter, currentUserId],
  );

  const selectedRuntime =
    runtimes.find((d) => d.id === selectedRuntimeId) ?? null;

  // The id this picker would seed, as a string. An id, not the list: callers
  // pass `runtimes={data ?? []}` and build `onSelect` inline, so an effect
  // keyed on those identities re-runs on every render of the parent — and a
  // parent that re-renders while it writes (a rebind invalidates its own
  // query) turns one empty selection into an endless switch loop (DENE-319).
  const seedRuntimeId = useMemo(
    () =>
      filteredRuntimes.find((r) => isRuntimeUsableForUser(r, currentUserId))
        ?.id ?? "",
    [filteredRuntimes, currentUserId],
  );

  // Read through a ref so the seed below depends only on the data it seeds
  // from, never on the caller's callback identity.
  const onSelectRef = useRef(onSelect);
  useEffect(() => {
    onSelectRef.current = onSelect;
  });

  // Sole source of truth for seeding the parent's selection when it's empty
  // — first mount with no template runtime, runtimes arriving later over
  // WS, or filter toggle clearing to a set with no usable item. Only fires
  // when `selectedRuntimeId === ""` so a duplicate-mode pre-fill (template
  // runtime) is never silently overwritten. A parent that ignores the seed
  // (there is nothing on the server to rebind) is not asked again on every
  // render.
  useEffect(() => {
    if (selectedRuntimeId !== "") return;
    if (seedRuntimeId) onSelectRef.current(seedRuntimeId);
  }, [seedRuntimeId, selectedRuntimeId]);

  // On filter toggle, recompute the picker's selection to a usable item
  // in the new filter set. Pushes `""` when nothing matches; the seeding
  // effect above is a no-op in that case (correct: no usable item to pick).
  const handleFilterChange = (next: RuntimeFilter) => {
    if (next === filter) return;
    setFilter(next);
    const nextList = computeFilteredRuntimes(runtimes, next, currentUserId);
    const firstUsable = nextList.find((r) =>
      isRuntimeUsableForUser(r, currentUserId),
    );
    onSelect(firstUsable?.id ?? "");
  };

  const pill = variant === "pill";

  // These are not just a view filter: changing tab re-selects the first usable
  // runtime in the new list, so they are a second way to fire onSelect and must
  // honour `disabled` alongside the trigger. Built once and placed by variant
  // — above the trigger in a form row, inside the popup on a toolbar pill.
  const filterToggle = hasOtherRuntimes ? (
    <RuntimeFilterToggle
      filter={filter}
      onFilterChange={handleFilterChange}
      disabled={disabled}
    />
  ) : null;

  return (
    <div className={pill ? "inline-flex min-w-0" : "flex flex-col min-w-0"}>
      {pill ? null : (
        <div className="flex h-6 items-center justify-between">
          <Label className="text-caption text-muted-foreground">
            {t(($) => $.create_dialog.runtime_label)}
          </Label>
          {filterToggle}
        </div>
      )}
      <Popover
        open={open && !disabled}
        onOpenChange={(next) => {
          if (disabled) return;
          setOpen(next);
        }}
      >
        {pill ? (
          // Same trigger contract, toolbar chrome: one line, the machine name
          // truncating before its siblings, and the owner/device second line
          // dropped — a pill has no room for it and the popup still shows it on
          // every row.
          <PopoverTrigger
            disabled={disabled || (runtimes.length === 0 && !runtimesLoading)}
            render={<PillButton />}
            title={selectedRuntime ? runtimeDisplayName(selectedRuntime) : undefined}
          >
            {runtimesLoading ? (
              <Loader2 className="size-3.5 shrink-0 animate-spin text-muted-foreground" />
            ) : selectedRuntime ? (
              <ProviderLogo
                provider={selectedRuntime.provider}
                className="size-3.5 shrink-0"
              />
            ) : (
              <Cloud className="size-3.5 shrink-0 text-muted-foreground" />
            )}
            <span className="truncate">
              {runtimesLoading
                ? t(($) => $.create_dialog.runtime_loading)
                : selectedRuntime
                  ? runtimeDisplayName(selectedRuntime)
                  : t(($) => $.create_dialog.runtime_none)}
            </span>
          </PopoverTrigger>
        ) : (
        <PopoverTrigger
          disabled={disabled || (runtimes.length === 0 && !runtimesLoading)}
          className="flex w-full min-w-0 items-center gap-3 rounded-lg border border-border bg-background px-3 py-2.5 mt-1.5 text-left text-body transition-colors hover:bg-muted disabled:pointer-events-none disabled:opacity-50"
        >
          {runtimesLoading ? (
            <Loader2 className="h-4 w-4 shrink-0 animate-spin text-muted-foreground" />
          ) : selectedRuntime ? (
            <ProviderLogo
              provider={selectedRuntime.provider}
              className="h-4 w-4 shrink-0"
            />
          ) : (
            <Cloud className="h-4 w-4 shrink-0 text-muted-foreground" />
          )}
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <span className="truncate font-medium">
                {runtimesLoading
                  ? t(($) => $.create_dialog.runtime_loading)
                  : selectedRuntime
                    ? runtimeDisplayName(selectedRuntime)
                    : t(($) => $.create_dialog.runtime_none)}
              </span>
              {selectedRuntime?.runtime_mode === "cloud" && (
                <span className="shrink-0 rounded-xs bg-info/10 px-1.5 py-0.5 text-caption font-medium text-info">
                  {t(($) => $.create_dialog.runtime_cloud_badge)}
                </span>
              )}
            </div>
            {selectedRuntime && (
              <div className="truncate text-caption text-muted-foreground">
                {getOwnerMember(selectedRuntime.owner_id)?.name ??
                  selectedRuntime.device_info}
              </div>
            )}
          </div>
          <ChevronDown
            className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${
              open ? "rotate-180" : ""
            }`}
          />
        </PopoverTrigger>
        )}
        <PopoverContent
          align="start"
          className={
            pill
              ? "w-72 p-1 flex flex-col max-h-72"
              : "w-[var(--anchor-width)] p-1 flex flex-col max-h-72"
          }
        >
          {pill && filterToggle ? (
            <div className="mb-1 flex shrink-0 items-center justify-between gap-2 px-1">
              <span className="truncate text-caption text-muted-foreground">
                {t(($) => $.create_dialog.runtime_label)}
              </span>
              {filterToggle}
            </div>
          ) : null}
          <RuntimeMachineList
            runtimes={runtimes}
            filter={filter}
            members={members}
            currentUserId={currentUserId}
            selectedRuntimeId={selectedRuntimeId}
            onSelect={(runtimeId) => {
              onSelect(runtimeId);
              setOpen(false);
            }}
          />
        </PopoverContent>
      </Popover>
    </div>
  );
}
