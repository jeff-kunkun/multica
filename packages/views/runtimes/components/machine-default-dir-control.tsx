"use client";

import { useEffect, useId, useState } from "react";
import { ApiError } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useFeatureEnabled } from "@multica/core/config";
import { OFFICE_DEFAULT_DIR_FLAG } from "@multica/core/feature-flags";
import { useWorkspaceId } from "@multica/core/hooks";
import { useUpdateRuntime } from "@multica/core/runtimes/mutations";
import type { AgentRuntime } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

// Mirrors the server's isAbsoluteLocalPath: POSIX, UNC and drive-letter paths.
function isAbsolutePath(path: string): boolean {
  return path.startsWith("/") || path.startsWith("\\\\") || /^[A-Za-z]:[\\/]/.test(path);
}

/**
 * The machine's default working directory (DENE-2667 PR 11): where a run starts
 * when its project binds no folder. Saved through the same runtime PATCH as
 * `multica runtime update --default-dir`. Only the machine's owner sees it —
 * the server sends the path to nobody else — and only in office test builds.
 */
export function MachineDefaultDirControl({ runtimes }: { runtimes: AgentRuntime[] }) {
  const enabled = useFeatureEnabled(OFFICE_DEFAULT_DIR_FLAG);
  const userId = useAuthStore((s) => s.user?.id);
  const target = runtimes.find((rt) => rt.runtime_mode === "local" && !!userId && rt.owner_id === userId);
  if (!enabled || !target) return null;
  return <DefaultDirForm target={target} online={runtimes.some((rt) => rt.status === "online")} />;
}

function DefaultDirForm({ target, online }: { target: AgentRuntime; online: boolean }) {
  const { t } = useT("runtimes");
  const { t: common } = useT("common");
  const mutation = useUpdateRuntime(useWorkspaceId());
  const id = useId();
  const saved = target.default_work_dir;
  const savedPath = saved?.path ?? "";
  const [draft, setDraft] = useState(savedPath);
  useEffect(() => { setDraft(savedPath); }, [savedPath]);

  const trimmed = draft.trim();
  const relative = trimmed !== "" && !isAbsolutePath(trimmed);
  const changed = trimmed !== savedPath;
  const save = (path: string) => mutation.mutate({ runtimeId: target.id, patch: { default_work_dir: path } });

  let status: { text: string; tone: "ok" | "warn" | "muted" } | null = null;
  if (relative) {
    status = { text: t(($) => $.machine.default_dir.relative), tone: "warn" };
  } else if (saved && !changed) {
    if (saved.exists === true) status = { text: t(($) => $.machine.default_dir.exists), tone: "ok" };
    else if (saved.exists === false) status = { text: t(($) => $.machine.default_dir.missing), tone: "warn" };
    else status = { text: t(($) => online ? $.machine.default_dir.checking : $.machine.default_dir.checking_offline), tone: "muted" };
  }

  return (
    <form
      className="flex flex-wrap items-center gap-2"
      data-testid="machine-default-dir"
      onSubmit={(event) => {
        event.preventDefault();
        if (changed && !relative && !mutation.isPending) save(trimmed);
      }}
    >
      <div className="w-full text-caption">
        <label htmlFor={id} className="block">{t(($) => $.machine.default_dir.label)}</label>
        <span className="text-muted-foreground">{t(($) => $.machine.default_dir.hint)}</span>
      </div>
      <Input
        id={id}
        className="h-11 min-w-0 max-w-xl flex-1 basis-60 font-mono"
        value={draft}
        placeholder={t(($) => $.machine.default_dir.placeholder)}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        aria-invalid={relative || saved?.exists === false ? true : undefined}
        aria-describedby={status ? `${id}-status` : undefined}
        onChange={(event) => setDraft(event.target.value)}
        disabled={mutation.isPending}
      />
      <Button type="submit" variant="outline" className="h-11" disabled={!changed || relative || mutation.isPending}>
        {common(($) => $.save)}
      </Button>
      {savedPath && (
        <Button type="button" variant="ghost" className="h-11" disabled={mutation.isPending} onClick={() => save("")}>
          {t(($) => $.machine.default_dir.clear)}
        </Button>
      )}
      {status && (
        <p
          id={`${id}-status`}
          role={status.tone === "warn" ? "alert" : "status"}
          className={cn(
            "w-full text-caption",
            status.tone === "ok" && "text-success",
            status.tone === "warn" && "text-destructive",
            status.tone === "muted" && "text-muted-foreground",
          )}
        >
          {status.text}
        </p>
      )}
      {mutation.isError && (
        <p role="alert" className="w-full text-caption text-destructive">
          {errorCode(mutation.error) === "invalid_default_work_dir"
            ? t(($) => $.machine.default_dir.invalid)
            : errorCode(mutation.error) === "default_work_dir_owner_required"
              ? t(($) => $.machine.default_dir.owner_only)
              : t(($) => $.machine.default_dir.failed)}
        </p>
      )}
    </form>
  );
}

function errorCode(error: unknown): unknown {
  if (!(error instanceof ApiError) || typeof error.body !== "object" || !error.body) return undefined;
  return (error.body as { code?: unknown }).code;
}
