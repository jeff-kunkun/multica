"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { ExternalBot } from "@multica/core/types";
import {
  mcpConnectionsOptions,
  useCreateMcpConnection,
  useRevokeMcpConnection,
  type McpConnection,
  type McpConnectionGrant,
} from "@multica/core/bots";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { copyText } from "@multica/ui/lib/clipboard";
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useLocale, useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection } from "./settings-layout";

export function McpConnectionsSection({
  wsId,
  canManage,
  bots,
}: {
  wsId: string;
  canManage: boolean;
  bots: ExternalBot[];
}) {
  const { t } = useT("settings");
  const locale = useLocale();
  const list = useQuery(mcpConnectionsOptions(wsId, canManage));
  const create = useCreateMcpConnection(wsId);
  const revoke = useRevokeMcpConnection(wsId);
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [botId, setBotId] = useState("");
  const [pending, setPending] = useState(false);
  const [grant, setGrant] = useState<McpConnectionGrant | null>(null);
  const [confirm, setConfirm] = useState<McpConnection | null>(null);
  const active = bots.filter((bot) => bot.status === "active");
  const items = active.map((bot) => ({ value: bot.id, label: bot.name }));
  const generation = useRef(0);
  useEffect(
    () => () => {
      generation.current++;
    },
    [],
  );
  useEffect(() => {
    if (!grant) return;
    const delay = new Date(grant.expiresAt).getTime() - Date.now();
    const timeout = setTimeout(() => setGrant(null), Math.max(0, delay));
    return () => clearTimeout(timeout);
  }, [grant]);
  const fail = () => toast.error(t(($) => $.bots.mcp.failed));
  const closeAdd = () => {
    generation.current++;
    setAdding(false);
    setName("");
    setBotId("");
    setPending(false);
  };
  const submit = async () => {
    if (!name.trim() || !botId || pending) return;
    const current = generation.current;
    setPending(true);
    try {
      const result = await create({ name: name.trim(), botId });
      if (current !== generation.current) return;
      closeAdd();
      setGrant(result);
    } catch {
      if (current === generation.current) fail();
    } finally {
      if (current === generation.current) setPending(false);
    }
  };
  const copy = async () => {
    if (!grant) return;
    const text = `${t(($) => $.bots.mcp.endpoint)}: ${grant.endpoint}\n${t(($) => $.bots.mcp.exchange)}: ${grant.exchangeUrl}\n${t(($) => $.bots.mcp.code)}: ${grant.code}`;
    if (await copyText(text)) toast.success(t(($) => $.bots.mcp.copied));
    else fail();
  };
  const disabledReason = !canManage
    ? t(($) => $.bots.mcp.permission)
    : active.length === 0
      ? t(($) => $.bots.mcp.no_bots)
      : undefined;
  return (
    <SettingsSection
      title={t(($) => $.bots.mcp.title)}
      description={disabledReason ?? t(($) => $.bots.mcp.description)}
      action={
        <Button
          size="sm"
          className="h-11 sm:h-8"
          disabled={Boolean(disabledReason)}
          onClick={() => setAdding(true)}
        >
          {t(($) => $.bots.mcp.add)}
        </Button>
      }
    >
      <SettingsCard>
        {list.isLoading ? (
          <SettingsRow label={t(($) => $.bots.mcp.loading)}>{null}</SettingsRow>
        ) : list.isError ? (
          <SettingsRow label={t(($) => $.bots.mcp.load_failed)}>
            <Button
              variant="outline"
              className="h-11 sm:h-8"
              onClick={() => void list.refetch()}
            >
              {t(($) => $.bots.mcp.retry)}
            </Button>
          </SettingsRow>
        ) : (list.data ?? []).length === 0 ? (
          <SettingsRow label={t(($) => $.bots.mcp.empty)}>{null}</SettingsRow>
        ) : (
          list.data?.map((connection) => (
            <SettingsRow
              key={connection.id}
              label={connection.name}
              description={
                connection.revoked
                  ? t(($) => $.bots.mcp.revoked)
                  : t(($) => $.bots.mcp.expires, {
                      date: new Date(connection.expiresAt).toLocaleDateString(
                        locale,
                      ),
                    })
              }
            >
              <Button
                variant="outline"
                size="sm"
                className="h-11 sm:h-8"
                disabled={connection.revoked || !canManage}
                onClick={() => setConfirm(connection)}
              >
                {t(($) => $.bots.mcp.revoke)}
              </Button>
            </SettingsRow>
          ))
        )}
      </SettingsCard>
      <Dialog
        open={adding}
        onOpenChange={(open) => {
          if (!open) closeAdd();
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t(($) => $.bots.mcp.add)}</DialogTitle>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              void submit();
            }}
          >
            <div className="space-y-1.5">
              <Label htmlFor="mcp-name">{t(($) => $.bots.mcp.name)}</Label>
              <Input
                id="mcp-name"
                autoFocus
                autoComplete="off"
                maxLength={60}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="mcp-bot">{t(($) => $.bots.mcp.bot)}</Label>
              <Select
                items={items}
                value={botId}
                onValueChange={(value) => setBotId(value ?? "")}
              >
                <SelectTrigger id="mcp-bot" className="w-full min-h-11">
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
            </div>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                className="min-h-11"
                onClick={closeAdd}
              >
                {t(($) => $.bots.mcp.cancel)}
              </Button>
              <Button
                type="submit"
                className="min-h-11"
                disabled={!name.trim() || !botId || pending}
              >
                {pending
                  ? t(($) => $.bots.mcp.creating)
                  : t(($) => $.bots.mcp.create)}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={Boolean(grant)}
        onOpenChange={(open) => {
          if (!open) setGrant(null);
        }}
      >
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t(($) => $.bots.mcp.grant_title)}</DialogTitle>
            <DialogDescription>
              {t(($) => $.bots.mcp.grant_description)}
            </DialogDescription>
          </DialogHeader>
          <dl className="min-w-0 space-y-3 text-body">
            {[
              [t(($) => $.bots.mcp.endpoint), grant?.endpoint],
              [t(($) => $.bots.mcp.exchange), grant?.exchangeUrl],
              [t(($) => $.bots.mcp.code), grant?.code],
            ].map(([label, value]) => (
              <div key={label}>
                <dt className="text-muted-foreground">{label}</dt>
                <dd className="break-all font-mono select-all">{value}</dd>
              </div>
            ))}
          </dl>
          <DialogFooter>
            <Button
              variant="outline"
              className="min-h-11"
              onClick={() => void copy()}
            >
              {t(($) => $.bots.mcp.copy)}
            </Button>
            <Button className="min-h-11" onClick={() => setGrant(null)}>
              {t(($) => $.bots.mcp.close)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <AlertDialog
        open={Boolean(confirm)}
        onOpenChange={(open) => {
          if (!open && !revoke.isPending) setConfirm(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.bots.mcp.revoke_title, { name: confirm?.name ?? "" })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.bots.mcp.revoke_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={revoke.isPending} className="min-h-11">
              {t(($) => $.bots.mcp.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              className="min-h-11"
              disabled={revoke.isPending}
              onClick={(e) => {
                e.preventDefault();
                if (confirm)
                  revoke.mutate(confirm.id, {
                    onSuccess: () => setConfirm(null),
                    onError: fail,
                  });
              }}
            >
              {t(($) => $.bots.mcp.revoke)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  );
}
