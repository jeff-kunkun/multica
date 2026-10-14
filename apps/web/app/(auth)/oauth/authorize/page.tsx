"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { api } from "@multica/core/api";
import { paths } from "@multica/core/paths";
import type { OAuthBotWorkspace } from "@multica/core/types";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@multica/ui/components/ui/card";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useT } from "@multica/views/i18n";
import { Loader2 } from "lucide-react";

const NEW_BOT = "new";

interface Choice {
  value: string;
  label: string;
  workspaceId: string;
  botId: string | null;
}

// One list of "existing Bot" and "new Bot" choices; workspace names only
// appear when the owner has more than one workspace.
function buildChoices(
  workspaces: OAuthBotWorkspace[],
  newLabel: (workspace: string | null) => string,
): Choice[] {
  const many = workspaces.length > 1;
  return workspaces.flatMap((ws) => [
    {
      value: `${ws.id}:${NEW_BOT}`,
      label: newLabel(many ? ws.name : null),
      workspaceId: ws.id,
      botId: null,
    },
    ...ws.bots.map((bot) => ({
      value: `${ws.id}:${bot.id}`,
      label: many ? `${bot.name} · ${ws.name}` : bot.name,
      workspaceId: ws.id,
      botId: bot.id,
    })),
  ]);
}

function redirectHost(raw: string): string {
  try {
    return new URL(raw).host || raw;
  } catch {
    return raw;
  }
}

function withQuery(raw: string, params: Record<string, string>): string {
  const url = new URL(raw);
  for (const [key, value] of Object.entries(params)) {
    if (value) url.searchParams.set(key, value);
  }
  return url.toString();
}

function AuthorizeContent() {
  const { t } = useT("auth");
  const router = useRouter();
  const searchParams = useSearchParams();
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);

  const clientId = searchParams.get("client_id") ?? "";
  const redirectUri = searchParams.get("redirect_uri") ?? "";
  const state = searchParams.get("state") ?? "";
  const codeChallenge = searchParams.get("code_challenge") ?? "";
  const codeChallengeMethod = searchParams.get("code_challenge_method") ?? "";
  const validRequest =
    searchParams.get("response_type") === "code" &&
    redirectUri !== "" &&
    codeChallenge !== "" &&
    URL.canParse(redirectUri);
  const host = redirectHost(redirectUri);

  useEffect(() => {
    if (!isLoading && !user) {
      const here = `/oauth/authorize?${searchParams.toString()}`;
      router.replace(`${paths.login()}?next=${encodeURIComponent(here)}`);
    }
  }, [isLoading, user, router, searchParams]);

  const { data: workspaces, isPending } = useQuery({
    queryKey: ["oauth", "bots"],
    queryFn: () => api.listOAuthBots(),
    enabled: !!user && validRequest,
  });

  const choices = buildChoices(workspaces ?? [], (workspace) =>
    workspace
      ? t(($) => $.web.oauth.new_bot_in, { workspace })
      : t(($) => $.web.oauth.new_bot),
  );
  const [selected, setSelected] = useState<string | null>(null);
  const [name, setName] = useState(host);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const choice = choices.find((c) => c.value === selected) ?? choices[0];

  if (isLoading || !user) return null;

  const approve = async () => {
    if (!choice) return;
    setSubmitting(true);
    setError("");
    try {
      const target = await api.approveOAuth({
        workspace_id: choice.workspaceId,
        ...(choice.botId ? { bot_id: choice.botId } : { bot_name: name.trim() }),
        client_id: clientId,
        redirect_uri: redirectUri,
        code_challenge: codeChallenge,
        code_challenge_method: codeChallengeMethod,
        state,
      });
      window.location.href = target;
    } catch (err) {
      setError(err instanceof Error ? err.message : t(($) => $.web.oauth.failed));
      setSubmitting(false);
    }
  };

  const deny = () => {
    window.location.href = withQuery(redirectUri, { error: "access_denied", state });
  };

  let body: React.ReactNode;
  if (!validRequest) {
    body = <p className="text-body-sm text-muted-foreground">{t(($) => $.web.oauth.invalid_request)}</p>;
  } else if (isPending) {
    body = (
      <div className="flex justify-center py-4">
        <Loader2 className="size-5 animate-spin text-muted-foreground" />
      </div>
    );
  } else if (!choice) {
    body = <p className="text-body-sm text-muted-foreground">{t(($) => $.web.oauth.not_owner)}</p>;
  } else {
    body = (
      <div className="flex flex-col gap-4">
        <div className="flex flex-col gap-2">
          <Label htmlFor="oauth-bot">{t(($) => $.web.oauth.bot_label)}</Label>
          <Select
            items={choices.map((c) => ({ value: c.value, label: c.label }))}
            value={choice.value}
            onValueChange={(next) => next && setSelected(next)}
          >
            <SelectTrigger id="oauth-bot" className="w-full">
              <SelectValue>{() => choice.label}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {choices.map((c) => (
                <SelectItem key={c.value} value={c.value}>
                  {c.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {choice.botId && (
            <p className="text-body-sm text-muted-foreground">{t(($) => $.web.oauth.replaces_token)}</p>
          )}
        </div>
        {!choice.botId && (
          <div className="flex flex-col gap-2">
            <Label htmlFor="oauth-bot-name">{t(($) => $.web.oauth.name_label)}</Label>
            <Input
              id="oauth-bot-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={100}
            />
          </div>
        )}
        {error && <p className="text-body-sm text-destructive">{error}</p>}
      </div>
    );
  }

  const canApprove = validRequest && !!choice && (!!choice.botId || name.trim() !== "");

  return (
    <div className="flex min-h-screen items-center justify-center px-4">
      <Card className="w-full max-w-md">
        <CardHeader>
          <CardTitle className="text-display-sm">{t(($) => $.web.oauth.title)}</CardTitle>
          {validRequest && (
            <CardDescription className="break-all">
              {t(($) => $.web.oauth.description, { host })}
            </CardDescription>
          )}
        </CardHeader>
        <CardContent>{body}</CardContent>
        {validRequest && (
          <CardFooter className="flex justify-end gap-2">
            <Button type="button" variant="outline" onClick={deny} disabled={submitting}>
              {t(($) => $.web.oauth.cancel)}
            </Button>
            <Button
              type="button"
              onClick={approve}
              disabled={!canApprove || submitting}
              aria-busy={submitting || undefined}
            >
              {submitting && <Loader2 className="size-4 animate-spin" />}
              {t(($) => $.web.oauth.allow)}
            </Button>
          </CardFooter>
        )}
      </Card>
    </div>
  );
}

export default function OAuthAuthorizePage() {
  return (
    <Suspense fallback={null}>
      <AuthorizeContent />
    </Suspense>
  );
}
