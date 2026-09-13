"use client";

import { Gauge } from "lucide-react";
import type {
  AgentRuntime,
  PlanLimitWindow,
  PlanLimitsSnapshot,
} from "@multica/core/types";
import { useT, useTimeAgo } from "../../i18n";

const SNAPSHOT_MAX_AGE_MS = 24 * 60 * 60 * 1000;

export interface DisplayPlanLimits {
  snapshot: PlanLimitsSnapshot;
  windows: PlanLimitWindow[];
}

/**
 * Drops windows after their reset boundary and expires observations after a
 * day. This prevents the UI from presenting an old exhausted state or usage
 * percentage as current when a daemon has stopped reporting.
 */
export function displayPlanLimits(
  snapshot: PlanLimitsSnapshot | null | undefined,
  nowMs = Date.now(),
): DisplayPlanLimits | null {
  if (!snapshot || snapshot.observed_at <= 0) return null;

  const nowSeconds = Math.floor(nowMs / 1000);
  const reportedWindows = snapshot.windows ?? [];
  const windows = reportedWindows.filter(
    (window) => window.resets_at == null || window.resets_at > nowSeconds,
  );
  if (reportedWindows.length > 0 && windows.length === 0) return null;

  const observedAge = nowMs - snapshot.observed_at * 1000;
  if (observedAge > SNAPSHOT_MAX_AGE_MS) return null;
  if (snapshot.status === "available" && windows.length === 0) return null;

  return { snapshot, windows };
}

export function planLimitWindowShortLabel(window: PlanLimitWindow): string {
  switch (window.name) {
    case "gemini_pro":
      return "Pro";
    case "gemini_flash":
      return "Flash";
    case "gemini_flash_lite":
      return "Lite";
    case "credits":
      return "Credits";
    case "five_hour":
      return "5h";
    case "seven_day":
      return "7d";
    case "balance_cny":
      return "¥";
    case "balance_usd":
      return "$";
    default:
      break;
  }
  if (window.window_minutes === 300) return "5h";
  if (window.window_minutes === 10_080) return "7d";
  return window.name;
}

export function isBalanceWindow(window: PlanLimitWindow): boolean {
  return window.name === "balance_cny" || window.name === "balance_usd";
}

export function formatPlanLimitRemaining(window: PlanLimitWindow): string | null {
  if (window.remaining == null || !isBalanceWindow(window)) return null;
  const amount = formatMoney(window.remaining);
  return window.name === "balance_cny" ? `¥${amount}` : `$${amount}`;
}

function formatMoney(value: number): string {
  if (!Number.isFinite(value)) return "0";
  const fixed = value.toFixed(2);
  return fixed.replace(/\.00$/, "").replace(/(\.\d)0$/, "$1");
}

export function quotaWindowSummaryParts(windows: PlanLimitWindow[]): string[] {
  const parts: string[] = [];
  for (const window of windows) {
    const remaining = formatPlanLimitRemaining(window);
    if (remaining) {
      parts.push(remaining);
      continue;
    }
    if (window.used_percent != null) {
      parts.push(
        `${planLimitWindowShortLabel(window)} ${Math.round(window.used_percent)}%`,
      );
    }
  }
  return parts;
}

function percentageTone(value: number): string {
  if (value >= 100) return "text-destructive";
  if (value >= 80) return "text-warning";
  return "text-foreground";
}

function percentageBarTone(value: number): string {
  if (value >= 100) return "bg-destructive";
  if (value >= 80) return "bg-warning";
  return "bg-primary";
}

export function PlanLimitsCell({
  runtime,
  now = Date.now(),
}: {
  runtime: AgentRuntime;
  now?: number;
}) {
  const { t } = useT("runtimes");
  const display = displayPlanLimits(runtime.plan_limits, now);
  if (!display) {
    return <span className="text-caption text-faint-foreground">—</span>;
  }

  const parts = quotaWindowSummaryParts(display.windows).slice(0, 2);
  if (parts.length === 0) {
    return (
      <span className="truncate text-caption font-medium text-destructive">
        {t(($) => $.plan_limits.limit_reached)}
      </span>
    );
  }

  return (
    <div
      className="flex min-w-0 flex-col leading-tight"
      aria-label={t(($) => $.plan_limits.title)}
    >
      {parts.map((part) => (
        <span
          key={part}
          className="truncate text-caption tabular-nums text-foreground"
        >
          {part}
        </span>
      ))}
    </div>
  );
}

function windowLabel(
  window: PlanLimitWindow,
  t: ReturnType<typeof useT<"runtimes">>["t"],
): string {
  switch (window.name) {
    case "gemini_pro":
      return t(($) => $.plan_limits.window_pro);
    case "gemini_flash":
      return t(($) => $.plan_limits.window_flash);
    case "gemini_flash_lite":
      return t(($) => $.plan_limits.window_flash_lite);
    case "credits":
      return t(($) => $.plan_limits.window_credits);
    case "five_hour":
      return t(($) => $.plan_limits.window_5h);
    case "seven_day":
      return t(($) => $.plan_limits.window_7d);
    case "balance_cny":
      return t(($) => $.plan_limits.window_balance_cny);
    case "balance_usd":
      return t(($) => $.plan_limits.window_balance_usd);
    default:
      break;
  }
  if (window.window_minutes === 300) {
    return t(($) => $.plan_limits.window_5h);
  }
  if (window.window_minutes === 10_080) {
    return t(($) => $.plan_limits.window_7d);
  }
  if (window.name === "primary") {
    return t(($) => $.plan_limits.window_primary);
  }
  if (window.name === "secondary") {
    return t(($) => $.plan_limits.window_secondary);
  }
  return window.name;
}

export function PlanLimitsCard({
  runtime,
  now = Date.now(),
}: {
  runtime: AgentRuntime;
  now?: number;
}) {
  const { t } = useT("runtimes");
  const timeAgo = useTimeAgo();
  const display = displayPlanLimits(runtime.plan_limits, now);
  const observed = display
    ? timeAgo(new Date(display.snapshot.observed_at * 1000).toISOString())
    : null;

  return (
    <section className="rounded-lg border bg-card">
      <div className="flex items-center justify-between gap-3 border-b px-4 py-3">
        <div className="flex items-center gap-2">
          <Gauge className="h-4 w-4 text-muted-foreground" />
          <h3 className="text-body font-semibold">
            {t(($) => $.plan_limits.title)}
          </h3>
        </div>
        {observed && (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.plan_limits.observed, { when: observed })}
          </span>
        )}
      </div>

      {!display ? (
        <div className="px-4 py-5">
          <p className="text-body font-medium">
            {t(($) => $.plan_limits.unavailable)}
          </p>
          <p className="mt-1 text-caption text-muted-foreground">
            {runtime.provider === "claude"
              ? t(($) => $.plan_limits.unavailable_hint_claude)
              : t(($) => $.plan_limits.unavailable_hint)}
          </p>
        </div>
      ) : display.windows.length === 0 ? (
        <div className="px-4 py-5">
          <p className="text-body font-medium text-destructive">
            {t(($) => $.plan_limits.limit_reached)}
          </p>
        </div>
      ) : (
        <div className="divide-y">
          {display.windows.map((window) => {
            const used = window.used_percent;
            const balance = formatPlanLimitRemaining(window);
            const reset = window.resets_at
              ? timeAgo(new Date(window.resets_at * 1000).toISOString())
              : null;
            return (
              <div key={window.name} className="px-4 py-3">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-caption font-medium">
                    {windowLabel(window, t)}
                  </span>
                  {balance ? (
                    <span className="text-caption font-semibold tabular-nums text-foreground">
                      {balance}
                    </span>
                  ) : used != null ? (
                    <span
                      className={`text-caption font-semibold tabular-nums ${percentageTone(used)}`}
                    >
                      {t(($) => $.plan_limits.used, {
                        percent: Math.round(used),
                      })}
                    </span>
                  ) : (
                    <span className="text-caption font-medium text-destructive">
                      {t(($) => $.plan_limits.limit_reached)}
                    </span>
                  )}
                </div>
                {used != null && !isBalanceWindow(window) && (
                  <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-muted">
                    <div
                      className={`h-full rounded-full ${percentageBarTone(used)}`}
                      style={{ width: `${Math.min(100, used)}%` }}
                    />
                  </div>
                )}
                {reset && (
                  <p className="mt-1.5 text-caption text-muted-foreground">
                    {t(($) => $.plan_limits.resets, { when: reset })}
                  </p>
                )}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}
