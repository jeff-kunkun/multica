"use client";

import { useState } from "react";
import { Check, Copy, KeyRound, MoreHorizontal, Pencil, Plus, Trash2, Ban } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import type { WorkspaceBot, WorkspaceBotWithSecrets } from "@multica/core/types";
import { workspaceBotsOptions } from "@multica/core/workspace/queries";
import {
  useCreateBot,
  useDeleteBot,
  useRevokeBotToken,
  useRotateBotToken,
  useUpdateBot,
} from "@multica/core/workspace/mutations";
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
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import { copyText } from "@multica/ui/lib/clipboard";
import { useLocale, useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection, SettingsTab } from "./settings-layout";

/** The remote MCP endpoint a Bot connects to. Web proxies /api, so fall back to the origin. */
function mcpEndpoint(): string {
  const base = api.getBaseUrl() || (typeof window !== "undefined" ? window.location.origin : "");
  return `${base.replace(/\/$/, "")}/api/mcp`;
}

/**
 * Project-manager Bots (Grok and the like). Each Bot is a workspace member
 * with one token; it drives the office through the remote MCP endpoint and
 * receives events on its webhook. Only the owner manages them.
 */
export function BotsTab() {
  const { t } = useT("settings");
  const locale = useLocale();
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const member = useCurrentMember(wsId);
  const isOwner = member.role === "owner";

  const botsQuery = useQuery(workspaceBotsOptions(wsId, isOwner));
  const createBot = useCreateBot(wsId);
  const updateBot = useUpdateBot(wsId);
  const rotateToken = useRotateBotToken(wsId);
  const revokeToken = useRevokeBotToken(wsId);
  const deleteBot = useDeleteBot(wsId);

  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<WorkspaceBot | null>(null);
  const [secrets, setSecrets] = useState<WorkspaceBotWithSecrets | null>(null);
  const [confirm, setConfirm] = useState<{ bot: WorkspaceBot; action: "revoke" | "delete" } | null>(null);

  const fail = (e: unknown) => toast.error(e instanceof Error ? e.message : t(($) => $.bots.toast_failed));
  const bots = botsQuery.data ?? [];
  const endpoint = mcpEndpoint();

  return (
    <SettingsTab
      title={t(($) => $.bots.title)}
      description={t(($) => $.bots.description)}
      scope="workspace"
      actions={
        isOwner ? (
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <Plus />
            {t(($) => $.bots.create)}
          </Button>
        ) : null
      }
    >
      {!isOwner ? (
        <p className="text-body text-muted-foreground">{t(($) => $.bots.owner_only)}</p>
      ) : (
        <>
          <SettingsCard>
            <SettingsRow
              label={t(($) => $.bots.endpoint_label)}
              description={t(($) => $.bots.endpoint_description)}
              size="text"
            >
              <CopyField value={endpoint} label={t(($) => $.bots.copy)} />
            </SettingsRow>
          </SettingsCard>

          <SettingsSection title={t(($) => $.bots.list_title)}>
            <SettingsCard>
              {botsQuery.isLoading ? (
                <div className="px-4 py-3.5 text-caption text-muted-foreground">{t(($) => $.bots.loading)}</div>
              ) : bots.length === 0 ? (
                <div className="px-4 py-3.5 text-caption text-muted-foreground">
                  {botsQuery.isError ? t(($) => $.bots.load_failed) : t(($) => $.bots.empty)}
                </div>
              ) : (
                bots.map((bot) => (
                  <SettingsRow
                    key={bot.id}
                    label={<span className="block truncate">{bot.name}</span>}
                    description={
                      <>
                        <span className="block truncate">
                          {bot.token_prefix
                            ? t(($) => $.bots.token_meta, {
                                prefix: bot.token_prefix,
                                lastUsed: bot.token_last_used_at
                                  ? new Date(bot.token_last_used_at).toLocaleDateString(locale)
                                  : t(($) => $.bots.never_used),
                              })
                            : t(($) => $.bots.token_revoked)}
                        </span>
                        {bot.webhook_url ? (
                          <span
                            className={
                              bot.webhook_last_error ? "block truncate text-destructive" : "block truncate"
                            }
                          >
                            {bot.webhook_last_error
                              ? t(($) => $.bots.webhook_failing, { error: bot.webhook_last_error })
                              : t(($) => $.bots.webhook_meta, { url: bot.webhook_url })}
                          </span>
                        ) : null}
                      </>
                    }
                  >
                    <div className="flex items-center gap-3">
                      <label className="flex items-center gap-2 text-caption text-muted-foreground">
                        {t(($) => $.bots.can_release_short)}
                        <Switch
                          checked={bot.can_release}
                          aria-label={t(($) => $.bots.can_release_aria, { name: bot.name })}
                          disabled={updateBot.isPending}
                          onCheckedChange={(checked) =>
                            updateBot.mutate({ botId: bot.id, can_release: checked }, { onError: fail })
                          }
                        />
                      </label>
                      <DropdownMenu>
                        <DropdownMenuTrigger
                          render={
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              aria-label={t(($) => $.bots.actions_aria, { name: bot.name })}
                            />
                          }
                        >
                          <MoreHorizontal />
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end" className="w-auto">
                          <DropdownMenuItem onClick={() => setEditing(bot)}>
                            <Pencil />
                            {t(($) => $.bots.edit)}
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            onClick={() =>
                              rotateToken.mutate(bot.id, { onSuccess: setSecrets, onError: fail })
                            }
                          >
                            <KeyRound />
                            {t(($) => $.bots.rotate)}
                          </DropdownMenuItem>
                          {bot.token_prefix ? (
                            <DropdownMenuItem onClick={() => setConfirm({ bot, action: "revoke" })}>
                              <Ban />
                              {t(($) => $.bots.revoke)}
                            </DropdownMenuItem>
                          ) : null}
                          <DropdownMenuItem
                            variant="destructive"
                            onClick={() => setConfirm({ bot, action: "delete" })}
                          >
                            <Trash2 />
                            {t(($) => $.bots.delete)}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                  </SettingsRow>
                ))
              )}
            </SettingsCard>
          </SettingsSection>
        </>
      )}

      <BotFormDialog
        open={createOpen}
        title={t(($) => $.bots.create_title)}
        submitLabel={t(($) => $.bots.create)}
        showRelease
        pending={createBot.isPending}
        onClose={() => setCreateOpen(false)}
        onSubmit={(values) =>
          createBot.mutate(values, {
            onSuccess: (bot) => {
              setCreateOpen(false);
              setSecrets(bot);
            },
            onError: fail,
          })
        }
      />

      <BotFormDialog
        key={editing?.id ?? "none"}
        open={!!editing}
        title={t(($) => $.bots.edit_title)}
        submitLabel={t(($) => $.bots.save)}
        initial={editing ?? undefined}
        pending={updateBot.isPending}
        onClose={() => setEditing(null)}
        onSubmit={(values) =>
          editing &&
          updateBot.mutate(
            { botId: editing.id, name: values.name, webhook_url: values.webhook_url },
            {
              onSuccess: () => setEditing(null),
              onError: fail,
            },
          )
        }
      />

      <SecretsDialog bot={secrets} endpoint={endpoint} onClose={() => setSecrets(null)} />

      <AlertDialog open={!!confirm} onOpenChange={(open) => { if (!open) setConfirm(null); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {confirm?.action === "delete"
                ? t(($) => $.bots.delete_title, { name: confirm.bot.name })
                : t(($) => $.bots.revoke_title, { name: confirm?.bot.name ?? "" })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirm?.action === "delete"
                ? t(($) => $.bots.delete_description)
                : t(($) => $.bots.revoke_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.bots.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (!confirm) return;
                const mutation = confirm.action === "delete" ? deleteBot : revokeToken;
                mutation.mutate(confirm.bot.id, { onError: fail });
                setConfirm(null);
              }}
            >
              {confirm?.action === "delete" ? t(($) => $.bots.delete) : t(($) => $.bots.revoke)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

function BotFormDialog({
  open,
  title,
  submitLabel,
  initial,
  showRelease,
  pending,
  onClose,
  onSubmit,
}: {
  open: boolean;
  title: string;
  submitLabel: string;
  initial?: WorkspaceBot;
  showRelease?: boolean;
  pending: boolean;
  onClose: () => void;
  onSubmit: (values: { name: string; can_release: boolean; webhook_url: string }) => void;
}) {
  const { t } = useT("settings");
  const [name, setName] = useState(initial?.name ?? "");
  const [canRelease, setCanRelease] = useState(initial?.can_release ?? false);
  const [webhookUrl, setWebhookUrl] = useState(initial?.webhook_url ?? "");

  const close = () => {
    if (!initial) {
      setName("");
      setCanRelease(false);
      setWebhookUrl("");
    }
    onClose();
  };

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) close(); }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            onSubmit({ name: name.trim(), can_release: canRelease, webhook_url: webhookUrl.trim() });
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor="bot-name">{t(($) => $.bots.name_label)}</Label>
            <Input
              id="bot-name"
              autoComplete="off"
              value={name}
              // eslint-disable-next-line no-restricted-syntax -- product name example, not translated
              placeholder="Grok"
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="bot-webhook">{t(($) => $.bots.webhook_label)}</Label>
            <Input
              id="bot-webhook"
              type="url"
              autoComplete="off"
              value={webhookUrl}
              placeholder="https://"
              onChange={(e) => setWebhookUrl(e.target.value)}
            />
            <p className="text-caption text-muted-foreground">{t(($) => $.bots.webhook_hint)}</p>
          </div>
          {showRelease ? (
            <label className="flex items-start justify-between gap-4">
              <span className="min-w-0">
                <span className="block text-body font-medium">{t(($) => $.bots.can_release_label)}</span>
                <span className="block text-caption text-muted-foreground">
                  {t(($) => $.bots.can_release_hint)}
                </span>
              </span>
              <Switch checked={canRelease} onCheckedChange={setCanRelease} />
            </label>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={close}>
              {t(($) => $.bots.cancel)}
            </Button>
            <Button type="submit" disabled={pending || !name.trim()}>
              {submitLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function SecretsDialog({
  bot,
  endpoint,
  onClose,
}: {
  bot: WorkspaceBotWithSecrets | null;
  endpoint: string;
  onClose: () => void;
}) {
  const { t } = useT("settings");
  const [stored, setStored] = useState(false);
  const close = () => {
    setStored(false);
    onClose();
  };
  return (
    <Dialog open={!!bot} onOpenChange={(v) => { if (!v && stored) close(); }}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.bots.secrets_title, { name: bot?.name ?? "" })}</DialogTitle>
        </DialogHeader>
        <p className="text-body text-muted-foreground">{t(($) => $.bots.secrets_warning)}</p>
        {bot?.setup_prompt ? <SetupPromptRow prompt={bot.setup_prompt} /> : null}
        <div className="min-w-0 space-y-3">
          <SecretLine label={t(($) => $.bots.endpoint_label)} value={endpoint} />
          {bot?.token ? <SecretLine label={t(($) => $.bots.token_label)} value={bot.token} /> : null}
          {bot?.webhook_secret ? (
            <SecretLine label={t(($) => $.bots.webhook_secret_label)} value={bot.webhook_secret} />
          ) : null}
        </div>
        <DialogFooter className="items-center sm:justify-between">
          <label className="flex items-center gap-2 text-body">
            <Checkbox checked={stored} onCheckedChange={(v) => setStored(v === true)} />
            {t(($) => $.bots.confirm_stored)}
          </label>
          <Button disabled={!stored} onClick={close}>
            {t(($) => $.bots.done)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** The server-rendered prompt embeds the token, so it is only offered here, next to it. */
function SetupPromptRow({ prompt }: { prompt: string }) {
  const { t } = useT("settings");
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex min-w-0 flex-col gap-2 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
      <div className="min-w-0">
        <div className="text-body font-medium">{t(($) => $.bots.setup_prompt_label)}</div>
        <div className="text-caption text-muted-foreground">{t(($) => $.bots.setup_prompt_hint)}</div>
      </div>
      <Button
        variant="outline"
        className="shrink-0"
        onClick={async () => {
          if (await copyText(prompt)) {
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
          }
        }}
      >
        {copied ? <Check /> : <Copy />}
        {copied ? t(($) => $.bots.copied) : t(($) => $.bots.copy_prompt)}
      </Button>
    </div>
  );
}

function SecretLine({ label, value }: { label: string; value: string }) {
  const { t } = useT("settings");
  return (
    <div className="min-w-0 space-y-1">
      <div className="text-caption text-muted-foreground">{label}</div>
      <CopyField value={value} label={t(($) => $.bots.copy)} />
    </div>
  );
}

function CopyField({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex min-w-0 items-center gap-2">
      <code className="min-w-0 flex-1 truncate rounded-md border bg-muted/50 px-3 py-1.5 text-caption select-all">
        {value}
      </code>
      <Button
        variant="outline"
        size="icon-sm"
        aria-label={label}
        onClick={async () => {
          if (await copyText(value)) {
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
          }
        }}
      >
        {copied ? <Check /> : <Copy />}
      </Button>
    </div>
  );
}
