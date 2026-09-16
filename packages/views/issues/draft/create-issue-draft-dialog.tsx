"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2, Sparkles } from "lucide-react";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueDraftListOptions, useStartIssueDraft } from "@multica/core/issue-drafts";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  isRuntimeUsableForUser,
  runtimeDisplayName,
  runtimeListOptions,
} from "@multica/core/runtimes";
import type { IssueDraftSummary } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent } from "@multica/ui/components/ui/dialog";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { UnfinishedIssueDraftsBanner } from "./unfinished-issue-drafts";

/**
 * The entry point to aligning: describe the request, then leave.
 *
 * This dialog's whole job is to open the conversation and hand off — it awaits
 * the server's session id, sends the user's own first turn, and navigates to
 * the alignment page. The conversation itself is deliberately NOT here: it is a
 * durable object that is left and resumed, and a modal cannot be refreshed
 * into, linked to, or restored as a desktop tab.
 *
 * Opening a conversation is one decision, so this face asks for one thing: what
 * to align on. Which machine runs it is decided for the user — the alignment
 * runs on the first runtime they may use, and the page's preview panel is where
 * that choice is visible and changeable afterwards. The single case that stops
 * the conversation from starting at all — nothing usable to run on, or the
 * chosen machine offline — is stated outright instead of being left for the
 * user to infer from a disabled button.
 */
export function CreateIssueDraftDialog({
  onClose,
  data,
}: {
  onClose: () => void;
  data?: Record<string, unknown> | null;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const currentUserId = useAuthStore((state) => state.user?.id ?? null);

  const seededRequest =
    typeof data?.initial_description === "string" ? data.initial_description : "";
  const [request, setRequest] = useState(seededRequest);
  const [runtimeId, setRuntimeId] = useState("");

  const draftsQuery = useQuery(issueDraftListOptions(wsId));
  const runtimesQuery = useQuery(runtimeListOptions(wsId));
  const start = useStartIssueDraft(wsId);

  const runtimes = runtimesQuery.data ?? [];
  const usableRuntimes = useMemo(
    () =>
      (runtimesQuery.data ?? []).filter((runtime) =>
        isRuntimeUsableForUser(runtime, currentUserId),
      ),
    [runtimesQuery.data, currentUserId],
  );

  // Picking the runtime moved out of this dialog with the picker, but choosing
  // one did not: the alignment has to run somewhere, and the user is never
  // asked where. Online first, then the user's own machines, then anything
  // else in the workspace they may use.
  //
  // Online has to outrank "mine", which the picker's own order did not: the
  // list arrives ordered by `created_at` and `isRuntimeUsableForUser` says
  // nothing about status, so "first machine I own" is really "oldest machine I
  // own". The picker could survive seeding that one offline — the user just
  // opened it and chose another. Here there is nothing to open, so seeding an
  // offline machine while an online one sits in the same list would leave the
  // dialog stating it cannot start and offering no way to fix it.
  const seededRuntimeId = useMemo(() => {
    const online = usableRuntimes.filter((runtime) => runtime.status === "online");
    // Falls back to the offline set only so the message below can name a
    // machine; submitting still requires an online one.
    const pool = online.length > 0 ? online : usableRuntimes;
    return (
      (pool.find((runtime) => runtime.owner_id === currentUserId) ?? pool[0])
        ?.id ?? ""
    );
  }, [usableRuntimes, currentUserId]);

  // Fills an empty selection only. The picker seeded through `onSelect` as
  // soon as runtimes arrived (over WS included); a derived seed plus this
  // effect keeps that timing without a mounted picker to call back into.
  useEffect(() => {
    if (runtimeId !== "" || !seededRuntimeId) return;
    setRuntimeId(seededRuntimeId);
  }, [runtimeId, seededRuntimeId]);

  const selectedRuntime =
    runtimes.find((runtime) => runtime.id === runtimeId) ?? null;
  const runtimeOnline = selectedRuntime?.status === "online";
  const runtimesLoading = runtimesQuery.isLoading;
  const hasUsableRuntime = usableRuntimes.length > 0;

  const drafts: IssueDraftSummary[] = draftsQuery.data ?? [];
  const canSubmit =
    request.trim().length > 0 && runtimeOnline && !start.isPending;

  const resume = (draftId: string) => {
    onClose();
    navigation.push(paths.newIssueDraft(draftId));
  };

  const submit = async () => {
    if (!canSubmit || !selectedRuntime) return;
    const result = await start.mutateAsync({
      runtimeId: selectedRuntime.id,
      request,
    });
    onClose();
    // Navigating even when the first turn failed: the draft exists and holds
    // the request, so staying would only invite the user to create a second
    // one. The page's composer is where a lost turn is resent.
    navigation.push(paths.newIssueDraft(result.draftId));
  };

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="gap-0 p-0 sm:max-w-xl">
        <div className="max-h-[80dvh] overflow-y-auto px-6 py-6">
          <UnfinishedIssueDraftsBanner drafts={drafts} onResume={resume} />

          <span className="flex size-11 items-center justify-center rounded-lg bg-primary/10 text-primary">
            <Sparkles className="size-5" aria-hidden="true" />
          </span>
          <h2 className="mt-4 text-title-sm font-semibold">
            {t(($) => $.alignment.entry_title)}
          </h2>
          <p className="mt-2 text-body leading-6 text-muted-foreground">
            {t(($) => $.alignment.entry_description)}
          </p>

          <div className="mt-5 space-y-4">
            <Textarea
              value={request}
              rows={4}
              autoFocus
              aria-label={t(($) => $.alignment.entry_placeholder)}
              placeholder={t(($) => $.alignment.entry_placeholder)}
              onChange={(event) => setRequest(event.target.value)}
            />

            {!runtimesLoading && !hasUsableRuntime ? (
              <p className="text-body text-muted-foreground">
                {t(($) => $.alignment.entry_no_runtime)}
              </p>
            ) : null}

            {/* A seeded but offline runtime blocks submitting, which a
                disabled button alone never explains. Only the machines list
                can fix it, so this stays a statement, not a second action. */}
            {selectedRuntime && !runtimeOnline ? (
              <p role="status" className="text-body text-destructive">
                {t(($) => $.alignment.entry_runtime_offline, {
                  name: runtimeDisplayName(selectedRuntime),
                })}
              </p>
            ) : null}

            {start.isError ? (
              <p role="alert" className="text-body text-destructive">
                {t(($) => $.alignment.entry_failed)}
              </p>
            ) : null}
          </div>

          <div className="mt-6 flex items-center justify-end gap-2">
            <Button variant="ghost" onClick={onClose} disabled={start.isPending}>
              {t(($) => $.alignment.entry_cancel)}
            </Button>
            {!runtimesLoading && !hasUsableRuntime ? (
              <Button
                render={<AppLink href={paths.runtimes()} />}
                nativeButton={false}
              >
                {t(($) => $.alignment.entry_connect_runtime)}
              </Button>
            ) : (
              <Button onClick={() => void submit()} disabled={!canSubmit}>
                {start.isPending ? (
                  <Loader2 className="size-4 animate-spin" aria-hidden="true" />
                ) : null}
                {start.isPending
                  ? t(($) => $.alignment.entry_submitting)
                  : t(($) => $.alignment.entry_submit)}
              </Button>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
