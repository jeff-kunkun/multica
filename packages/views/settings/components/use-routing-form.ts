"use client";

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import {
  routingHealthOptions,
  workspaceKeys,
  workspaceListOptions,
} from "@multica/core/workspace/queries";
import {
  normalizeStaleReviewHours,
  parseRoutingSettings,
  routingState,
  withRoutingSettings,
  type RoutingAnalysisSettings,
  type RoutingRole,
  type RoutingSettings,
} from "@multica/core/workspace/routing-settings";
import type { Workspace } from "@multica/core/types";
import { useT } from "../../i18n";
import { useAutoSave } from "./use-auto-save";

/**
 * The routing form (DENE-1966): one draft for every block on the page.
 *
 * The draft is the saved settings with this sitting's edits laid over them.
 * Most stored knobs have no control any more (threshold, usage priority,
 * upshift, learn rate and window, continuation, judged review, the judge's
 * own gateway), and they must survive a save untouched — a form that rebuilt
 * the block from its own fields would quietly reset them to defaults.
 */
export function useRoutingForm() {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const workspace = useCurrentWorkspace();
  // Definitions are owner/admin work, like every other workspace-level
  // setting. Members see the page and its state but cannot change it.
  const { role, userId } = useCurrentMember(workspace?.id ?? "");
  const canManage = role === "owner" || role === "admin";

  const saved = useMemo(
    () => parseRoutingSettings(workspace?.settings),
    [workspace?.settings],
  );

  const [edits, setEdits] = useState<Partial<RoutingSettings>>({});
  // Number boxes hold the typed text so a half-typed value is not snapped
  // back while somebody is still typing.
  const [staleHoursText, setStaleHoursText] = useState<string | null>(null);
  const [consultText, setConsultText] = useState<string | null>(null);
  // Held apart from the auto-saved draft on purpose: a half-typed credential
  // saved to the server is both useless and a real key in a row nobody will
  // think to clear. The key is committed by an explicit button instead.
  const [analysisKeyInput, setAnalysisKeyInput] = useState("");

  // Reset only when the workspace changes, not on every cached-object
  // replacement — an unrelated mutation must not wipe an unsaved edit.
  useEffect(() => {
    setEdits({});
    setStaleHoursText(null);
    setConsultText(null);
    setAnalysisKeyInput("");
  }, [workspace?.id]);

  const draft: RoutingSettings = useMemo(() => {
    const next: RoutingSettings = { ...saved, ...edits };
    if (staleHoursText !== null) {
      next.stale_review_hours = normalizeStaleReviewHours(Number(staleHoursText));
    }
    if (consultText !== null) {
      next.consult_per_issue = Math.min(20, Math.max(1, Math.round(Number(consultText) || 3)));
    }
    return next;
  }, [saved, edits, staleHoursText, consultText]);

  const set = (patch: Partial<RoutingSettings>) =>
    setEdits((prev) => ({ ...prev, ...patch }));
  const setAnalysis = (patch: Partial<RoutingAnalysisSettings>) =>
    setEdits((prev) => ({
      ...prev,
      analysis: { ...(prev.analysis ?? saved.analysis), ...patch },
    }));

  const writeSettings = async (settings: Record<string, unknown>) => {
    if (!workspace) return;
    const updated = await api.updateWorkspace(workspace.id, { settings });
    qc.setQueryData(
      workspaceListOptions().queryKey,
      (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
    );
    // The health report is derived from the settings that were just
    // replaced, so the cached copy is stale the moment this resolves.
    await qc.invalidateQueries({
      queryKey: workspaceKeys.routingHealth(workspace.id),
    });
  };

  const autoSave = useAutoSave({
    value: draft,
    savedValue: saved,
    onSave: (next) => writeSettings(withRoutingSettings(workspace?.settings, next)),
    enabled: !!workspace && canManage,
    isEqual: sameRoutingSettings,
  });

  // Sub-pages remount the tab, which would drop an edit still inside the
  // debounce window; write it on the way out instead.
  const { flush } = autoSave;
  useEffect(() => flush, [flush]);

  // Live health from the server: the stored fields cannot know that the
  // model is rejected or cooling down.
  const health = useQuery({
    ...routingHealthOptions(workspace?.id ?? ""),
    enabled: !!workspace?.id,
  });

  const recheck = useMutation({
    mutationFn: () => api.checkRoutingHealth(workspace?.id ?? ""),
    onSuccess: (next) => {
      qc.setQueryData(workspaceKeys.routingHealth(workspace?.id ?? ""), next);
    },
    onError: (error) =>
      toast.error(error instanceof Error && error.message ? error.message : t(($) => $.routing.health_recheck_failed)),
  });

  // The key write sends the rest of the draft along because the server
  // replaces the whole settings column — the key alone would revert an
  // unsaved edit made in the same sitting.
  const saveKey = useMutation({
    mutationFn: ({ role, key }: { role: RoutingRole; key: string }) =>
      writeSettings(
        role === "judge"
          ? withRoutingSettings(workspace?.settings, draft, key)
          : withRoutingSettings(workspace?.settings, draft, undefined, key),
      ),
    onError: (error) =>
      toast.error(error instanceof Error && error.message ? error.message : t(($) => $.routing.gateway_key_save_error)),
    // Cleared whichever way it went: on failure leaving a credential in a
    // text box behind a red message is not something to do to somebody.
    onSettled: () => setAnalysisKeyInput(""),
  });

  return {
    workspace,
    wsId: workspace?.id ?? "",
    userId,
    canManage,
    saved,
    draft,
    set,
    setAnalysis,
    staleHoursText: staleHoursText ?? String(draft.stale_review_hours),
    setStaleHoursText,
    consultText: consultText ?? String(draft.consult_per_issue ?? 3),
    setConsultText,
    analysisKeyInput,
    setAnalysisKeyInput,
    autoSave,
    health,
    recheck,
    saveKey,
    // The draft wins over the server report while an edit is in flight.
    state: routingState(draft, health.data),
  };
}

export type RoutingForm = ReturnType<typeof useRoutingForm>;

/**
 * Equal once whitespace and stored-vs-default spellings are ignored: a field
 * the server omits and the same field at its default are one setting, or the
 * page would save on open and keep saving.
 */
export function sameRoutingSettings(a: RoutingSettings, b: RoutingSettings): boolean {
  return stableJSON(comparable(a)) === stableJSON(comparable(b));
}

function comparable(s: RoutingSettings) {
  return {
    ...s,
    consult_per_issue: s.consult_per_issue ?? 3,
    analysis: {
      ...s.analysis,
      source: s.analysis.source ?? "api_gateway",
      thinking_level: s.analysis.thinking_level ?? "low",
    },
  };
}

function stableJSON(value: unknown): string {
  return JSON.stringify(value, (_key, v: unknown) => {
    if (typeof v === "string") return v.trim();
    if (v && typeof v === "object" && !Array.isArray(v)) {
      return Object.fromEntries(
        Object.entries(v as Record<string, unknown>)
          .filter(([, inner]) => inner !== undefined && !(typeof inner === "string" && inner.trim() === ""))
          .sort(([x], [y]) => x.localeCompare(y)),
      );
    }
    return v;
  });
}
