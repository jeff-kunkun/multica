"use client";

import { useMemo, useState } from "react";
import { ChevronDown, LifeBuoy } from "lucide-react";
import {
  isRuntimeUsableForUser,
  runtimeDisplayLabel,
} from "@multica/core/runtimes";
import type { AgentRuntime } from "@multica/core/types";
import {
  PickerItem,
  PropertyPicker,
} from "../../../issues/components/pickers";
import { useT } from "../../../i18n";

/**
 * Runtimes that may stand in for `primaryId` on an urgent chat message
 * (DENE-1844). Mirrors the server rule in handler/chat_urgent.go: a
 * different runtime, same owner, same provider, and one this user may
 * select. The server re-checks on save, so this is a courtesy filter, not
 * the guard.
 */
export function fallbackRuntimeCandidates(
  runtimes: AgentRuntime[],
  primaryId: string,
  currentUserId: string,
): AgentRuntime[] {
  const primary = runtimes.find((r) => r.id === primaryId);
  if (!primary || !primary.owner_id) return [];
  return runtimes.filter(
    (r) =>
      r.id !== primary.id &&
      r.owner_id === primary.owner_id &&
      r.provider === primary.provider &&
      isRuntimeUsableForUser(r, currentUserId),
  );
}

/**
 * Standby runtime control for the agent inspector. A plain one-level list:
 * the candidate set is already narrowed to one owner and one provider, so
 * the two-level machine picker would mostly show single rows.
 */
export function FallbackRuntimePicker({
  value,
  primaryRuntimeId,
  runtimes,
  currentUserId,
  canEdit,
  onChange,
}: {
  value: string;
  primaryRuntimeId: string;
  runtimes: AgentRuntime[];
  currentUserId: string;
  canEdit: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  const candidates = useMemo(
    () => fallbackRuntimeCandidates(runtimes, primaryRuntimeId, currentUserId),
    [runtimes, primaryRuntimeId, currentUserId],
  );
  const selected = value ? runtimes.find((r) => r.id === value) : undefined;
  const noneLabel = t(($) => $.inspector.prop_fallback_runtime_none);
  const triggerLabel = selected ? runtimeDisplayLabel(selected) : noneLabel;

  // Read-only states carry the reason in the control itself, so the row
  // explains why it cannot be changed without an extra hint line.
  let readOnlyReason: string | null = null;
  if (!primaryRuntimeId) {
    readOnlyReason = t(($) => $.inspector.prop_fallback_runtime_no_primary);
  } else if (candidates.length === 0 && !value) {
    readOnlyReason = t(($) => $.inspector.prop_fallback_runtime_no_candidates);
  }

  const select = async (next: string) => {
    setOpen(false);
    if (next !== value) await onChange(next);
  };

  const display = (
    <div className="flex min-h-10 items-center gap-2 rounded-lg border border-input bg-input/50 px-3 text-body text-muted-foreground">
      <LifeBuoy className="h-4 w-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 truncate">{readOnlyReason ?? triggerLabel}</span>
    </div>
  );
  if (!canEdit || readOnlyReason) return display;

  const label = t(($) => $.inspector.prop_fallback_runtime);
  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-[var(--anchor-width)] min-w-[14rem] max-w-md"
      align="start"
      tooltip={label}
      triggerRender={
        <button
          type="button"
          className="flex min-h-10 w-full min-w-0 items-center gap-2 rounded-lg border border-input bg-transparent px-3 text-left text-body transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
          aria-label={label}
        />
      }
      trigger={
        <>
          <LifeBuoy
            className="h-4 w-4 shrink-0 text-muted-foreground"
            aria-hidden="true"
          />
          <span className="min-w-0 flex-1 truncate">{triggerLabel}</span>
          <ChevronDown
            className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${
              open ? "rotate-180" : ""
            }`}
            aria-hidden="true"
          />
        </>
      }
    >
      <PickerItem selected={!value} onClick={() => void select("")}>
        <span className="block min-w-0 flex-1 truncate text-left text-label">
          {noneLabel}
        </span>
      </PickerItem>
      {candidates.map((rt) => (
        <PickerItem
          key={rt.id}
          selected={rt.id === value}
          onClick={() => void select(rt.id)}
        >
          <span className="block min-w-0 flex-1 text-left">
            <span className="block truncate text-label font-medium">
              {runtimeDisplayLabel(rt)}
            </span>
            <span className="mt-0.5 block truncate text-micro leading-snug text-muted-foreground">
              {rt.device_info}
            </span>
          </span>
        </PickerItem>
      ))}
    </PropertyPicker>
  );
}
