"use client";

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import { Badge } from "@multica/ui/components/ui/badge";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import {
  routingHealthOptions,
  workspaceKeys,
  workspaceListOptions,
} from "@multica/core/workspace/queries";
import type { RoutingHealth } from "@multica/core/workspace/routing-health";
import {
  normalizeThreshold,
  parseRoutingSettings,
  routingState,
  withRoutingSettings,
  type RoutingSettings,
  type RoutingState,
} from "@multica/core/workspace/routing-settings";
import type { Workspace } from "@multica/core/types";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";
import { useAutoSave } from "./use-auto-save";

/**
 * The routing section — the ONLY screen this feature adds.
 *
 * The 验收席 slot renders through the existing custom-property panel and the
 * routing decisions render as ordinary comments, so neither needed a change.
 * Hiding the slot while routing is off is done by archiving the property
 * definition, which the property panel already honours.
 *
 * The model field is a free-text identifier rather than a picker. The routing
 * judge runs on the deployment's server-internal LLM gateway (the same layer
 * that backs chat titling), not on the per-workspace runtime model catalog the
 * agent seats use — so there is no workspace-scoped list to pick from, and
 * offering one would let a workspace choose a model the server cannot reach.
 * Credentials for that gateway are deployment configuration and are
 * deliberately not editable here: this section stores no secret of any kind.
 */
export function RoutingTab() {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const workspace = useCurrentWorkspace();
  // Definitions are owner/admin work, like every other workspace-level
  // setting. Members see the section and its state but cannot change it.
  const { role } = useCurrentMember(workspace?.id ?? "");
  const canManage = role === "owner" || role === "admin";

  const saved = useMemo(
    () => parseRoutingSettings(workspace?.settings),
    [workspace?.settings],
  );

  const [enabled, setEnabled] = useState(saved.enabled);
  const [model, setModel] = useState(saved.model);
  const [threshold, setThreshold] = useState(String(saved.confidence_threshold));

  // Reset only when the workspace changes, not on every cached-object
  // replacement — an unrelated mutation must not wipe an unsaved edit.
  useEffect(() => {
    const next = parseRoutingSettings(workspace?.settings);
    setEnabled(next.enabled);
    setModel(next.model);
    setThreshold(String(next.confidence_threshold));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspace?.id]);

  const draft: RoutingSettings = useMemo(
    () => ({
      enabled,
      model,
      confidence_threshold: normalizeThreshold(Number(threshold)),
    }),
    [enabled, model, threshold],
  );

  useAutoSave({
    value: draft,
    savedValue: saved,
    onSave: async (next) => {
      if (!workspace) return;
      const updated = await api.updateWorkspace(workspace.id, {
        settings: withRoutingSettings(workspace.settings, next),
      });
      qc.setQueryData(
        workspaceListOptions().queryKey,
        (old: Workspace[] | undefined) =>
          old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      // The health report is derived from the settings that were just
      // replaced, so the cached copy is stale the moment this resolves.
      // Without this the chip keeps describing the previous configuration for
      // up to a refetch interval.
      await qc.invalidateQueries({
        queryKey: workspaceKeys.routingHealth(workspace.id),
      });
    },
    enabled: !!workspace && canManage,
    isEqual: (a, b) =>
      a.enabled === b.enabled &&
      a.model.trim() === b.model.trim() &&
      a.confidence_threshold === b.confidence_threshold,
  });

  // Live health from the server. Without it the fourth state is unreachable:
  // the stored fields cannot know that the model is rejected or cooling down,
  // and the routing design deliberately keeps that off every ticket — so a
  // failure the section did not show would be visible nowhere but the server
  // log (DENE-633 review, F2).
  const health = useQuery({
    ...routingHealthOptions(workspace?.id ?? ""),
    enabled: !!workspace?.id,
  });

  const recheck = useMutation({
    mutationFn: () => api.checkRoutingHealth(workspace?.id ?? ""),
    onSuccess: (next) => {
      qc.setQueryData(workspaceKeys.routingHealth(workspace?.id ?? ""), next);
    },
  });

  // The draft wins over the server report while an edit is in flight: a person
  // who just switched routing off should not keep reading a red chip about the
  // model they stopped using. Health only decides the enabled/ineffective
  // split, and only once the draft itself says enabled — and only on the
  // server's explicit `ineffective`, so a health report that has not caught up
  // with the switch yet cannot report a fault that does not exist.
  const state = routingState(draft, health.data);

  return (
    <SettingsTab
      title={t(($) => $.routing.title)}
      description={t(($) => $.routing.description)}
    >
      <SettingsSection>
        <StateBanner
          state={state}
          health={health.data}
          canManage={canManage}
          checking={recheck.isPending}
          onRecheck={() => recheck.mutate()}
        />
      </SettingsSection>

      <SettingsSection title={t(($) => $.routing.section_title)}>
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.routing.enabled_label)}
            description={t(($) => $.routing.enabled_description)}
          >
            <Switch
              checked={enabled}
              disabled={!canManage}
              onCheckedChange={setEnabled}
              aria-label={t(($) => $.routing.enabled_label)}
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.routing.model_label)}
            description={t(($) => $.routing.model_description)}
            size="text"
          >
            <Input
              value={model}
              disabled={!canManage}
              placeholder={t(($) => $.routing.model_placeholder)}
              onChange={(e) => setModel(e.target.value)}
              aria-label={t(($) => $.routing.model_label)}
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.routing.threshold_label)}
            description={t(($) => $.routing.threshold_description)}
            size="code"
          >
            <Input
              type="number"
              min={0.05}
              max={1}
              step={0.05}
              value={threshold}
              // Greyed out while routing is off, so the number cannot look
              // like it is doing something it is not.
              disabled={!canManage || !enabled}
              onChange={(e) => setThreshold(e.target.value)}
              aria-label={t(($) => $.routing.threshold_label)}
            />
          </SettingsRow>
        </SettingsCard>
        <p className="px-0.5 text-caption text-muted-foreground">
          {t(($) => $.routing.no_credentials_note)}
        </p>
      </SettingsSection>

      {/* Placeholder for the candidate filter chain. Kept visible and clearly
          marked as unimplemented so the seam is documented where the person
          who would ask for it is already looking. */}
      <SettingsSection title={t(($) => $.routing.filters_title)}>
        <div className="rounded-lg border border-dashed border-surface-border px-4 py-6 text-caption text-muted-foreground">
          {t(($) => $.routing.filters_placeholder)}
        </div>
      </SettingsSection>
    </SettingsTab>
  );
}

/**
 * The four states, each with its own chip and its own sentence.
 *
 * `incomplete` is the one that must never be collapsed into `enabled`:
 * somebody who flipped the switch and walked away will otherwise believe
 * routing is working while the product behaves exactly as it did before.
 */
function StateBanner({
  state,
  health,
  canManage,
  checking,
  onRecheck,
}: {
  state: RoutingState;
  health?: RoutingHealth;
  canManage: boolean;
  checking: boolean;
  onRecheck: () => void;
}) {
  const { t } = useT("settings");
  const chip: Record<
    RoutingState,
    { variant: "secondary" | "destructive" | "default"; className?: string }
  > = {
    off: { variant: "secondary" },
    incomplete: {
      variant: "secondary",
      className: "bg-warning/15 text-warning-foreground",
    },
    enabled: { variant: "default", className: "bg-success/15 text-success" },
    ineffective: { variant: "destructive" },
  };

  const lastCheckedLabel = (
    translate: typeof t,
    unixSeconds: number,
  ): string => {
    const { unit, n } = timeAgoBucket(unixSeconds);
    if (unit === "now") return translate(($) => $.routing.health_time_just_now);
    if (unit === "minutes")
      return translate(($) => $.routing.health_time_minutes_ago, { n });
    return translate(($) => $.routing.health_time_hours_ago, { n });
  };

  const detail =
    state === "ineffective" && health?.reason
      ? `${t(($) => $.routing.states.ineffective.detail)} ${t(($) => $.routing.health_reason_label)}: ${health.reason}`
      : t(($) => $.routing.states[state].detail);

  return (
    <div className="flex flex-col gap-2 rounded-lg border border-surface-border px-4 py-3 sm:flex-row sm:items-start sm:gap-4">
      <Badge
        variant={chip[state].variant}
        className={chip[state].className}
        data-state={state}
      >
        {t(($) => $.routing.states[state].chip)}
      </Badge>
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <p className="text-caption leading-5 text-muted-foreground">{detail}</p>
        {state === "enabled" && (
          <p className="text-caption leading-5 text-muted-foreground">
            {health?.last_success_at
              ? t(($) => $.routing.health_connected, {
                  when: lastCheckedLabel(t, health.last_success_at),
                })
              : t(($) => $.routing.health_never_checked)}
          </p>
        )}
        {state === "ineffective" && health?.retry_after_seconds ? (
          <p className="text-caption leading-5 text-muted-foreground">
            {t(($) => $.routing.health_retry_in, {
              minutes: Math.max(1, Math.round(health.retry_after_seconds / 60)),
            })}
          </p>
        ) : null}
      </div>
      {/* Offered for both live states, not just the broken one: a person who
          just typed a model identifier wants to know it works before they
          find out from a ticket that never got dispatched. */}
      {canManage && (state === "enabled" || state === "ineffective") && (
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={checking}
          onClick={onRecheck}
          className="shrink-0"
        >
          {checking
            ? t(($) => $.routing.health_checking)
            : t(($) => $.routing.health_recheck)}
        </Button>
      )}
    </div>
  );
}

/**
 * How long ago, as a unit plus a count. The caller resolves the copy, because
 * only it holds a properly typed `t`.
 *
 * Deliberately coarse: what this describes is "the last time the routing model
 * answered", and the reader wants to know whether that was recent, not when
 * exactly.
 */
export function timeAgoBucket(
  unixSeconds: number,
  now: number = Date.now(),
): { unit: "now" | "minutes" | "hours"; n: number } {
  const minutes = Math.floor((now / 1000 - unixSeconds) / 60);
  if (minutes < 1) return { unit: "now", n: 0 };
  if (minutes < 60) return { unit: "minutes", n: minutes };
  return { unit: "hours", n: Math.floor(minutes / 60) };
}
