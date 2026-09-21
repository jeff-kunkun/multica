"use client";

import { useQuery } from "@tanstack/react-query";
import { useCurrentWorkspace } from "@multica/core/paths";
import { providerDisplayName } from "@multica/core/runtimes";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import type {
  AgentRuntime,
  PlanLimitWindow,
  PlanLimitsSnapshot,
} from "@multica/core/types";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { ProviderLogo } from "../runtimes/components/provider-logo";
import {
  displayPlanLimits,
  formatPlanLimitRemaining,
  planLimitWindowShortLabel,
} from "../runtimes/components/plan-limits";
import { useLocale, useT } from "../i18n";

export interface ProviderQuotaSummary {
  provider: string;
  snapshot: PlanLimitsSnapshot;
  windows: PlanLimitWindow[];
}

/** Collapse runtimes onto the newest real snapshot for each provider. */
export function collectProviderQuotas(
  runtimes: readonly AgentRuntime[],
  nowMs = Date.now(),
): ProviderQuotaSummary[] {
  const summaries = new Map<string, ProviderQuotaSummary>();

  for (const runtime of runtimes) {
    const display = displayPlanLimits(runtime.plan_limits, nowMs);
    if (!display) continue;

    const provider = runtime.provider.trim().toLowerCase();
    const previous = summaries.get(provider);
    if (previous && previous.snapshot.observed_at >= display.snapshot.observed_at) {
      continue;
    }
    summaries.set(provider, {
      provider,
      snapshot: display.snapshot,
      windows: display.windows,
    });
  }

  return [...summaries.values()].sort((a, b) =>
    providerDisplayName(a.provider).localeCompare(providerDisplayName(b.provider)),
  );
}

export function ProviderStatusBar() {
  const workspace = useCurrentWorkspace();
  const { data: runtimes = [] } = useQuery({
    ...runtimeListOptions(workspace?.id ?? ""),
    enabled: Boolean(workspace),
  });

  return <ProviderStatusBarView providers={collectProviderQuotas(runtimes)} />;
}

export function ProviderStatusBarView({
  providers,
}: {
  providers: ProviderQuotaSummary[];
}) {
  const { t } = useT("runtimes");
  if (providers.length === 0) return null;

  return (
    <footer
      aria-label={t(($) => $.plan_limits.title)}
      className="flex h-10 shrink-0 items-center gap-4 overflow-x-auto border-t px-3 pe-chat-launcher"
    >
      {providers.map((provider) => (
        <ProviderStatusEntry key={provider.provider} provider={provider} />
      ))}
    </footer>
  );
}

function ProviderStatusEntry({
  provider,
}: {
  provider: ProviderQuotaSummary;
}) {
  const { t } = useT("runtimes");
  const locale = useLocale();
  const name = providerDisplayName(provider.provider);

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <div className="flex shrink-0 items-center gap-2 text-caption">
            <ProviderLogo provider={provider.provider} className="h-3.5 w-3.5" />
            <span className="font-medium">{name}</span>
            {provider.windows.length === 0 ? (
              <span className="font-medium text-destructive">
                {t(($) => $.plan_limits.limit_reached)}
              </span>
            ) : (
              provider.windows.map((window) => (
                <WindowSummary key={window.name} window={window} locale={locale} />
              ))
            )}
          </div>
        }
      />
      <TooltipContent side="top">
        <div className="space-y-1">
          <div className="font-medium">{name}</div>
          {provider.windows.length === 0 ? (
            <div>{t(($) => $.plan_limits.limit_reached)}</div>
          ) : (
            provider.windows.map((window) => (
              <WindowDetail key={window.name} window={window} locale={locale} />
            ))
          )}
        </div>
      </TooltipContent>
    </Tooltip>
  );
}

function WindowSummary({
  window,
  locale,
}: {
  window: PlanLimitWindow;
  locale: string;
}) {
  const remaining = remainingLabel(window);
  return (
    <span className={remainingTone(window)}>
      {planLimitWindowShortLabel(window)} {remaining ?? "—"}
      {window.resets_at ? ` · ${relativeReset(window.resets_at, locale)}` : ""}
    </span>
  );
}

function WindowDetail({
  window,
  locale,
}: {
  window: PlanLimitWindow;
  locale: string;
}) {
  const { t } = useT("runtimes");
  const remaining = remainingLabel(window);
  const reset = window.resets_at
    ? relativeReset(window.resets_at, locale)
    : null;

  return (
    <div>
      {planLimitWindowShortLabel(window)}: {remaining ?? t(($) => $.plan_limits.limit_reached)}
      {reset ? ` · ${t(($) => $.plan_limits.resets, { when: reset })}` : ""}
    </div>
  );
}

function remainingLabel(window: PlanLimitWindow): string | null {
  const prepaid = formatPlanLimitRemaining(window);
  if (prepaid) return prepaid;
  if (window.used_percent == null) return null;
  return `${Math.max(0, Math.round(100 - window.used_percent))}%`;
}

function remainingTone(window: PlanLimitWindow): string {
  if (window.used_percent == null) return "text-muted-foreground";
  const remaining = 100 - window.used_percent;
  if (remaining <= 0) return "text-destructive";
  if (remaining <= 20) return "text-warning";
  return "text-foreground";
}

function relativeReset(timestamp: number, locale: string): string {
  const deltaMs = timestamp * 1000 - Date.now();
  const absoluteMinutes = Math.max(1, Math.round(Math.abs(deltaMs) / 60_000));
  if (absoluteMinutes < 60) {
    return new Intl.RelativeTimeFormat(locale, { numeric: "auto" }).format(
      Math.sign(deltaMs) * absoluteMinutes,
      "minute",
    );
  }
  const absoluteHours = Math.max(1, Math.round(absoluteMinutes / 60));
  if (absoluteHours < 24) {
    return new Intl.RelativeTimeFormat(locale, { numeric: "auto" }).format(
      Math.sign(deltaMs) * absoluteHours,
      "hour",
    );
  }
  const absoluteDays = Math.max(1, Math.round(absoluteHours / 24));
  return new Intl.RelativeTimeFormat(locale, { numeric: "auto" }).format(
    Math.sign(deltaMs) * absoluteDays,
    "day",
  );
}
