"use client";

import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Copy, MoreHorizontal } from "lucide-react";
import { toast } from "sonner";
import type { ExternalBot } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useConfigStore } from "@multica/core/config";
import { useCurrentMember } from "@multica/core/permissions";
import {
  externalBotListOptions,
  useDeleteExternalBot,
  useStopExternalBot,
} from "@multica/core/bots";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
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
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { useLocale, useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection, SettingsTab } from "./settings-layout";

type Confirm = { kind: "stop" | "delete"; bot: ExternalBot };

// External bots (DENE-1788): a Bot (Grok, ...) works with the power of the
// admin who let it in. Since DENE-2009 the admin only copies one sentence to
// the Bot; the Bot asks to connect, the admin agrees once, and the key goes
// straight to the Bot — no key is ever shown here. The Bot brings its own
// address with the request, so messages and decisions reach it without any
// further setting: a listed bot can only be stopped or removed.
// The nav entry is admin-only; the server enforces the same gate.
// `lead` renders above the list (settings puts "what goes to my Bot" there).
export function BotsTab({ lead }: { lead?: ReactNode } = {}) {
  const { t } = useT("settings");
  const locale = useLocale();
  const wsId = useWorkspaceId();
  const { role } = useCurrentMember(wsId);
  const canManage = role === "owner" || role === "admin";
  const { data: bots = [], isLoading, isError } = useQuery(externalBotListOptions(wsId, canManage));
  const stop = useStopExternalBot(wsId);
  const remove = useDeleteExternalBot(wsId);

  const [confirm, setConfirm] = useState<Confirm | null>(null);

  const fail = (e: unknown) =>
    toast.error(e instanceof Error && e.message ? e.message : t(($) => $.bots.toast_failed));

  const serverUrl =
    useConfigStore((s) => s.daemonServerUrl) || (typeof window !== "undefined" ? window.location.origin : "");
  const connectText = t(($) => $.bots.connect.instruction, {
    command: `multica bot connect --server-url ${serverUrl} --name Grok --webhook-url <URL> --webhook-key <KEY> --expires-in-days 0`,
  });
  const copyConnect = async () => {
    if (await copyText(connectText)) {
      toast.success(t(($) => $.bots.connect.copied));
    } else {
      toast.error(t(($) => $.bots.toast_failed));
    }
  };

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

  const runConfirm = async () => {
    if (!confirm) return;
    const { kind, bot } = confirm;
    try {
      if (kind === "stop") {
        await stop.mutateAsync(bot.id);
        toast.success(t(($) => $.bots.toast_stopped, { name: bot.name }));
      } else {
        await remove.mutateAsync(bot.id);
      }
      setConfirm(null);
    } catch (e) {
      fail(e);
    }
  };

  const busy = stop.isPending || remove.isPending;

  const confirmText = (c: Confirm) => {
    const name = c.bot.name;
    switch (c.kind) {
      case "stop":
        return {
          title: t(($) => $.bots.stop_dialog.title, { name }),
          description: t(($) => $.bots.stop_dialog.description),
          confirm: t(($) => $.bots.stop),
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

  return (
    <SettingsTab
      title={t(($) => $.bots.title)}
      description={t(($) => $.bots.description)}
      scope="workspace"
    >
      {lead}
      <SettingsSection>
        <SettingsCard>
          <SettingsRow label={t(($) => $.bots.connect.title)} description={t(($) => $.bots.connect.description)}>
            <Button size="sm" className="min-h-11 sm:min-h-8" onClick={() => void copyConnect()}>
              <Copy className="size-4" />
              {t(($) => $.bots.connect.copy)}
            </Button>
          </SettingsRow>
          <pre
            data-testid="bot-connect-text"
            className="mx-4 mb-3.5 min-w-0 overflow-auto rounded-md border bg-muted/50 px-3 py-2 font-mono text-caption leading-5 whitespace-pre-wrap break-all select-all"
          >
            {connectText}
          </pre>
        </SettingsCard>
      </SettingsSection>
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
              {null}
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
              </SettingsRow>
            ))}
          </SettingsCard>
        )}
      </SettingsSection>

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
                  variant="destructive"
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
