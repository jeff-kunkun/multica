"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";
import { SettingsCard, SettingsSection } from "./settings-layout";

const logExportConfigKey = (wsId: string) => ["workspaces", wsId, "log-export-config"] as const;

/**
 * The workspace log repository: where reported log bundles are committed, so
 * the issue comment carries a link instead of a large attachment. The token is
 * write-only — the server only ever says whether one is stored.
 */
export function LogRepositorySection({ wsId, canManage }: { wsId: string; canManage: boolean }) {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const { data: config } = useQuery({
    queryKey: logExportConfigKey(wsId),
    queryFn: () => api.getLogExportConfig(wsId),
  });

  const [repoUrl, setRepoUrl] = useState("");
  const [branch, setBranch] = useState("");
  const [token, setToken] = useState("");

  useEffect(() => {
    if (!config) return;
    setRepoUrl(config.repo_url ?? "");
    setBranch(config.branch ?? "");
    setToken("");
  }, [config]);

  const save = useMutation({
    mutationFn: (data: { repo_url: string; branch: string; token?: string }) =>
      api.updateLogExportConfig(wsId, data),
    onSuccess: (next) => {
      qc.setQueryData(logExportConfigKey(wsId), next);
      toast.success(t(($) => $.log_repository.saved));
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : t(($) => $.log_repository.save_failed));
    },
  });

  const hasToken = config?.has_token === true;
  const tokenStorable = config?.token_storable !== false;
  const dirty =
    repoUrl.trim() !== (config?.repo_url ?? "") ||
    branch.trim() !== (config?.branch ?? "") ||
    token.trim() !== "";

  const submit = () => {
    const trimmedToken = token.trim();
    save.mutate({
      repo_url: repoUrl.trim(),
      branch: branch.trim(),
      // Omitted keeps the stored token; only a typed value replaces it.
      ...(trimmedToken !== "" ? { token: trimmedToken } : {}),
    });
  };

  return (
    <SettingsSection
      title={t(($) => $.log_repository.title)}
      description={t(($) => $.log_repository.description)}
    >
      <SettingsCard>
        <div className="grid gap-3 px-4 py-3.5">
          <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,0.4fr)]">
            <Input
              type="text"
              name="log-repository-url"
              autoComplete="off"
              spellCheck={false}
              aria-label={t(($) => $.log_repository.url_label)}
              placeholder={t(($) => $.log_repository.url_placeholder)}
              value={repoUrl}
              onChange={(event) => setRepoUrl(event.target.value)}
              disabled={!canManage}
              className="font-mono text-caption"
            />
            <Input
              type="text"
              name="log-repository-branch"
              autoComplete="off"
              spellCheck={false}
              aria-label={t(($) => $.log_repository.branch_label)}
              placeholder={t(($) => $.log_repository.branch_placeholder)}
              value={branch}
              onChange={(event) => setBranch(event.target.value)}
              disabled={!canManage}
              className="font-mono text-caption"
            />
          </div>
          {canManage ? (
            <Input
              type="password"
              name="log-repository-token"
              autoComplete="new-password"
              aria-label={t(($) => $.log_repository.token_label)}
              placeholder={
                hasToken
                  ? t(($) => $.log_repository.token_placeholder_stored)
                  : t(($) => $.log_repository.token_placeholder)
              }
              value={token}
              onChange={(event) => setToken(event.target.value)}
              disabled={!tokenStorable}
              className="font-mono text-caption"
            />
          ) : null}
          <p className="text-caption text-muted-foreground">
            {!tokenStorable
              ? t(($) => $.log_repository.token_not_storable)
              : t(($) => $.log_repository.hint)}
          </p>
          {canManage ? (
            <div className="flex flex-wrap justify-end gap-2">
              {hasToken ? (
                <Button
                  variant="outline"
                  size="sm"
                  disabled={save.isPending}
                  onClick={() =>
                    save.mutate({
                      repo_url: (config?.repo_url ?? "").trim(),
                      branch: (config?.branch ?? "").trim(),
                      token: "",
                    })
                  }
                >
                  {t(($) => $.log_repository.clear_token)}
                </Button>
              ) : null}
              <Button size="sm" disabled={!dirty || save.isPending} onClick={submit}>
                {t(($) => $.log_repository.save)}
              </Button>
            </div>
          ) : null}
        </div>
      </SettingsCard>
    </SettingsSection>
  );
}
