"use client";

import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { botBindingOptions, botWritesOptions } from "@multica/core/bots";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection } from "./settings-layout";

/**
 * 要我决定的事交给我的 Bot (DENE-1933). Since DENE-2009 nobody picks here:
 * agreeing to a Bot's connect request binds it to the person who agreed, with
 * every activity flowing to it, so this only says where things stand and what
 * the Bot did in their name.
 */
export function BotBindingSection() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const { data: binding } = useQuery(botBindingOptions(wsId));
  const state = binding?.state ?? "none";
  const name = binding?.bot_name ?? "";

  const writes = useInfiniteQuery({ ...botWritesOptions(wsId), enabled: !!wsId && !!binding?.bound });
  const writeRows = writes.data?.pages.flatMap((page) => page.writes) ?? [];

  const familyLabels: Record<string, string> = {
    pr: t(($) => $.notifications.decisions.writes_family.pr),
    run: t(($) => $.notifications.decisions.writes_family.run),
    project: t(($) => $.notifications.decisions.writes_family.project),
    settings: t(($) => $.notifications.decisions.writes_family.settings),
    member: t(($) => $.notifications.decisions.writes_family.member),
    agent: t(($) => $.notifications.decisions.writes_family.agent),
    autopilot: t(($) => $.notifications.decisions.writes_family.autopilot),
    skill: t(($) => $.notifications.decisions.writes_family.skill),
    runtime: t(($) => $.notifications.decisions.writes_family.runtime),
    integration: t(($) => $.notifications.decisions.writes_family.integration),
    squad: t(($) => $.notifications.decisions.writes_family.squad),
    routing: t(($) => $.notifications.decisions.writes_family.routing),
    billing: t(($) => $.notifications.decisions.writes_family.billing),
    access: t(($) => $.notifications.decisions.writes_family.access),
    issue: t(($) => $.notifications.decisions.writes_family.issue),
    other: t(($) => $.notifications.decisions.writes_family.other),
  };
  const familyLabel = (family: string) => familyLabels[family] ?? familyLabels.other;

  const description =
    state === "ok"
      ? t(($) => $.notifications.decisions.state_ok, { name })
      : state === "unverified"
        ? t(($) => $.notifications.decisions.state_unverified, { name })
        : state === "broken"
          ? t(($) => $.notifications.decisions.state_broken, { name })
          : t(($) => $.notifications.decisions.state_none);

  return (
    <SettingsSection
      title={t(($) => $.notifications.decisions.title)}
      anchor="decisions"
    >
      <SettingsCard>
        <SettingsRow
          anchor="decisions-bot"
          label={t(($) => $.notifications.decisions.label)}
          description={
            <span className={state === "broken" ? "text-destructive" : undefined}>
              {description}
            </span>
          }
        >
          {null}
        </SettingsRow>
        {binding?.bound ? (
          <SettingsRow
            anchor="decisions-writes"
            label={t(($) => $.notifications.decisions.writes_title)}
            description={writeRows.length === 0 ? t(($) => $.notifications.decisions.writes_empty) : undefined}
            size="select-wide"
          >
            {writeRows.length > 0 ? (
              <ul className="flex min-w-0 flex-col gap-2 text-sm">
                {writeRows.map((row) => (
                  <li key={row.id} className="min-w-0">
                    <span className="block [overflow-wrap:anywhere]">
                      {t(($) => $.notifications.decisions.writes_who, {
                        bot: row.bot_name,
                        owner: row.on_behalf_of_name,
                      })}
                    </span>
                    <span
                      className={`block text-caption [overflow-wrap:anywhere] ${
                        row.outcome === "blocked" ? "text-destructive" : "text-muted-foreground"
                      }`}
                    >
                      {row.outcome === "blocked"
                        ? t(($) => $.notifications.decisions.writes_blocked)
                        : t(($) => $.notifications.decisions.writes_line, {
                            family: familyLabel(row.family),
                            fields: row.fields.join(", ") || "-",
                          })}
                    </span>
                  </li>
                ))}
                {writes.hasNextPage ? (
                  <li>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      disabled={writes.isFetchingNextPage}
                      onClick={() => void writes.fetchNextPage()}
                    >
                      {t(($) => $.notifications.decisions.writes_more)}
                    </Button>
                  </li>
                ) : null}
              </ul>
            ) : null}
          </SettingsRow>
        ) : null}
      </SettingsCard>
    </SettingsSection>
  );
}
