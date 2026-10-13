"use client";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { ExternalBot } from "@multica/core/types";
import { projectListOptions } from "@multica/core/projects";
import {
  officeBotRoutesOptions,
  useSaveOfficeBotRoute,
  useDeleteOfficeBotRoute,
  type OfficeBotRoute,
} from "@multica/core/bots";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Dialog,
  DialogContent,
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
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection } from "./settings-layout";

const DEFAULT_ROUTE = "default";
export function OfficeBotRoutesSection({
  wsId,
  canManage,
  bots,
}: {
  wsId: string;
  canManage: boolean;
  bots: ExternalBot[];
}) {
  const { t } = useT("settings");
  const list = useQuery(officeBotRoutesOptions(wsId, canManage));
  const projects = useQuery({
    ...projectListOptions(wsId),
    enabled: canManage,
  });
  const save = useSaveOfficeBotRoute(wsId);
  const remove = useDeleteOfficeBotRoute(wsId);
  const [draft, setDraft] = useState<OfficeBotRoute | null>(null);
  const [editing, setEditing] = useState(false);
  const [confirm, setConfirm] = useState<OfficeBotRoute | null>(null);
  const active = bots.filter((bot) => bot.status === "active");
  const projectItems = [
    { value: DEFAULT_ROUTE, label: t(($) => $.bots.routes.fallback) },
    ...(projects.data ?? []).map((project) => ({
      value: project.id,
      label: project.title,
    })),
  ];
  const botItems = active.map((bot) => ({ value: bot.id, label: bot.name }));
  const disabledReason = !canManage
    ? t(($) => $.bots.mcp.permission)
    : !active.length
      ? t(($) => $.bots.routes.no_bots)
      : undefined;
  const fail = () => toast.error(t(($) => $.bots.routes.failed));
  const projectName = (id: string | null) =>
    id
      ? (projects.data?.find((project) => project.id === id)?.title ??
        t(($) => $.bots.routes.unknown_project))
      : t(($) => $.bots.routes.fallback);
  const close = () => {
    if (!save.isPending) setDraft(null);
  };
  return (
    <SettingsSection
      title={t(($) => $.bots.routes.title)}
      description={disabledReason ?? t(($) => $.bots.routes.description)}
      action={
        <Button
          size="sm"
          className="h-11 sm:h-8"
          disabled={Boolean(disabledReason)}
          onClick={() => {
            setEditing(false);
            setDraft({ projectId: null, botId: "", channelRef: "" });
          }}
        >
          {t(($) => $.bots.routes.add)}
        </Button>
      }
    >
      <SettingsCard>
        {list.isLoading ? (
          <SettingsRow label={t(($) => $.bots.mcp.loading)}>{null}</SettingsRow>
        ) : list.isError || projects.isError ? (
          <SettingsRow label={t(($) => $.bots.routes.load_failed)}>
            <Button
              variant="outline"
              className="h-11 sm:h-8"
              onClick={() => {
                void list.refetch();
                void projects.refetch();
              }}
            >
              {t(($) => $.bots.mcp.retry)}
            </Button>
          </SettingsRow>
        ) : !list.data?.length ? (
          <SettingsRow label={t(($) => $.bots.routes.empty)}>
            {null}
          </SettingsRow>
        ) : (
          list.data.map((route) => (
            <SettingsRow
              key={route.projectId ?? DEFAULT_ROUTE}
              label={projectName(route.projectId)}
              description={
                <span className="break-all">
                  {bots.find((bot) => bot.id === route.botId)?.name ??
                    t(($) => $.bots.routes.unknown_bot)}{" "}
                  · {route.channelRef}
                </span>
              }
            >
              <div className="flex flex-wrap gap-1">
                <Button
                  size="sm"
                  variant="outline"
                  className="h-11 sm:h-8"
                  disabled={!canManage}
                  onClick={() => {
                    setEditing(true);
                    setDraft(route);
                  }}
                >
                  {t(($) => $.bots.routes.edit)}
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-11 sm:h-8"
                  disabled={!canManage}
                  onClick={() => setConfirm(route)}
                >
                  {t(($) => $.bots.routes.remove)}
                </Button>
              </div>
            </SettingsRow>
          ))
        )}
      </SettingsCard>
      <Dialog
        open={Boolean(draft)}
        onOpenChange={(open) => {
          if (!open) close();
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>
              {editing
                ? t(($) => $.bots.routes.edit)
                : t(($) => $.bots.routes.add)}
            </DialogTitle>
          </DialogHeader>
          {draft && (
            <form
              className="space-y-4"
              onSubmit={(e) => {
                e.preventDefault();
                if (!draft.botId || !draft.channelRef.trim() || save.isPending)
                  return;
                save.mutate(
                  { ...draft, channelRef: draft.channelRef.trim() },
                  { onSuccess: () => setDraft(null), onError: fail },
                );
              }}
            >
              <div className="space-y-1.5">
                <Label htmlFor="office-route-project">
                  {t(($) => $.bots.routes.project)}
                </Label>
                {editing ? (
                  <Input
                    id="office-route-project"
                    readOnly
                    value={projectName(draft.projectId)}
                  />
                ) : (
                  <Select
                    items={projectItems}
                    value={draft.projectId ?? DEFAULT_ROUTE}
                    disabled={save.isPending}
                    onValueChange={(value) =>
                      setDraft({
                        ...draft,
                        projectId: value === DEFAULT_ROUTE ? null : value,
                      })
                    }
                  >
                    <SelectTrigger
                      id="office-route-project"
                      className="w-full min-h-11"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {projectItems.map((item) => (
                        <SelectItem key={item.value} value={item.value}>
                          {item.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="office-route-bot">
                  {t(($) => $.bots.routes.bot)}
                </Label>
                <Select
                  items={botItems}
                  value={draft.botId}
                  disabled={save.isPending}
                  onValueChange={(value) =>
                    setDraft({ ...draft, botId: value ?? "" })
                  }
                >
                  <SelectTrigger
                    id="office-route-bot"
                    className="w-full min-h-11"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {botItems.map((item) => (
                      <SelectItem key={item.value} value={item.value}>
                        {item.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="office-route-channel">
                  {t(($) => $.bots.routes.channel)}
                </Label>
                <Input
                  id="office-route-channel"
                  aria-describedby="office-route-channel-hint"
                  value={draft.channelRef}
                  maxLength={255}
                  disabled={save.isPending}
                  onChange={(e) =>
                    setDraft({ ...draft, channelRef: e.target.value })
                  }
                />
                <p
                  id="office-route-channel-hint"
                  className="text-caption text-muted-foreground"
                >
                  {t(($) => $.bots.routes.channel_hint)}
                </p>
              </div>
              <DialogFooter>
                <Button
                  type="button"
                  variant="outline"
                  className="min-h-11"
                  disabled={save.isPending}
                  onClick={close}
                >
                  {t(($) => $.bots.mcp.cancel)}
                </Button>
                <Button
                  type="submit"
                  className="min-h-11"
                  disabled={
                    save.isPending || !draft.botId || !draft.channelRef.trim()
                  }
                >
                  {t(($) => $.bots.routes.save)}
                </Button>
              </DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
      <AlertDialog
        open={Boolean(confirm)}
        onOpenChange={(open) => {
          if (!open && !remove.isPending) setConfirm(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.bots.routes.remove_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirm?.projectId
                ? t(($) => $.bots.routes.remove_description)
                : t(($) => $.bots.routes.remove_default_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={remove.isPending} className="min-h-11">
              {t(($) => $.bots.mcp.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              className="min-h-11"
              disabled={remove.isPending}
              onClick={(e) => {
                e.preventDefault();
                if (confirm)
                  remove.mutate(confirm.projectId, {
                    onSuccess: () => setConfirm(null),
                    onError: fail,
                  });
              }}
            >
              {t(($) => $.bots.routes.remove)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  );
}
