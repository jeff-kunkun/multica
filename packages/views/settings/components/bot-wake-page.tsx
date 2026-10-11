"use client";

import { useEffect, useState, type CSSProperties, type ReactNode } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError } from "@multica/core/api";
import type {
  BotQuietHours,
  BotWakeEvent,
  BotWakeEventStatus,
  BotWakeEventType,
  BotWakeFrom,
  BotWakeFromRule,
  BotWakeSenderKind,
  BotWakeSettings,
  ExternalBot,
} from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  BOT_WAKE_ACK_MAX,
  BOT_WAKE_EVENT_TYPES,
  botWakeEventsOptions,
  botWakeOptions,
  externalBotListOptions,
  useAckBotWakePending,
  useUpdateBotWake,
} from "@multica/core/bots";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
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

// The key is a bot's credential, not the person's. A password-type field makes
// password managers offer to save (or overwrite!) the site login with it, and
// they ignore opt-out attributes on submit — so mask a text field instead.
const MASKED = { WebkitTextSecurity: "disc" } as CSSProperties;
// Phone-width touch targets stay 44px; desktop keeps the compact settings density.
const TAP = "h-11 sm:h-8";
const NOT_A_LOGIN = {
  "data-1p-ignore": true,
  "data-bwignore": true,
  "data-lpignore": "true",
  "data-form-type": "other",
} as const;

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
      {/* A cache snapshot from before the server knew wake_from can paint
          first; the section waits for the refetch instead of guessing. */}
      {wake.data.wake_from ? <WakeFromSection bot={bot} settings={wake.data} /> : null}
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
          {...NOT_A_LOGIN}
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
              {...NOT_A_LOGIN}
              className={TAP}
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
              type="text"
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              {...NOT_A_LOGIN}
              style={MASKED}
              className={TAP}
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
                className={cn("text-destructive hover:text-destructive", TAP)}
                disabled={update.isPending}
                onClick={() => setConfirmDisconnect(true)}
              >
                {t(($) => $.bots.wake.disconnect)}
              </Button>
            ) : null}
            <Button type="submit" className={TAP} disabled={!canSave}>
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

const SENDERS: readonly BotWakeSenderKind[] = ["member", "agent", "bot"];
const WAKE_FROM_MODES: readonly BotWakeFromRule["mode"][] = ["all", "none", "list"];

// Order-insensitive, so ticking and unticking back reads as unchanged.
const fromKey = (from: BotWakeFrom) =>
  JSON.stringify(SENDERS.map((k) => [from[k].mode, [...from[k].ids].sort()]));

// Who may wake the bot. The server replaces the whole map on save, so the
// draft always carries all three rules; ids the pickers no longer know about
// (a removed member, a deleted bot) are kept rather than silently dropped.
// The owner-follows switch rides the same draft and save button.
function WakeFromSection({ bot, settings }: { bot: ExternalBot; settings: BotWakeSettings }) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const update = useUpdateBotWake(wsId, bot.id);
  const saved = settings.wake_from;
  // A cache snapshot from before the field existed reads as the server default.
  const savedInherit = settings.inherit_owner_follows ?? false;
  const [draft, setDraft] = useState<BotWakeFrom>(saved);
  const [inherit, setInherit] = useState(savedInherit);
  const savedKey = fromKey(saved);
  useEffect(() => {
    setDraft(saved);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [savedKey]);
  useEffect(() => setInherit(savedInherit), [savedInherit]);

  const members = useQuery(memberListOptions(wsId));
  const agents = useQuery(agentListOptions(wsId));
  const bots = useQuery(externalBotListOptions(wsId));
  const candidates: Record<BotWakeSenderKind, { id: string; name: string }[]> = {
    member: (members.data ?? []).map((m) => ({ id: m.user_id, name: m.name })),
    agent: (agents.data ?? [])
      .filter((a) => !a.archived_at)
      .map((a) => ({ id: a.id, name: a.name })),
    // A bot never wakes itself, so it is not offered.
    bot: (bots.data ?? []).filter((b) => b.id !== bot.id).map((b) => ({ id: b.id, name: b.name })),
  };

  const tooMany = SENDERS.some((k) => draft[k].ids.length > 100);
  const fromDirty = fromKey(draft) !== savedKey;
  const inheritDirty = inherit !== savedInherit;
  const dirty = fromDirty || inheritDirty;

  const setRule = (kind: BotWakeSenderKind, rule: BotWakeFromRule) =>
    setDraft((d) => ({ ...d, [kind]: rule }));
  const setMode = (kind: BotWakeSenderKind, mode: BotWakeFromRule["mode"]) =>
    // The server rejects ids outside list mode; a list starts from what was saved.
    setRule(kind, { mode, ids: mode === "list" ? (saved[kind].mode === "list" ? saved[kind].ids : []) : [] });
  const toggleId = (kind: BotWakeSenderKind, id: string, on: boolean) =>
    setDraft((d) => ({
      ...d,
      [kind]: { ...d[kind], ids: on ? [...d[kind].ids, id] : d[kind].ids.filter((x) => x !== id) },
    }));

  const save = () => {
    if (!dirty || tooMany || update.isPending) return;
    update.mutate(
      {
        ...(fromDirty ? { wake_from: draft } : {}),
        ...(inheritDirty ? { inherit_owner_follows: inherit } : {}),
      },
      {
        onSuccess: () => toast.success(t(($) => $.bots.wake.toast_from_saved)),
        onError: (e) =>
          toast.error(e instanceof Error && e.message ? e.message : t(($) => $.bots.toast_failed)),
      },
    );
  };

  const modeItems = WAKE_FROM_MODES.map((m) => ({ value: m, label: t(($) => $.bots.wake[`mode_${m}`]) }));

  return (
    <SettingsSection
      title={t(($) => $.bots.wake.from_title)}
      description={t(($) => $.bots.wake.from_description)}
    >
      <SettingsCard>
        {SENDERS.map((kind) => {
          const rule = draft[kind];
          const known = new Set(candidates[kind].map((c) => c.id));
          const unknown = rule.ids.filter((id) => !known.has(id)).length;
          return (
            <div key={kind} data-testid={`bot-wake-from-${kind}`}>
              <SettingsRow
                label={t(($) => $.bots.wake[`sender_${kind}`])}
                description={t(($) => $.bots.wake[`sender_${kind}_hint`])}
                size="select"
              >
                <Select
                  items={modeItems}
                  value={rule.mode}
                  onValueChange={(v) => v && setMode(kind, v as BotWakeFromRule["mode"])}
                >
                  <SelectTrigger
                    className={cn("w-full", TAP)}
                    aria-label={t(($) => $.bots.wake[`sender_${kind}`])}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {modeItems.map((item) => (
                      <SelectItem key={item.value} value={item.value}>
                        {item.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </SettingsRow>
              {rule.mode === "list" ? (
                <div className="px-4 pb-3">
                  <div className="max-h-60 overflow-y-auto rounded-md border border-surface-border">
                    {candidates[kind].length === 0 ? (
                      <div className="px-3 py-3 text-caption text-muted-foreground">
                        {t(($) => $.bots.wake.list_none_available)}
                      </div>
                    ) : (
                      candidates[kind].map((c) => {
                        const id = `bot-wake-from-${kind}-${c.id}`;
                        return (
                          <label
                            key={c.id}
                            htmlFor={id}
                            className="flex min-h-11 cursor-pointer items-center gap-3 px-3 py-2 hover:bg-surface-hover sm:min-h-9"
                          >
                            <Checkbox
                              id={id}
                              checked={rule.ids.includes(c.id)}
                              onCheckedChange={(v) => toggleId(kind, c.id, v === true)}
                            />
                            <span className="min-w-0 truncate text-body">{c.name}</span>
                          </label>
                        );
                      })
                    )}
                  </div>
                  <div className="mt-1.5 space-y-0.5 text-caption leading-5 text-muted-foreground">
                    {rule.ids.length === 0 ? <div>{t(($) => $.bots.wake.list_empty)}</div> : null}
                    {unknown > 0 ? (
                      <div>{t(($) => $.bots.wake.list_unknown, { count: unknown })}</div>
                    ) : null}
                    {rule.ids.length > 100 ? (
                      <div className="text-destructive">{t(($) => $.bots.wake.list_over_limit)}</div>
                    ) : null}
                  </div>
                </div>
              ) : null}
            </div>
          );
        })}
        <SettingsRow
          label={t(($) => $.bots.wake.inherit_label)}
          description={t(($) => $.bots.wake.inherit_hint)}
        >
          <label className="-mx-2 flex min-h-11 cursor-pointer items-center px-2 sm:min-h-0">
            <Switch
              data-testid="bot-wake-inherit"
              aria-label={t(($) => $.bots.wake.inherit_label)}
              checked={inherit}
              onCheckedChange={setInherit}
            />
          </label>
        </SettingsRow>
        <div className="flex flex-wrap items-center justify-end gap-2 border-t border-surface-border px-4 py-3">
          {dirty ? (
            <Button
              type="button"
              variant="ghost"
              className={TAP}
              onClick={() => {
                setDraft(saved);
                setInherit(savedInherit);
              }}
              disabled={update.isPending}
            >
              {t(($) => $.bots.wake.reset)}
            </Button>
          ) : null}
          <Button
            type="button"
            className={TAP}
            onClick={save}
            disabled={!dirty || tooMany || update.isPending}
          >
            {update.isPending ? t(($) => $.bots.wake.saving) : t(($) => $.bots.wake.save_from)}
          </Button>
        </div>
      </SettingsCard>
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
                className={cn("w-20", TAP)}
              />
              <span className="text-caption text-muted-foreground">
                {t(($) => $.bots.wake.per_hour)}
              </span>
            </div>
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.bots.wake.effective_hourly_limit, { count: saved.hourly_limit })}
            description={settings.limited_count ? t(($) => $.bots.wake.limited_count, { count: settings.limited_count }) : undefined}
            size="none"
          >
            {null}
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.bots.wake.quiet)}
            description={t(($) => $.bots.wake.quiet_hint)}
          >
            <label className="-mx-2 flex min-h-11 cursor-pointer items-center px-2 sm:min-h-0">
              <Switch
                aria-label={t(($) => $.bots.wake.quiet)}
                checked={draft.quiet_hours.enabled}
                onCheckedChange={(v) => setQuiet({ enabled: v })}
              />
            </label>
          </SettingsRow>
          {draft.quiet_hours.enabled ? (
            <SettingsRow label={t(($) => $.bots.wake.quiet_window)} size="text">
              <div className="flex flex-wrap items-center gap-2">
                <TimeInput
                  value={draft.quiet_hours.start}
                  onChange={(v) => setQuiet({ start: v })}
                  className={TAP}
                  hourLabel={t(($) => $.bots.wake.quiet_start)}
                />
                <span className="text-caption text-muted-foreground">–</span>
                <TimeInput
                  value={draft.quiet_hours.end}
                  onChange={(v) => setQuiet({ end: v })}
                  className={TAP}
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
                className={TAP}
                aria-label={t(($) => $.bots.wake.timezone)}
                value={draft.quiet_hours.timezone}
                onChange={(e) => setQuiet({ timezone: e.target.value.trim() })}
              />
            </SettingsRow>
          ) : null}
          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-surface-border px-4 py-3">
            {dirty ? (
              <Button type="button" variant="ghost" className={TAP} onClick={reset} disabled={update.isPending}>
                {t(($) => $.bots.wake.reset)}
              </Button>
            ) : null}
            <Button
              type="button"
              className={TAP}
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
  const pending = view === "pending";
  const log = useInfiniteQuery(botWakeEventsOptions(wsId, bot.id, pending));
  const events = log.data?.pages.flatMap((p) => p.events) ?? [];
  const ack = useAckBotWakePending(wsId, bot.id);
  const [selected, setSelected] = useState<string[]>([]);
  // Only ids still on screen count; a rescan may have moved some away.
  const picked = selected.filter((id) => events.some((e) => e.event_id === id));
  const tooMany = picked.length > BOT_WAKE_ACK_MAX;

  const toggle = (id: string, on: boolean) =>
    setSelected((s) => (on ? [...s, id] : s.filter((x) => x !== id)));
  const loadedIds = [...new Set(events.map((e) => e.event_id))].slice(0, BOT_WAKE_ACK_MAX);
  const allPicked = loadedIds.length > 0 && loadedIds.every((id) => picked.includes(id));

  // Rows leave only after the server accepted the batch; on failure they and
  // the selection stay put so nothing disappears unseen.
  const acknowledge = () => {
    if (picked.length === 0 || tooMany || ack.isPending) return;
    ack.mutate(picked, {
      onSuccess: (res) => {
        setSelected([]);
        toast.success(t(($) => $.bots.wake.toast_acked, { count: res.acknowledged }));
      },
      onError: (e) =>
        toast.error(e instanceof Error && e.message ? e.message : t(($) => $.bots.toast_failed)),
    });
  };

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
        {pending ? (
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-surface-border px-4 py-2">
            <label className="flex min-h-11 cursor-pointer items-center gap-3 sm:min-h-8">
              <Checkbox
                checked={allPicked}
                onCheckedChange={(v) => setSelected(v === true ? loadedIds : [])}
              />
              <span className="text-caption text-muted-foreground">
                {t(($) => $.bots.wake.ack_select_all)}
              </span>
            </label>
            <Button
              type="button"
              size="sm"
              variant="outline"
              data-testid="bot-wake-ack"
              className={TAP}
              disabled={picked.length === 0 || tooMany || ack.isPending}
              onClick={acknowledge}
            >
              {ack.isPending
                ? t(($) => $.bots.wake.ack_acking)
                : t(($) => $.bots.wake.ack_button, { count: picked.length })}
            </Button>
            {tooMany ? (
              <div className="w-full text-caption text-destructive">
                {t(($) => $.bots.wake.ack_over_limit)}
              </div>
            ) : null}
          </div>
        ) : null}
        {events.map((event) => (
          <EventRow
            key={`${event.event_id}-${event.sequence}`}
            event={event}
            selected={pending ? picked.includes(event.event_id) : undefined}
            onSelect={pending ? (on) => toggle(event.event_id, on) : undefined}
          />
        ))}
        {log.hasNextPage ? (
          <div className="flex justify-center px-4 py-3">
            <Button
              size="sm"
              variant="ghost"
              className={TAP}
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
      <div className="flex items-center justify-between gap-2">
        <SettingsViewTabs
          label={t(($) => $.bots.wake.log_title)}
          items={[
            { value: "recent", label: t(($) => $.bots.wake.log_recent) },
            { value: "pending", label: t(($) => $.bots.wake.log_pending) },
          ]}
          value={view}
          onChange={(v) => {
            setView(v);
            setSelected([]);
          }}
        />
        {/* Refetch walks again from the first page; nothing remembers how far
            it read, so an older event that failed late is found again. */}
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className={TAP}
          disabled={log.isFetching}
          onClick={() => void log.refetch()}
        >
          {t(($) => $.bots.wake.refresh)}
        </Button>
      </div>
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

function useDuration() {
  const { t } = useT("settings");
  return (ms: number) => {
    const s = Math.max(0, Math.round(ms / 1000));
    if (s < 60) return t(($) => $.bots.wake.duration_seconds, { count: s });
    if (s < 3600) return t(($) => $.bots.wake.duration_minutes, { count: Math.round(s / 60) });
    return t(($) => $.bots.wake.duration_hours, { count: Math.round(s / 360) / 10 });
  };
}

function EventRow({
  event,
  selected,
  onSelect,
}: {
  event: BotWakeEvent;
  /** Set only in the pending list, where rows can be acknowledged. */
  selected?: boolean;
  onSelect?: (on: boolean) => void;
}) {
  const { t } = useT("settings");
  const locale = useLocale();
  const paths = useWorkspacePaths();
  const duration = useDuration();
  const fmt = (iso: string) =>
    new Date(iso).toLocaleString(locale, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  const when = fmt(event.created_at);
  const sourceHref = event.issue_id
    ? paths.issueDetail(event.issue_id)
    : event.chat_session_id
      ? paths.chatSession(event.chat_session_id)
      : event.runtime_outage ? paths.runtimes() : null;
  // Each receipt stands on its own: "started" comes only from the bot's woken
  // receipt, never from delivery or a result, and a result may arrive first.
  const delivered = event.status === "received" || !!event.received_at;
  const showReceipts = delivered || !!event.woken_at || !!event.result_at;
  const outOfOrder =
    !!event.result_at &&
    (!event.woken_at || new Date(event.result_at) < new Date(event.woken_at));
  const source = sourceHref ? (
    <AppLink href={sourceHref} className="inline-flex min-h-11 items-center underline-offset-2 hover:text-foreground hover:underline sm:min-h-0">
      {event.runtime_outage?.machine_name ?? (event.issue_id ? t(($) => $.bots.wake.source_issue) : t(($) => $.bots.wake.source_chat))}
    </AppLink>
  ) : null;

  return (
    <div data-testid="bot-wake-event" className="flex items-start gap-3 px-4 py-3">
      {onSelect ? (
        <label className="-my-2 -ml-2 flex min-h-11 min-w-11 cursor-pointer items-center justify-center sm:min-h-8 sm:min-w-8">
          <Checkbox
            checked={selected}
            onCheckedChange={(v) => onSelect(v === true)}
            aria-label={t(($) => $.bots.wake.ack_select)}
          />
        </label>
      ) : null}
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
        {showReceipts ? (
          <div
            data-testid="bot-wake-receipts"
            className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-caption leading-5 text-muted-foreground"
          >
            {event.received_at ? (
              <span>{t(($) => $.bots.wake.receipt_received, { time: fmt(event.received_at) })}</span>
            ) : null}
            {event.received_at ? <span aria-hidden="true">·</span> : null}
            <span className={event.woken_at ? "text-foreground" : undefined}>
              {event.woken_at
                ? t(($) => $.bots.wake.receipt_woken, { time: fmt(event.woken_at) })
                : t(($) => $.bots.wake.receipt_woken_missing)}
            </span>
            <span aria-hidden="true">·</span>
            {event.result_at && sourceHref ? (
              <AppLink
                href={sourceHref}
                className="inline-flex min-h-11 items-center text-foreground underline-offset-2 hover:underline sm:min-h-0"
              >
                {t(($) => $.bots.wake.receipt_result, { time: fmt(event.result_at) })}
              </AppLink>
            ) : (
              <span className={event.result_at ? "text-foreground" : undefined}>
                {event.result_at
                  ? t(($) => $.bots.wake.receipt_result, { time: fmt(event.result_at) })
                  : t(($) => $.bots.wake.receipt_result_missing)}
              </span>
            )}
            {event.woken_at ? (
              <>
                <span aria-hidden="true">·</span>
                <span>
                  {t(($) => $.bots.wake.receipt_latency, {
                    duration: duration(
                      new Date(event.woken_at).getTime() - new Date(event.created_at).getTime(),
                    ),
                  })}
                </span>
              </>
            ) : null}
            {outOfOrder ? (
              <>
                <span aria-hidden="true">·</span>
                <span className="text-warning">{t(($) => $.bots.wake.receipt_out_of_order)}</span>
              </>
            ) : null}
          </div>
        ) : null}
        {event.acknowledged_at ? (
          <div className="mt-0.5 text-caption leading-5 text-muted-foreground">
            {t(($) => $.bots.wake.acknowledged, { time: fmt(event.acknowledged_at) })}
          </div>
        ) : null}
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
