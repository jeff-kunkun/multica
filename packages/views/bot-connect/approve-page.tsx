"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Button } from "@multica/ui/components/ui/button";
import { ApiError, api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import type { BotConnectInfo } from "@multica/core/types";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";

type Failure = "missing_code" | "not_found" | "not_admin" | "closed" | "unknown";

function failureOf(err: unknown): Failure {
  if (err instanceof ApiError) {
    if (err.status === 404) return "not_found";
    if (err.status === 403) return "not_admin";
    if (err.status === 409) return "closed";
  }
  return "unknown";
}

// BotConnectApprovePage is where a Bot's "please let me in" link lands
// (DENE-2009). One yes connects it: the Bot collects its own key, so the
// person never sees or copies one.
export function BotConnectApprovePage({ code }: { code: string | null }) {
  const { t } = useT("common");
  const user = useAuthStore((s) => s.user);
  const isAuthLoading = useAuthStore((s) => s.isLoading);
  const navigation = useNavigation();
  const signedIn = !!user && !isAuthLoading;

  const request = useQuery({
    queryKey: ["bot-connect", code],
    queryFn: () => api.getBotConnect(code ?? ""),
    enabled: signedIn && !!code,
    retry: false,
  });
  const workspaces = useQuery({ ...workspaceListOptions(), enabled: signedIn });
  const [workspaceId, setWorkspaceId] = useState("");
  useEffect(() => {
    if (!workspaceId && workspaces.data?.[0]) setWorkspaceId(workspaces.data[0].id);
  }, [workspaces.data, workspaceId]);

  const [decided, setDecided] = useState<BotConnectInfo | null>(null);
  const approve = useMutation({
    mutationFn: () => api.approveBotConnect(code ?? "", workspaceId),
    onSuccess: setDecided,
  });
  const deny = useMutation({
    mutationFn: () => api.denyBotConnect(code ?? ""),
    onSuccess: setDecided,
  });

  const info = decided ?? request.data;
  const failure: Failure | null = !code
    ? "missing_code"
    : request.error
      ? failureOf(request.error)
      : approve.error
        ? failureOf(approve.error)
        : deny.error
          ? failureOf(deny.error)
          : null;
  const workspaceName = workspaces.data?.find((w) => w.id === (info?.workspace_id ?? workspaceId))?.name ?? "";

  let body: React.ReactNode;
  if (failure) {
    body = (
      <>
        <p className="break-words text-body font-medium">{t(($) => $.bot_connect.error_title)}</p>
        <p className="break-words text-caption text-muted-foreground">
          {failure === "missing_code"
            ? t(($) => $.bot_connect.error_missing_code)
            : failure === "not_found"
              ? t(($) => $.bot_connect.error_not_found)
              : failure === "not_admin"
                ? t(($) => $.bot_connect.error_not_admin)
                : failure === "closed"
                  ? t(($) => $.bot_connect.error_closed)
                  : t(($) => $.bot_connect.error_unknown)}
        </p>
      </>
    );
  } else if (!isAuthLoading && !user) {
    body = (
      <>
        <p className="break-words text-body text-muted-foreground">{t(($) => $.bot_connect.needs_auth)}</p>
        <Button
          size="sm"
          className="min-h-11 w-full sm:w-auto"
          onClick={() =>
            navigation.push(`/login?next=${encodeURIComponent(`/bot-connect?code=${encodeURIComponent(code ?? "")}`)}`)
          }
        >
          {t(($) => $.bot_connect.sign_in)}
        </Button>
      </>
    );
  } else if (!info) {
    body = <p className="break-words text-body text-muted-foreground">{t(($) => $.bot_connect.loading)}</p>;
  } else if (info.status === "approved" || info.status === "claimed") {
    body = (
      <>
        <p className="break-words text-body font-medium">
          {t(($) => $.bot_connect.done_title, { bot: info.bot_name, workspace: workspaceName })}
        </p>
        <p className="break-words text-caption text-muted-foreground">{t(($) => $.bot_connect.done_description)}</p>
      </>
    );
  } else if (info.status === "denied") {
    body = <p className="break-words text-body font-medium">{t(($) => $.bot_connect.denied_title)}</p>;
  } else if (info.status === "expired") {
    body = <p className="break-words text-body font-medium">{t(($) => $.bot_connect.expired_title)}</p>;
  } else {
    const busy = approve.isPending || deny.isPending;
    body = (
      <>
        <p className="break-words text-body">{t(($) => $.bot_connect.ask, { bot: info.bot_name })}</p>
        <p className="break-words text-caption text-muted-foreground">{t(($) => $.bot_connect.explain)}</p>
        <p className="break-words text-micro text-muted-foreground">
          {t(($) => $.bot_connect.code_hint, { code: info.code })}
        </p>
        {(workspaces.data?.length ?? 0) > 1 && (
          <label className="block space-y-1">
            <span className="text-caption text-muted-foreground">{t(($) => $.bot_connect.workspace_label)}</span>
            <select
              className="min-h-11 w-full rounded-md border bg-background px-3 text-body"
              value={workspaceId}
              onChange={(e) => setWorkspaceId(e.target.value)}
            >
              {workspaces.data?.map((w) => (
                <option key={w.id} value={w.id}>
                  {w.name}
                </option>
              ))}
            </select>
          </label>
        )}
        {workspaces.data?.length === 0 && (
          <p className="break-words text-caption text-destructive">{t(($) => $.bot_connect.no_workspace)}</p>
        )}
        <div className="flex flex-col gap-2 sm:flex-row">
          <Button className="min-h-11 flex-1" disabled={busy || !workspaceId} onClick={() => approve.mutate()}>
            {t(($) => $.bot_connect.approve)}
          </Button>
          <Button variant="outline" className="min-h-11" disabled={busy} onClick={() => deny.mutate()}>
            {t(($) => $.bot_connect.deny)}
          </Button>
        </div>
      </>
    );
  }

  return (
    <div className="mx-auto flex min-h-dvh w-full max-w-md flex-col items-center justify-center p-4 sm:p-6">
      <Card className="w-full min-w-0">
        <CardContent className="min-w-0 space-y-4">
          <h1 className="break-words text-title font-semibold">{t(($) => $.bot_connect.page_title)}</h1>
          {body}
        </CardContent>
      </Card>
    </div>
  );
}
