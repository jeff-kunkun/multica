"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, ChevronRight, Copy, MoreHorizontal } from "lucide-react";
import { toast } from "sonner";
import type { ExternalBot, ExternalBotKeyResponse } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentMember } from "@multica/core/permissions";
import {
  externalBotListOptions,
  useCreateExternalBot,
  useDeleteExternalBot,
  useRotateExternalBotKey,
  useStopExternalBot,
} from "@multica/core/bots";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { AppLink, useNavigation } from "../../navigation";
import { useLocale, useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection, SettingsTab } from "./settings-layout";
import { resolveSettingsLocation, settingsHref } from "./settings-navigation";
import { BotWakePage } from "./bot-wake-page";

const EXPIRY_KEYS = ["never", "365"] as const;
type Expiry = (typeof EXPIRY_KEYS)[number];
type Confirm = { kind: "stop" | "rotate" | "delete"; bot: ExternalBot };

// External bots (DENE-1788): a workspace admin adds a bot (Grok, ...) and
// hands it a key with the admin's own power plus the setup text to paste.
// The nav entry is admin-only; the server enforces the same gate.
export function BotsTab() {
  const { t } = useT("settings");
  const locale = useLocale();
  const wsId = useWorkspaceId();
  const { role } = useCurrentMember(wsId);
  const canManage = role === "owner" || role === "admin";
  const { data: bots = [], isLoading, isError } = useQuery(externalBotListOptions(wsId, canManage));
  const navigation = useNavigation();
  const requested = resolveSettingsLocation(navigation.searchParams).integration;
  const selected = bots.find((bot) => bot.id === requested);
  const href = (integration?: string) =>
    settingsHref(navigation.pathname, navigation.searchParams, "bots", { integration });

  const create = useCreateExternalBot(wsId);
  const rotate = useRotateExternalBotKey(wsId);
  const stop = useStopExternalBot(wsId);
  const remove = useDeleteExternalBot(wsId);

  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [expiry, setExpiry] = useState<Expiry>("never");
  const [issued, setIssued] = useState<ExternalBotKeyResponse | null>(null);
  const [copied, setCopied] = useState(false);
  const [confirm, setConfirm] = useState<Confirm | null>(null);

  const fail = (e: unknown) =>
    toast.error(e instanceof Error && e.message ? e.message : t(($) => $.bots.toast_failed));

  const expiryItems = EXPIRY_KEYS.map((value) => ({
    value,
    label: t(($) => $.bots.expiry[value]),
  }));
  const typeItems = [{ value: "grok", label: t(($) => $.bots.type.grok) }];

  const fmtDate = (iso: string) => {
    const d = new Date(iso);
    return d.toLocaleDateString(locale, {
      year: d.getFullYear() === new Date().getFullYear() ? undefined : "numeric",
      month: "short",
      day: "numeric",
    });
  };
  const fmtDateTime = (iso: string) =>
    new Date(iso).toLocaleString(locale, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });

  const closeAdd = () => {
    setAdding(false);
    setName("");
    setExpiry("never");
  };

  const submitAdd = () => {
    const trimmed = name.trim();
    if (!trimmed || create.isPending) return;
    create.mutate(
      {
        name: trimmed,
        type: "grok",
        expires_in_days: expiry === "365" ? 365 : undefined,
      },
      {
        onSuccess: (res) => {
          closeAdd();
          setIssued(res);
        },
        onError: fail,
      },
    );
  };

  const runConfirm = async () => {
    if (!confirm) return;
    const { kind, bot } = confirm;
    try {
      if (kind === "stop") {
        await stop.mutateAsync(bot.id);
        toast.success(t(($) => $.bots.toast_stopped, { name: bot.name }));
      } else if (kind === "delete") {
        await remove.mutateAsync(bot.id);
      } else {
        setIssued(await rotate.mutateAsync(bot.id));
      }
      setConfirm(null);
    } catch (e) {
      fail(e);
    }
  };

  const copySetup = async () => {
    if (!issued) return;
    if (await copyText(issued.setup_text)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  };

  const closeIssued = () => {
    setIssued(null);
    setCopied(false);
  };

  const busy = stop.isPending || rotate.isPending || remove.isPending;

  const confirmText = (c: Confirm) => {
    const name = c.bot.name;
    switch (c.kind) {
      case "stop":
        return {
          title: t(($) => $.bots.stop_dialog.title, { name }),
          description: t(($) => $.bots.stop_dialog.description),
          confirm: t(($) => $.bots.stop),
        };
      case "rotate":
        return {
          title: t(($) => $.bots.rotate_dialog.title, { name }),
          description: t(($) => $.bots.rotate_dialog.description),
          confirm: t(($) => $.bots.new_key),
        };
      case "delete":
        return {
          title: t(($) => $.bots.delete_dialog.title, { name }),
          description: t(($) => $.bots.delete_dialog.description),
          confirm: t(($) => $.bots.delete),
        };
    }
  };
  const confirmCopy = confirm ? confirmText(confirm) : null;

  // ?tab=bots&integration=<botId> opens that bot's wake settings.
  if (selected) {
    return (
      <SettingsTab
        title={selected.name}
        description={t(($) => $.bots.wake.description)}
        scope="workspace"
        breadcrumb={
          <nav
            aria-label={t(($) => $.bots.wake.breadcrumb)}
            className="flex items-center gap-1.5 text-caption text-muted-foreground"
          >
            <AppLink
              href={href()}
              className="rounded-sm hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
            >
              {t(($) => $.bots.title)}
            </AppLink>
            <ChevronRight aria-hidden="true" className="size-3" />
            <span aria-current="page" className="truncate text-foreground">
              {selected.name}
            </span>
          </nav>
        }
      >
        <BotWakePage bot={selected} />
      </SettingsTab>
    );
  }

  return (
    <SettingsTab
      title={t(($) => $.bots.title)}
      description={t(($) => $.bots.description)}
      scope="workspace"
      actions={
        bots.length > 0 ? (
          <Button size="sm" onClick={() => setAdding(true)}>
            {t(($) => $.bots.add)}
          </Button>
        ) : null
      }
    >
      <SettingsSection>
        {isLoading ? (
          <SettingsCard>
            {Array.from({ length: 2 }).map((_, i) => (
              <div key={i} className="flex min-h-16 items-center px-4 py-3.5">
                <div className="flex-1 space-y-1.5">
                  <Skeleton className="h-4 w-32" />
                  <Skeleton className="h-3 w-48" />
                </div>
              </div>
            ))}
          </SettingsCard>
        ) : bots.length === 0 ? (
          <SettingsCard>
            <SettingsRow
              label={isError ? t(($) => $.bots.load_failed) : t(($) => $.bots.empty)}
              description={isError ? undefined : t(($) => $.bots.empty_hint)}
            >
              {isError ? null : (
                <Button size="sm" onClick={() => setAdding(true)}>
                  {t(($) => $.bots.add)}
                </Button>
              )}
            </SettingsRow>
          </SettingsCard>
        ) : (
          <SettingsCard>
            {bots.map((bot) => (
              <SettingsRow
                key={bot.id}
                label={
                  <span className="flex min-w-0 items-center gap-2">
                    <span className={cn("truncate", bot.status !== "active" && "text-muted-foreground")}>
                      {bot.name}
                    </span>
                    {bot.status !== "active" ? (
                      <span
                        className={cn(
                          "shrink-0 text-caption font-normal",
                          bot.status === "stopped" ? "text-destructive" : "text-muted-foreground",
                        )}
                      >
                        {t(($) => $.bots.status[bot.status])}
                      </span>
                    ) : null}
                  </span>
                }
                description={[
                  t(($) => $.bots.meta, {
                    type: t(($) => $.bots.type[bot.type]),
                    lastUsed: bot.last_used_at
                      ? t(($) => $.bots.last_used, { date: fmtDateTime(bot.last_used_at) })
                      : t(($) => $.bots.last_used_never),
                    created: fmtDate(bot.created_at),
                  }),
                  bot.status === "active" && bot.expires_at
                    ? t(($) => $.bots.expires, { date: fmtDate(bot.expires_at) })
                    : null,
                ]
                  .filter(Boolean)
                  .join(" · ")}
              >
                <div className="flex items-center gap-1">
                <AppLink
                  href={href(bot.id)}
                  className={cn(buttonVariants({ variant: "outline", size: "sm" }), "h-11 sm:h-8")}
                >
                  {t(($) => $.bots.wake.open)}
                </AppLink>
                <DropdownMenu>
                  <DropdownMenuTrigger
                    render={
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        className="size-11 sm:size-8"
                        aria-label={t(($) => $.bots.actions_aria, { name: bot.name })}
                      >
                        <MoreHorizontal className="size-4" />
                      </Button>
                    }
                  />
                  <DropdownMenuContent align="end" className="w-auto">
                    <DropdownMenuItem onClick={() => setConfirm({ kind: "rotate", bot })}>
                      {t(($) => $.bots.new_key)}
                    </DropdownMenuItem>
                    <DropdownMenuSeparator />
                    {bot.status === "stopped" ? (
                      <DropdownMenuItem
                        variant="destructive"
                        onClick={() => setConfirm({ kind: "delete", bot })}
                      >
                        {t(($) => $.bots.delete)}
                      </DropdownMenuItem>
                    ) : (
                      <DropdownMenuItem
                        variant="destructive"
                        onClick={() => setConfirm({ kind: "stop", bot })}
                      >
                        {t(($) => $.bots.stop)}
                      </DropdownMenuItem>
                    )}
                  </DropdownMenuContent>
                </DropdownMenu>
                </div>
              </SettingsRow>
            ))}
          </SettingsCard>
        )}
      </SettingsSection>

      <Dialog open={adding} onOpenChange={(v) => { if (!v) closeAdd(); }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t(($) => $.bots.add_dialog.title)}</DialogTitle>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              submitAdd();
            }}
          >
            <div className="space-y-1.5">
              <Label htmlFor="bot-name">{t(($) => $.bots.add_dialog.name)}</Label>
              <Input
                id="bot-name"
                autoFocus
                autoComplete="off"
                maxLength={60}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t(($) => $.bots.add_dialog.name_placeholder)}
              />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label>{t(($) => $.bots.add_dialog.type)}</Label>
                <Select items={typeItems} value="grok">
                  <SelectTrigger className="w-full" aria-label={t(($) => $.bots.add_dialog.type)}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {typeItems.map((item) => (
                      <SelectItem key={item.value} value={item.value}>
                        {item.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <Label>{t(($) => $.bots.add_dialog.expiry)}</Label>
                <Select
                  items={expiryItems}
                  value={expiry}
                  onValueChange={(v) => { if (v) setExpiry(v as Expiry); }}
                >
                  <SelectTrigger className="w-full" aria-label={t(($) => $.bots.add_dialog.expiry)}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {expiryItems.map((item) => (
                      <SelectItem key={item.value} value={item.value}>
                        {item.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={closeAdd}>
                {t(($) => $.bots.cancel)}
              </Button>
              <Button type="submit" disabled={!name.trim() || create.isPending}>
                {create.isPending
                  ? t(($) => $.bots.add_dialog.submitting)
                  : t(($) => $.bots.add_dialog.submit)}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={!!issued} onOpenChange={(v) => { if (!v) closeIssued(); }}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>
              {t(($) => $.bots.key_dialog.title, { name: issued?.bot.name ?? "" })}
            </DialogTitle>
            <DialogDescription>{t(($) => $.bots.key_dialog.description)}</DialogDescription>
          </DialogHeader>
          <pre
            data-testid="bot-setup-text"
            className="max-h-[50vh] min-w-0 overflow-auto rounded-md border bg-muted/50 px-3 py-2 font-mono text-caption leading-5 whitespace-pre-wrap break-all select-all"
          >
            {issued?.setup_text}
          </pre>
          <DialogFooter>
            <Button variant="outline" onClick={copySetup}>
              {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
              {copied ? t(($) => $.bots.key_dialog.copied) : t(($) => $.bots.key_dialog.copy)}
            </Button>
            <Button onClick={closeIssued}>{t(($) => $.bots.key_dialog.done)}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog open={!!confirm} onOpenChange={(v) => { if (!v && !busy) setConfirm(null); }}>
        <AlertDialogContent>
          {confirm && confirmCopy ? (
            <>
              <AlertDialogHeader>
                <AlertDialogTitle>
                  {confirmCopy.title}
                </AlertDialogTitle>
                <AlertDialogDescription>
                  {confirmCopy.description}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel disabled={busy}>{t(($) => $.bots.cancel)}</AlertDialogCancel>
                <AlertDialogAction
                  variant={confirm.kind === "rotate" ? "default" : "destructive"}
                  disabled={busy}
                  onClick={(e) => {
                    e.preventDefault();
                    void runConfirm();
                  }}
                >
                  {confirmCopy.confirm}
                </AlertDialogAction>
              </AlertDialogFooter>
            </>
          ) : null}
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}
