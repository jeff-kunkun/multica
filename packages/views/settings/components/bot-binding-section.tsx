"use client";

import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentMember } from "@multica/core/permissions";
import {
  botBindingOptions,
  externalBotListOptions,
  useDeleteBotBinding,
  usePutBotBinding,
  useVerifyBotBinding,
} from "@multica/core/bots";
import { Button } from "@multica/ui/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Switch } from "@multica/ui/components/ui/switch";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection } from "./settings-layout";

const NO_BOT = "none";

/**
 * 把要我决定的事交给我的 Bot (DENE-1933). Each person turns this on for
 * themselves: pick your own Bot, and whatever needs your decision is pushed to
 * it. The server decides the state (the Bot has to confirm it can receive).
 */
export function BotBindingSection() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const { role } = useCurrentMember(wsId);
  // Only owners and admins add Bots, so only they have one of their own to pick.
  const canListBots = role === "owner" || role === "admin";
  const { data: binding } = useQuery(botBindingOptions(wsId));
  const { data: bots = [] } = useQuery(externalBotListOptions(wsId, canListBots));
  const put = usePutBotBinding(wsId);
  const remove = useDeleteBotBinding(wsId);
  const verify = useVerifyBotBinding(wsId);

  const activeBots = bots.filter((bot) => bot.status === "active");
  const state = binding?.state ?? "none";
  const selected = binding?.bound ? (binding.bot_id ?? NO_BOT) : NO_BOT;
  const busy = put.isPending || remove.isPending || verify.isPending;
  const name = binding?.bot_name ?? "";

  // A bound Bot that was stopped is no longer in the active list, but the
  // selector still has to show its name rather than its id.
  const boundId = binding?.bound ? binding.bot_id : undefined;
  const boundGone = !!boundId && !activeBots.some((bot) => bot.id === boundId);
  const items = [
    { value: NO_BOT, label: t(($) => $.notifications.decisions.none) },
    ...(boundGone && boundId ? [{ value: boundId, label: name }] : []),
    ...activeBots.map((bot) => ({ value: bot.id, label: bot.name })),
  ];

  const fail = (err: unknown) =>
    toast.error(
      err instanceof Error && err.message
        ? err.message
        : t(($) => $.notifications.decisions.toast_failed),
    );

  const onSelect = (value: string | null) => {
    if (!value || value === selected) return;
    if (value === NO_BOT) {
      remove.mutate(undefined, { onError: fail });
      return;
    }
    put.mutate({ bot_id: value }, { onError: fail });
  };

  const description =
    state === "ok"
      ? t(($) => $.notifications.decisions.state_ok, { name })
      : state === "unverified"
        ? t(($) => $.notifications.decisions.state_unverified, { name })
        : state === "broken"
          ? t(($) => $.notifications.decisions.state_broken, { name })
          : activeBots.length === 0
            ? t(($) => $.notifications.decisions.no_bots)
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
          size="select-wide"
        >
          <div className="flex items-center gap-2">
            <Select
              items={items}
              value={selected}
              onValueChange={onSelect}
              disabled={busy || (activeBots.length === 0 && selected === NO_BOT)}
            >
              <SelectTrigger
                className="w-full min-w-0"
                aria-label={t(($) => $.notifications.decisions.label)}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {items.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {state === "unverified" || state === "broken" ? (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="shrink-0"
                disabled={busy}
                onClick={() => verify.mutate(undefined, { onError: fail })}
              >
                {t(($) => $.notifications.decisions.check_again)}
              </Button>
            ) : null}
          </div>
        </SettingsRow>
        {binding?.bound ? (
          <SettingsRow
            anchor="decisions-mirror"
            label={t(($) => $.notifications.decisions.mirror_label)}
            description={t(($) => $.notifications.decisions.mirror_hint)}
          >
            <Switch
              checked={binding.mode === "mirror"}
              disabled={busy}
              aria-label={t(($) => $.notifications.decisions.mirror_label)}
              onCheckedChange={(checked) =>
                put.mutate({ mode: checked ? "mirror" : "bot_first" }, { onError: fail })
              }
            />
          </SettingsRow>
        ) : null}
      </SettingsCard>
    </SettingsSection>
  );
}
