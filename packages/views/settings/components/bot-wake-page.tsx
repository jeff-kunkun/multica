"use client";

import { useEffect, useState, type ReactNode } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError } from "@multica/core/api";
import type {
  BotQuietHours,
  BotWakeEvent,
  BotWakeEventStatus,
  BotWakeEventType,
  BotWakeSettings,
  ExternalBot,
} from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  BOT_WAKE_EVENT_TYPES,
  botWakeEventsOptions,
  botWakeOptions,
  useUpdateBotWake,
} from "@multica/core/bots";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Switch } from "@multica/ui/components/ui/switch";
import { TimeInput } from "@multica/ui/components/ui/time-input";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { AppLink } from "../../navigation";
import { useLocale, useT } from "../../i18n";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
  SettingsViewTabs,
} from "./settings-layout";

type Rules = Pick<BotWakeSettings, "events" | "hourly_limit" | "quiet_hours">;

const sameRules = (a: Rules, b: Rules) =>
  a.hourly_limit === b.hourly_limit &&
  a.events.length === b.events.length &&
  a.events.every((e) => b.events.includes(e)) &&
  JSON.stringify(a.quiet_hours) === JSON.stringify(b.quiet_hours);

const errorStatus = (e: unknown) => (e instanceof ApiError ? e.status : 0);

// Wake settings for one external bot (DENE-1793): where Multica POSTs event
// summaries, which events may wake it, and how often. The server owns every
// rule; this page renders only what it answered.
export function BotWakePage({ bot }: { bot: ExternalBot }) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const wake = useQuery(botWakeOptions(wsId, bot.id));

  if (wake.isLoading) {
    return (
      <SettingsCard>
        {Array.from({ length: 3 }).map((_, i) => (
          <div key={i} className="flex min-h-16 items-center px-4 py-3.5">
            <div className="flex-1 space-y-1.5">
              <Skeleton className="h-4 w-32" />
              <Skeleton className="h-3 w-56" />
            </div>
          </div>
        ))}
      </SettingsCard>
    );
  }

  if (!wake.data) {
    const status = errorStatus(wake.error);
    return (
      <SettingsCard>
        <SettingsRow
          label={
            status === 403
              ? t(($) => $.bots.wake.forbidden)
              : status === 404
                ? t(($) => $.bots.wake.not_found)
                : t(($) => $.bots.wake.load_failed)
          }
        >
          {status === 403 || status === 404 ? null : (
            <Button size="sm" variant="outline" onClick={() => void wake.refetch()}>
              {t(($) => $.bots.wake.retry)}
            </Button>
          )}
        </SettingsRow>
      </SettingsCard>
    );
  }

  return (
    <>
      <WebhookSection bot={bot} settings={wake.data} />
      <RulesSection bot={bot} settings={wake.data} />
      <DeliveryLog bot={bot} />
    </>
  );
}

// URL and key are write-only: the form starts empty every time, and an empty
// form is never sent, so leaving it alone cannot clear a saved webhook.
function WebhookSection({ bot, settings }: { bot: ExternalBot; settings: BotWakeSettings }) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const update = useUpdateBotWake(wsId, bot.id);
  const [url, setUrl] = useState("");
  const [key, setKey] = useState("");
  const [confirmDisconnect, setConfirmDisconnect] = useState(false);
  const configured = settings.webhook_configured;
  const canSave = url.trim() !== "" && key.trim() !== "" && !update.isPending;

  const fail = (e: unknown) =>
    toast.error(e instanceof Error && e.message ? e.message : t(($) => $.bots.toast_failed));

  const save = () => {
    if (!canSave) return;
    update.mutate(
      { webhook_url: url.trim(), webhook_key: key.trim() },
      {
        onSuccess: () => {
          setUrl("");
          setKey("");
          toast.success(t(($) => $.bots.wake.toast_webhook_saved));
        },
        onError: fail,
      },
    );
  };

  const disconnect = () =>
    update.mutate(
      { webhook_url: "", webhook_key: "" },
      {
        onSuccess: () => {
          setConfirmDisconnect(false);
          toast.success(t(($) => $.bots.wake.toast_disconnected));
        },
        onError: fail,
      },
    );

  return (
    <SettingsSection
      title={t(($) => $.bots.wake.webhook_title)}
      description={t(($) => $.bots.wake.webhook_description)}
      action={
        <span
          data-testid="bot-webhook-status"
          className={cn(
            "inline-flex items-center gap-1.5 text-caption",
            configured ? "text-success" : "text-muted-foreground",
          )}
        >
          {configured ? (
            <span aria-hidden="true" className="size-1.5 rounded-full bg-success" />
          ) : null}
          {configured
            ? t(($) => $.bots.wake.webhook_configured)
            : t(($) => $.bots.wake.webhook_missing)}
        </span>
      }
    >
      <SettingsCard>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            save();
          }}
        >
          <SettingsRow label={t(($) => $.bots.wake.url)} size="text">
            <Input
              id="bot-webhook-url"
              type="url"
              inputMode="url"
              autoComplete="off"
              spellCheck={false}
              aria-label={t(($) => $.bots.wake.url)}
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder={
                configured
                  ? t(($) => $.bots.wake.saved_placeholder)
                  : "https://"
              }
            />
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.bots.wake.key)}
            description={t(($) => $.bots.wake.key_hint)}
            size="text"
          >
            <Input
              id="bot-webhook-key"
              type="password"
              autoComplete="new-password"
              spellCheck={false}
              aria-label={t(($) => $.bots.wake.key)}
              value={key}
              onChange={(e) => setKey(e.target.value)}
              placeholder={
                configured
                  ? t(($) => $.bots.wake.saved_placeholder)
                  : t(($) => $.bots.wake.key_placeholder)
              }
            />
          </SettingsRow>
          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-surface-border px-4 py-3">
            {configured ? (
              <Button
                type="button"
                variant="ghost"
                className="text-destructive hover:text-destructive"
                disabled={update.isPending}
                onClick={() => setConfirmDisconnect(true)}
              >
                {t(($) => $.bots.wake.disconnect)}
              </Button>
            ) : null}
            <Button type="submit" disabled={!canSave}>
              {update.isPending
                ? t(($) => $.bots.wake.saving)
                : configured
                  ? t(($) => $.bots.wake.replace_webhook)
                  : t(($) => $.bots.wake.save_webhook)}
            </Button>
          </div>
        </form>
      </SettingsCard>

      <AlertDialog
        open={confirmDisconnect}
        onOpenChange={(v) => { if (!v && !update.isPending) setConfirmDisconnect(false); }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.bots.wake.disconnect_dialog.title, { name: bot.name })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.bots.wake.disconnect_dialog.description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={update.isPending}>{t(($) => $.bots.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={update.isPending}
              onClick={(e) => {
                e.preventDefault();
                disconnect();
              }}
            >
              {t(($) => $.bots.wake.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  );
}

function RulesSection({ bot, settings }: { bot: ExternalBot; settings: BotWakeSettings }) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const update = useUpdateBotWake(wsId, bot.id);
  const saved: Rules = {
    events: settings.events,
    hourly_limit: settings.hourly_limit,
    quiet_hours: settings.quiet_hours,
  };
  const [draft, setDraft] = useState<Rules>(saved);
  const [limitText, setLimitText] = useState(String(saved.hourly_limit));

  // A save (or a bot changing its own rules via CLI and a refetch) replaces
  // the baseline; follow it whenever nothing local is pending.
  const savedKey = JSON.stringify(saved);
  useEffect(() => {
    setDraft(saved);
    setLimitText(String(saved.hourly_limit));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [savedKey]);

  const limitValid = /^\d{1,3}$/.test(limitText) && Number(limitText) <= 120;
  const next: Rules = { ...draft, hourly_limit: limitValid ? Number(limitText) : draft.hourly_limit };
  const dirty = !limitValid || !sameRules(next, saved);

  const toggleEvent = (type: BotWakeEventType, on: boolean) =>
    setDraft((d) => ({
      ...d,
      // Keep server order so an unchanged set compares equal.
      events: BOT_WAKE_EVENT_TYPES.filter((e) => (e === type ? on : d.events.includes(e))),
    }));
  const setQuiet = (patch: Partial<BotQuietHours>) =>
    setDraft((d) => ({ ...d, quiet_hours: { ...d.quiet_hours, ...patch } }));

  const save = () => {
    if (!limitValid || update.isPending) return;
    update.mutate(next, {
      onSuccess: () => toast.success(t(($) => $.bots.wake.toast_rules_saved)),
      onError: (e) =>
        toast.error(e instanceof Error && e.message ? e.message : t(($) => $.bots.toast_failed)),
    });
  };

  const reset = () => {
    setDraft(saved);
    setLimitText(String(saved.hourly_limit));
  };

  return (
    <>
      <SettingsSection
        title={t(($) => $.bots.wake.events_title)}
        description={t(($) => $.bots.wake.events_description)}
      >
        <SettingsCard>
          {BOT_WAKE_EVENT_TYPES.map((type) => {
            const id = `bot-wake-event-${type}`;
            return (
              <label
                key={type}
                htmlFor={id}
                className="flex min-h-14 cursor-pointer items-center gap-3 px-4 py-3 hover:bg-surface-hover"
              >
                <Checkbox
                  id={id}
                  checked={draft.events.includes(type)}
                  onCheckedChange={(v) => toggleEvent(type, v === true)}
                />
                <span className="min-w-0 flex-1">
                  <span className="block text-body font-medium">
                    {t(($) => $.bots.wake.event[type].label)}
                  </span>
                  <span className="mt-0.5 block text-caption leading-5 text-muted-foreground">
                    {t(($) => $.bots.wake.event[type].description)}
                  </span>
                </span>
              </label>
            );
          })}
        </SettingsCard>
      </SettingsSection>

      <SettingsSection title={t(($) => $.bots.wake.limits_title)}>
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.bots.wake.hourly_limit)}
            description={
              limitValid
                ? t(($) => $.bots.wake.hourly_limit_hint)
                : <span className="text-destructive">{t(($) => $.bots.wake.hourly_limit_invalid)}</span>
            }
            size="code"
          >
            <div className="flex items-center gap-2">
              <Input
                id="bot-wake-limit"
                inputMode="numeric"
                aria-label={t(($) => $.bots.wake.hourly_limit)}
                aria-invalid={!limitValid || undefined}
                value={limitText}
                onChange={(e) => setLimitText(e.target.value.trim())}
                className="w-20"
              />
              <span className="text-caption text-muted-foreground">
                {t(($) => $.bots.wake.per_hour)}
              </span>
            </div>
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.bots.wake.quiet)}
            description={t(($) => $.bots.wake.quiet_hint)}
          >
            <Switch
              aria-label={t(($) => $.bots.wake.quiet)}
              checked={draft.quiet_hours.enabled}
              onCheckedChange={(v) => setQuiet({ enabled: v })}
            />
          </SettingsRow>
          {draft.quiet_hours.enabled ? (
            <SettingsRow label={t(($) => $.bots.wake.quiet_window)} size="text">
              <div className="flex flex-wrap items-center gap-2">
                <TimeInput
                  value={draft.quiet_hours.start}
                  onChange={(v) => setQuiet({ start: v })}
                  hourLabel={t(($) => $.bots.wake.quiet_start)}
                />
                <span className="text-caption text-muted-foreground">–</span>
                <TimeInput
                  value={draft.quiet_hours.end}
                  onChange={(v) => setQuiet({ end: v })}
                  hourLabel={t(($) => $.bots.wake.quiet_end)}
                />
              </div>
            </SettingsRow>
          ) : null}
          {draft.quiet_hours.enabled ? (
            <SettingsRow
              label={t(($) => $.bots.wake.timezone)}
              description={t(($) => $.bots.wake.timezone_hint)}
              size="select-wide"
            >
              <Input
                id="bot-wake-timezone"
                autoComplete="off"
                spellCheck={false}
                aria-label={t(($) => $.bots.wake.timezone)}
                value={draft.quiet_hours.timezone}
                onChange={(e) => setQuiet({ timezone: e.target.value.trim() })}
              />
            </SettingsRow>
          ) : null}
          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-surface-border px-4 py-3">
            {dirty ? (
              <Button type="button" variant="ghost" onClick={reset} disabled={update.isPending}>
                {t(($) => $.bots.wake.reset)}
              </Button>
            ) : null}
            <Button
              type="button"
              onClick={save}
              disabled={!dirty || !limitValid || update.isPending}
            >
              {update.isPending ? t(($) => $.bots.wake.saving) : t(($) => $.bots.wake.save_rules)}
            </Button>
          </div>
        </SettingsCard>
      </SettingsSection>
    </>
  );
}

type LogView = "recent" | "pending";

function DeliveryLog({ bot }: { bot: ExternalBot }) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const [view, setView] = useState<LogView>("recent");
  const log = useInfiniteQuery(botWakeEventsOptions(wsId, bot.id, view === "pending"));
  const events = log.data?.pages.flatMap((p) => p.events) ?? [];

  let body: ReactNode;
  if (log.isLoading) {
    body = (
      <div className="flex min-h-16 items-center px-4 py-3.5">
        <div className="flex-1 space-y-1.5">
          <Skeleton className="h-4 w-40" />
          <Skeleton className="h-3 w-64" />
        </div>
      </div>
    );
  } else if (log.isError && events.length === 0) {
    body = (
      <SettingsRow label={t(($) => $.bots.wake.log_failed)}>
        <Button size="sm" variant="outline" onClick={() => void log.refetch()}>
          {t(($) => $.bots.wake.retry)}
        </Button>
      </SettingsRow>
    );
  } else if (events.length === 0 && !log.hasNextPage) {
    body = (
      <div className="px-4 py-6 text-center text-caption text-muted-foreground">
        {view === "pending"
          ? t(($) => $.bots.wake.empty_pending)
          : t(($) => $.bots.wake.empty_recent)}
      </div>
    );
  } else {
    body = (
      <>
        {events.map((event) => (
          <EventRow key={`${event.event_id}-${event.sequence}`} event={event} />
        ))}
        {log.hasNextPage ? (
          <div className="flex justify-center px-4 py-3">
            <Button
              size="sm"
              variant="ghost"
              disabled={log.isFetchingNextPage}
              onClick={() => void log.fetchNextPage()}
            >
              {log.isFetchingNextPage
                ? t(($) => $.bots.wake.loading)
                : t(($) => $.bots.wake.load_more)}
            </Button>
          </div>
        ) : null}
      </>
    );
  }

  return (
    <SettingsSection
      title={t(($) => $.bots.wake.log_title)}
      description={
        view === "pending"
          ? t(($) => $.bots.wake.log_pending_description)
          : t(($) => $.bots.wake.log_description)
      }
    >
      <SettingsViewTabs
        label={t(($) => $.bots.wake.log_title)}
        items={[
          { value: "recent", label: t(($) => $.bots.wake.log_recent) },
          { value: "pending", label: t(($) => $.bots.wake.log_pending) },
        ]}
        value={view}
        onChange={setView}
      />
      <SettingsCard>{body}</SettingsCard>
    </SettingsSection>
  );
}

const STATUS_TONE: Record<BotWakeEventStatus, string> = {
  queued: "text-muted-foreground",
  sending: "text-muted-foreground",
  received: "text-success",
  failed: "text-destructive",
  uncertain: "text-warning",
  pending: "text-muted-foreground",
};

function EventRow({ event }: { event: BotWakeEvent }) {
  const { t } = useT("settings");
  const locale = useLocale();
  const paths = useWorkspacePaths();
  const when = new Date(event.created_at).toLocaleString(locale, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
  const source = event.issue_id ? (
    <AppLink href={paths.issueDetail(event.issue_id)} className="underline-offset-2 hover:text-foreground hover:underline">
      {t(($) => $.bots.wake.source_issue)}
    </AppLink>
  ) : event.chat_session_id ? (
    <AppLink href={paths.chatSession(event.chat_session_id)} className="underline-offset-2 hover:text-foreground hover:underline">
      {t(($) => $.bots.wake.source_chat)}
    </AppLink>
  ) : null;

  return (
    <div data-testid="bot-wake-event" className="flex items-start gap-3 px-4 py-3">
      <div className="min-w-0 flex-1">
        <div className="text-body font-medium">
          {t(($) => $.bots.wake.event[event.type].label)}
        </div>
        <div className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-caption leading-5 text-muted-foreground">
          <span>{when}</span>
          {source ? <><span aria-hidden="true">·</span>{source}</> : null}
          {event.attempts > 1 ? (
            <>
              <span aria-hidden="true">·</span>
              <span>{t(($) => $.bots.wake.attempts, { count: event.attempts })}</span>
            </>
          ) : null}
        </div>
        {event.reason ? (
          <div className="mt-0.5 break-words text-caption leading-5 text-muted-foreground">
            {event.reason}
          </div>
        ) : null}
      </div>
      <span className={cn("shrink-0 text-caption font-medium", STATUS_TONE[event.status])}>
        {t(($) => $.bots.wake.status[event.status])}
      </span>
    </div>
  );
}
